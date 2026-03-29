package gamedb

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"voice-server/internal/domain"

	_ "modernc.org/sqlite"
)

// CaptureSource — откуда бралась речь (для анализа: файл vs живая игра).
type CaptureSource string

const (
	CaptureSourceFile       CaptureSource = "file"       // загруженный/прогнанный файл
	CaptureSourceMicrophone CaptureSource = "microphone" // микрофон
)

// SessionMode — режим gateway (ingest / chunked file / live).
type SessionMode string

const (
	SessionModeIngest SessionMode = "ingest" // полный файл, обучение
	SessionModeFile   SessionMode = "file"   // чанки по файлу
	SessionModeRecord SessionMode = "record" // микрофон
)

// SessionMeta — метаданные партии для последующего анализа (структура игры, поведение).
type SessionMeta struct {
	CaptureSource  CaptureSource
	SessionMode    SessionMode
	SpeakersHint   int
	SourceFilename string // базовое имя файла, если применимо
}

// SessionRow — партия для API / экспорта.
type SessionRow struct {
	ID             string     `json:"id"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	CaptureSource  string     `json:"capture_source"`
	SessionMode    string     `json:"session_mode"`
	SpeakersHint   *int       `json:"speakers_hint,omitempty"`
	SourceFilename string     `json:"source_filename,omitempty"`
}

// SegmentRow — реплика в БД.
type SegmentRow struct {
	ID        int64   `json:"id"`
	SessionID string  `json:"session_id"`
	Seq       int     `json:"seq"`
	Speaker   string  `json:"speaker"`
	VoiceID   string  `json:"voice_id,omitempty"`
	Text      string  `json:"text"`
	AbsStart  float64 `json:"abs_start"`
	AbsEnd    float64 `json:"abs_end"`
	WallTime  string  `json:"wall_time"`
}

// Store — SQLite: партии и реплики (отдельно от реестра голосов на Python).
type Store struct {
	db *sql.DB
}

// Open открывает или создаёт файл БД, применяет схему.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS game_sessions (
	id TEXT PRIMARY KEY,
	started_at TEXT NOT NULL,
	ended_at TEXT,
	capture_source TEXT NOT NULL CHECK (capture_source IN ('file', 'microphone')),
	session_mode TEXT NOT NULL CHECK (session_mode IN ('ingest', 'file', 'record')),
	speakers_hint INTEGER,
	source_filename TEXT
);
CREATE INDEX IF NOT EXISTS idx_game_sessions_started ON game_sessions(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_game_sessions_mode ON game_sessions(session_mode);
CREATE INDEX IF NOT EXISTS idx_game_sessions_source ON game_sessions(capture_source);

CREATE TABLE IF NOT EXISTS game_segments (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id TEXT NOT NULL REFERENCES game_sessions(id) ON DELETE CASCADE,
	seq INTEGER NOT NULL,
	speaker TEXT NOT NULL,
	voice_id TEXT,
	text TEXT NOT NULL,
	abs_start REAL NOT NULL,
	abs_end REAL NOT NULL,
	wall_time TEXT NOT NULL,
	UNIQUE (session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_game_segments_session ON game_segments(session_id);
`)
	return err
}

// CreateSession создаёт партию и возвращает id.
func (s *Store) CreateSession(meta SessionMeta) (string, error) {
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	var fn sql.NullString
	if meta.SourceFilename != "" {
		fn = sql.NullString{String: meta.SourceFilename, Valid: true}
	}
	var sh sql.NullInt64
	if meta.SpeakersHint > 0 {
		sh = sql.NullInt64{Int64: int64(meta.SpeakersHint), Valid: true}
	}
	_, err := s.db.Exec(
		`INSERT INTO game_sessions (id, started_at, capture_source, session_mode, speakers_hint, source_filename)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, now, string(meta.CaptureSource), string(meta.SessionMode), sh, fn,
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

// EndSession проставляет ended_at.
func (s *Store) EndSession(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE game_sessions SET ended_at = ? WHERE id = ? AND ended_at IS NULL`, now, sessionID)
	return err
}

// InsertSegment сохраняет реплику в порядке следования.
func (s *Store) InsertSegment(sessionID string, seq int, seg domain.Segment, wallTime string) error {
	if sessionID == "" {
		return nil
	}
	var vid interface{}
	if seg.VoiceID != "" {
		vid = seg.VoiceID
	}
	_, err := s.db.Exec(
		`INSERT INTO game_segments (session_id, seq, speaker, voice_id, text, abs_start, abs_end, wall_time)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, seq, seg.Speaker, vid, seg.Text, seg.AbsStart, seg.AbsEnd, wallTime,
	)
	return err
}

// ListSessions возвращает последние партии.
func (s *Store) ListSessions(limit int) ([]SessionRow, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.Query(`
		SELECT id, started_at, ended_at, capture_source, session_mode, speakers_hint, source_filename
		FROM game_sessions ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		var started string
		var ended sql.NullString
		var sh sql.NullInt64
		var fn sql.NullString
		if err := rows.Scan(&r.ID, &started, &ended, &r.CaptureSource, &r.SessionMode, &sh, &fn); err != nil {
			return nil, err
		}
		t0, err := time.Parse(time.RFC3339, started)
		if err != nil {
			return nil, fmt.Errorf("started_at: %w", err)
		}
		r.StartedAt = t0
		if ended.Valid && ended.String != "" {
			t1, err := time.Parse(time.RFC3339, ended.String)
			if err == nil {
				r.EndedAt = &t1
			}
		}
		if sh.Valid {
			v := int(sh.Int64)
			r.SpeakersHint = &v
		}
		if fn.Valid {
			r.SourceFilename = fn.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetSession возвращает партию по id.
func (s *Store) GetSession(id string) (*SessionRow, error) {
	row := s.db.QueryRow(`
		SELECT id, started_at, ended_at, capture_source, session_mode, speakers_hint, source_filename
		FROM game_sessions WHERE id = ?`, id)
	var r SessionRow
	var started string
	var ended sql.NullString
	var sh sql.NullInt64
	var fn sql.NullString
	if err := row.Scan(&r.ID, &started, &ended, &r.CaptureSource, &r.SessionMode, &sh, &fn); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	t0, err := time.Parse(time.RFC3339, started)
	if err != nil {
		return nil, err
	}
	r.StartedAt = t0
	if ended.Valid && ended.String != "" {
		t1, err := time.Parse(time.RFC3339, ended.String)
		if err == nil {
			r.EndedAt = &t1
		}
	}
	if sh.Valid {
		v := int(sh.Int64)
		r.SpeakersHint = &v
	}
	if fn.Valid {
		r.SourceFilename = fn.String
	}
	return &r, nil
}

// ListSegments возвращает все реплики партии по порядку seq.
func (s *Store) ListSegments(sessionID string) ([]SegmentRow, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, seq, speaker, voice_id, text, abs_start, abs_end, wall_time
		FROM game_segments WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SegmentRow
	for rows.Next() {
		var r SegmentRow
		var vid sql.NullString
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Seq, &r.Speaker, &vid, &r.Text, &r.AbsStart, &r.AbsEnd, &r.WallTime); err != nil {
			return nil, err
		}
		if vid.Valid {
			r.VoiceID = vid.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
