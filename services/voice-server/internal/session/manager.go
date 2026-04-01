package session

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"voice-server/internal/domain"
	"voice-server/internal/gamedb"
	"voice-server/internal/hub"
	"voice-server/internal/pipeline"
	"voice-server/internal/voiceclient"
)

// Manager — одна глобальная сессия; новый старт отменяет предыдущую.
type Manager struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	vc     *voiceclient.Client
	hub    *hub.Hub
	store  *gamedb.Store
	status string
	mode   string

	muGame       sync.Mutex
	activeGameID string
}

func NewManager(vc *voiceclient.Client, h *hub.Hub, store *gamedb.Store) *Manager {
	return &Manager{
		vc:     vc,
		hub:    h,
		store:  store,
		status: "idle",
		mode:   "",
	}
}

func (m *Manager) ActiveGameID() string {
	m.muGame.Lock()
	defer m.muGame.Unlock()
	return m.activeGameID
}

func (m *Manager) setActiveGameID(id string) {
	m.muGame.Lock()
	m.activeGameID = id
	m.muGame.Unlock()
}

func (m *Manager) clearActiveGameID(id string) {
	m.muGame.Lock()
	if m.activeGameID == id {
		m.activeGameID = ""
	}
	m.muGame.Unlock()
}

func (m *Manager) Status() (status, mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.status
	if st == "" {
		st = "idle"
	}
	return st, m.mode
}

func (m *Manager) onSegment(gameID string) func(domain.Segment) {
	seq := 0
	return func(s domain.Segment) {
		seq++
		wall := time.Now().Format("15:04:05")
		if m.store != nil && gameID != "" {
			if err := m.store.InsertSegment(gameID, seq, s, wall); err != nil {
				log.Printf("[session] gamedb insert segment seq=%d: %v", seq, err)
			}
		}
		msg := map[string]interface{}{
			"type":      "segment",
			"speaker":   s.Speaker,
			"text":      s.Text,
			"abs_start": s.AbsStart,
			"abs_end":   s.AbsEnd,
			"ts":        wall,
			"seq":       seq,
		}
		if s.VoiceID != "" {
			msg["voice_id"] = s.VoiceID
		}
		if s.MatchScore != nil {
			msg["match_score"] = *s.MatchScore
		}
		if gameID != "" {
			msg["game_session_id"] = gameID
		}
		m.hub.BroadcastJSON(msg)
	}
}

func (m *Manager) beginSession() context.Context {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()
	return ctx
}

func (m *Manager) Stop() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.status = "idle"
	m.mode = ""
	m.mu.Unlock()
	m.muGame.Lock()
	m.activeGameID = ""
	m.muGame.Unlock()
	m.hub.BroadcastStatus("idle")
}

func (m *Manager) setRunning(mode string) {
	m.mu.Lock()
	m.status = "running"
	if mode != "" {
		m.mode = mode
	}
	m.mu.Unlock()
}

func (m *Manager) setProcessing() {
	m.mu.Lock()
	m.status = "processing"
	m.mode = "ingest"
	m.mu.Unlock()
}

func (m *Manager) deferIdle(gameID string) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.status = "idle"
	m.mode = ""
	m.mu.Unlock()
	if m.store != nil && gameID != "" {
		if err := m.store.EndSession(gameID); err != nil {
			log.Printf("[session] gamedb end session %s: %v", gameID, err)
		}
	}
	m.clearActiveGameID(gameID)
	m.hub.BroadcastStatus("idle")
}

// broadcastWorkerError sends a worker_error event to all WS clients.
func (m *Manager) broadcastWorkerError(err error) {
	log.Printf("[session] worker error, broadcasting to clients: %v", err)
	m.hub.BroadcastJSON(map[string]interface{}{
		"type":    "worker_error",
		"message": err.Error(),
	})
}

