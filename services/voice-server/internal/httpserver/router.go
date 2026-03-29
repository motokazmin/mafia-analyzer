package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"voice-server/internal/gamedb"
	"voice-server/internal/hub"
	"voice-server/internal/session"
	"voice-server/internal/voiceclient"
)

// Config — параметры HTTP-слоя (delivery).
type Config struct {
	StaticDir string
	Store     *gamedb.Store // локальная БД партий; nil — без сохранения
}

// NewRouter собирает chi: API, WebSocket, статика.
func NewRouter(cfg Config, sm *session.Manager, vc *voiceclient.Client, h *hub.Hub) chi.Router {
	r := chi.NewRouter()
	r.Use(corsMiddleware)

	r.Get("/ws", h.ServeWS)

	r.Route("/api", func(r chi.Router) {
		r.Post("/ingest", handleIngest(sm))
		r.Post("/session/start", handleSessionStart(sm))
		r.Post("/session/stop", handleSessionStop(sm))
		r.Get("/session/status", handleSessionStatus(sm, cfg.Store))
		r.Post("/upload", handleUpload)
		r.Get("/speakers", handleSpeakersList(vc))
		r.Post("/speakers/{id}/label", handleSpeakerLabel(vc, h))
	})

	if cfg.Store != nil {
		r.Route("/api/games", func(r chi.Router) {
			r.Get("/sessions", handleListGameSessions(cfg.Store))
			r.Get("/sessions/{id}", handleGetGameSession(cfg.Store))
			r.Get("/sessions/{id}/segments", handleListGameSegments(cfg.Store))
		})
	}

	r.NotFound(staticHandler(cfg.StaticDir))
	return r
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PATCH, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func staticHandler(staticDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api") || r.URL.Path == "/ws" {
			http.NotFound(w, r)
			return
		}
		clean := filepath.Clean(r.URL.Path)
		if clean == "/" || clean == "." {
			http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
			return
		}
		full := filepath.Join(staticDir, strings.TrimPrefix(clean, "/"))
		base, err := filepath.Abs(staticDir)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		abs, err := filepath.Abs(full)
		if err != nil || !strings.HasPrefix(abs, base+string(os.PathSeparator)) && abs != base {
			http.NotFound(w, r)
			return
		}
		if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
			http.ServeFile(w, r, abs)
			return
		}
		http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
	}
}

func handleIngest(sm *session.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 30); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fh, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file field", http.StatusBadRequest)
			return
		}
		defer fh.Close()

		origName := ""
		if header != nil && header.Filename != "" {
			origName = filepath.Base(header.Filename)
		}

		speakers := 0
		if v := r.FormValue("speakers"); v != "" {
			speakers, _ = strconv.Atoi(v)
		}

		tmp, err := os.CreateTemp(os.TempDir(), "ingest-*")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmpPath := tmp.Name()
		if _, err := io.Copy(tmp, fh); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmp.Close()

		sm.StartIngest(tmpPath, speakers, true, origName)

		w.Header().Set("Content-Type", "application/json")
		out := map[string]string{"status": "started"}
		if gid := sm.ActiveGameID(); gid != "" {
			out["game_session_id"] = gid
		}
		if origName != "" {
			out["source_filename"] = origName
		}
		_ = json.NewEncoder(w).Encode(out)
	}
}

type sessionStartBody struct {
	Mode           string `json:"mode"`
	Speakers       int    `json:"speakers"`
	FilePath       string `json:"file_path"`
	SourceFilename string `json:"source_filename,omitempty"` // оригинальное имя файла (тест по файлу)
}

func handleSessionStart(sm *session.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body sessionStartBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch body.Mode {
		case "file":
			if body.FilePath == "" {
				http.Error(w, "file_path required for file mode", http.StatusBadRequest)
				return
			}
			sm.StartFile(body.FilePath, body.Speakers, body.SourceFilename)
		case "record":
			sm.StartRecord(body.Speakers)
		default:
			http.Error(w, "mode must be file or record", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		out := map[string]string{"status": "started"}
		if gid := sm.ActiveGameID(); gid != "" {
			out["game_session_id"] = gid
		}
		_ = json.NewEncoder(w).Encode(out)
	}
}

func handleSessionStop(sm *session.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sm.Stop()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
	}
}

func handleSessionStatus(sm *session.Manager, store *gamedb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, mode := sm.Status()
		out := map[string]interface{}{
			"status": st,
			"mode":   nil,
		}
		if mode != "" {
			out["mode"] = mode
		}
		if gid := sm.ActiveGameID(); gid != "" {
			out["game_session_id"] = gid
			if store != nil {
				if row, err := store.GetSession(gid); err == nil && row != nil && row.SourceFilename != "" {
					out["source_filename"] = row.SourceFilename
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func handleListGameSessions(store *gamedb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		rows, err := store.ListSessions(limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rows)
	}
}

func handleGetGameSession(store *gamedb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		row, err := store.GetSession(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if row == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(row)
	}
}

func handleListGameSegments(store *gamedb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		rows, err := store.ListSegments(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rows)
	}
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(1 << 30); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fh, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer fh.Close()

	origName := ""
	if header != nil && header.Filename != "" {
		origName = filepath.Base(header.Filename)
	}

	tmp, err := os.CreateTemp(os.TempDir(), "upload-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	path := tmp.Name()
	if _, err := io.Copy(tmp, fh); err != nil {
		tmp.Close()
		os.Remove(path)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmp.Close()

	w.Header().Set("Content-Type", "application/json")
	out := map[string]string{"file_path": path}
	if origName != "" {
		out["filename"] = origName
	}
	_ = json.NewEncoder(w).Encode(out)
}

func handleSpeakersList(vc *voiceclient.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, code, err := vc.ListVoices()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write(body)
	}
}

type labelBody struct {
	Name string `json:"name"`
}

func handleSpeakerLabel(vc *voiceclient.Client, h *hub.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		var body labelBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			http.Error(w, "missing name", http.StatusBadRequest)
			return
		}
		if err := vc.LabelVoice(id, body.Name); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		h.BroadcastJSON(map[string]interface{}{
			"type":       "label",
			"speaker_id": id,
			"name":       body.Name,
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}
