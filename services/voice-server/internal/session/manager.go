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

// Manager — одна глобальная сессия; новый старт отменяет предыдущую (use case).
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

// ActiveGameID — id партии в локальной БД (для выгрузки / анализа), пусто если idle.
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
				log.Printf("gamedb segment: %v", err)
			}
		}
		msg := map[string]interface{}{
			"type":      "segment",
			"speaker":   s.Speaker,
			"text":      s.Text,
			"abs_start": s.AbsStart,
			"abs_end":   s.AbsEnd,
			"ts":        wall,
		}
		if s.VoiceID != "" {
			msg["voice_id"] = s.VoiceID
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
			log.Printf("gamedb end session: %v", err)
		}
	}
	m.clearActiveGameID(gameID)
	m.hub.BroadcastStatus("idle")
}

// StartIngest — full_file; originalFilename — имя загруженного файла для анализа.
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
			log.Printf("ingest: %v", err)
		}
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
		log.Printf("reset: %v", err)
	}
	m.setRunning("file")
	m.hub.BroadcastStatus("running")
	ctx := m.beginSession()
	go func() {
		defer m.deferIdle(gameID)
		onSeg := m.onSegment(gameID)
		if err := pipeline.RunFile(ctx, m.vc, speakers, onSeg, filePath); err != nil {
			log.Printf("runFile: %v", err)
		}
	}()
}

func (m *Manager) StartRecord(speakers int) {
	gameID := m.openGameSession(gamedb.SessionMeta{
		CaptureSource: gamedb.CaptureSourceMicrophone,
		SessionMode:   gamedb.SessionModeRecord,
		SpeakersHint:  speakers,
	})
	if err := m.vc.Reset(); err != nil {
		log.Printf("reset: %v", err)
	}
	m.setRunning("record")
	m.hub.BroadcastStatus("running")
	ctx := m.beginSession()
	go func() {
		defer m.deferIdle(gameID)
		pipeline.RunRecord(ctx, m.vc, speakers, m.onSegment(gameID))
	}()
}

func (m *Manager) openGameSession(meta gamedb.SessionMeta) string {
	if m.store == nil {
		return ""
	}
	id, err := m.store.CreateSession(meta)
	if err != nil {
		log.Printf("gamedb create session: %v", err)
		return ""
	}
	if meta.SourceFilename != "" {
		log.Printf("game session %s source file: %s", id, meta.SourceFilename)
	}
	m.setActiveGameID(id)
	return id
}