// broadcastSplitSuggestions fetches split candidates from the worker and
// broadcasts a voice_split_suggested event if any are found.
// Called after ingest/file jobs complete — never blocks the caller on error.
func (m *Manager) broadcastSplitSuggestions() {
	candidates, err := m.vc.GetSplitCandidates()
	if err != nil {
		log.Printf("[session] broadcastSplitSuggestions: %v", err)
		return
	}
	if len(candidates) == 0 {
		return
	}
	log.Printf("[session] split suggestions: %d candidate(s)", len(candidates))

	// Convert to a plain []map for JSON serialisation
	items := make([]map[string]interface{}, 0, len(candidates))
	for _, c := range candidates {
		items = append(items, map[string]interface{}{
			"voice_id":          c.VoiceID,
			"display_name":      c.DisplayName,
			"embedding_count":   c.EmbeddingCount,
			"max_pairwise_dist": c.MaxPairwiseDist,
			"cluster_a_size":    c.ClusterASize,
			"cluster_b_size":    c.ClusterBSize,
		})
	}
	m.hub.BroadcastJSON(map[string]interface{}{
		"type":       "voice_split_suggested",
		"candidates": items,
	})
}

func (m *Manager) StartIngest(filePath string, speakers int, removeAfter bool, originalFilename string) {
	gameID := m.openGameSession(gamedb.SessionMeta{
		CaptureSource:  gamedb.CaptureSourceFile,
		SessionMode:    gamedb.SessionModeIngest,
		SpeakersHint:   speakers,
		SourceFilename: originalFilename,
	})
	m.setProcessing()
	m.hub.BroadcastStatus("processing")
	ctx := m.beginSession()
	go func() {
		defer m.deferIdle(gameID)
		if removeAfter {
			defer func() { _ = os.Remove(filePath) }()
		}
		onSeg := m.onSegment(gameID)
		if err := pipeline.SendIngestFull(m.vc, speakers, onSeg, filePath); err != nil {
			log.Printf("[session] ingest error: %v", err)
			m.broadcastWorkerError(err)
			return
		}
		m.broadcastSplitSuggestions()
		_ = ctx
	}()
}

func (m *Manager) StartFile(filePath string, speakers int, originalFilename string) {
	src := originalFilename
	if src == "" {
		src = filepath.Base(filePath)
	}
	gameID := m.openGameSession(gamedb.SessionMeta{
		CaptureSource:  gamedb.CaptureSourceFile,
		SessionMode:    gamedb.SessionModeFile,
		SpeakersHint:   speakers,
		SourceFilename: src,
	})
	if err := m.vc.Reset(); err != nil {
		log.Printf("[session] reset before file: %v", err)
	}
	m.setRunning("file")
	m.hub.BroadcastStatus("running")
	ctx := m.beginSession()
	go func() {
		defer m.deferIdle(gameID)
		onSeg := m.onSegment(gameID)
		if err := pipeline.RunFile(ctx, m.vc, speakers, onSeg, filePath); err != nil {
			log.Printf("[session] runFile error: %v", err)
			m.broadcastWorkerError(err)
			return
		}
		m.broadcastSplitSuggestions()
	}()
}

func (m *Manager) StartRecord(speakers int) {
	gameID := m.openGameSession(gamedb.SessionMeta{
		CaptureSource: gamedb.CaptureSourceMicrophone,
		SessionMode:   gamedb.SessionModeRecord,
		SpeakersHint:  speakers,
	})
	if err := m.vc.Reset(); err != nil {
		log.Printf("[session] reset before record: %v", err)
	}
	m.setRunning("record")
	m.hub.BroadcastStatus("running")
	ctx := m.beginSession()
	go func() {
		defer m.deferIdle(gameID)
		pipeline.RunRecord(ctx, m.vc, speakers, m.onSegment(gameID))
		// Record mode: no split suggestions (streaming, not full-file)
	}()
}

func (m *Manager) openGameSession(meta gamedb.SessionMeta) string {
	if m.store == nil {
		return ""
	}
	id, err := m.store.CreateSession(meta)
	if err != nil {
		log.Printf("[session] gamedb create session: %v", err)
		return ""
	}
	log.Printf("[session] started game=%s mode=%s file=%q", id, meta.SessionMode, meta.SourceFilename)
	m.setActiveGameID(id)
	return id
}
