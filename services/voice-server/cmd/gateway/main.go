package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"voice-server/internal/gamedb"
	"voice-server/internal/hub"
	"voice-server/internal/httpserver"
	"voice-server/internal/session"
	"voice-server/internal/voiceclient"
)

// findVoiceServerRoot ищет каталог модуля voice-server, поднимаясь от текущей рабочей директории
// (чтобы ./gateway из cmd/gateway всё равно находил ../web/static).
func findVoiceServerRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for i := 0; i < 16; i++ {
		if st := filepath.Join(dir, "web", "static"); dirExists(st) {
			return dir
		}
		mod := filepath.Join(dir, "go.mod")
		if b, err := os.ReadFile(mod); err == nil && strings.Contains(string(b), "module voice-server") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	wd, _ := os.Getwd()
	return wd
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func defaultStaticDir() string {
	root := findVoiceServerRoot()
	if st := filepath.Join(root, "web", "static"); dirExists(st) {
		abs, err := filepath.Abs(st)
		if err == nil {
			return abs
		}
	}
	for _, p := range []string{"web/static", "static"} {
		if dirExists(p) {
			abs, err := filepath.Abs(p)
			if err == nil {
				return abs
			}
		}
	}
	return "web/static"
}

func defaultGameDBPath() string {
	root := findVoiceServerRoot()
	p := filepath.Join(root, "data", "game_log.sqlite")
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func main() {
	voiceURL := flag.String("voice-url", "", "URL Python voice-worker (required), e.g. https://xxx.ngrok-free.app")
	port := flag.Int("port", 8080, "HTTP listen port")
	apiKey := flag.String("api-key", "barchik", "X-API-Key for voice-worker")
	staticDir := flag.String("static", defaultStaticDir(), "directory with static web assets")
	gameDB := flag.String("game-db", defaultGameDBPath(), "SQLite file for saved game transcripts (empty = disable)")
	flag.Parse()

	if strings.TrimSpace(*voiceURL) == "" {
		log.Fatal("-voice-url is required")
	}

	var store *gamedb.Store
	if strings.TrimSpace(*gameDB) != "" {
		if err := os.MkdirAll(filepath.Dir(*gameDB), 0755); err != nil {
			log.Fatalf("game-db dir: %v", err)
		}
		s, err := gamedb.Open(*gameDB)
		if err != nil {
			log.Fatalf("game-db: %v", err)
		}
		defer s.Close()
		store = s
		log.Printf("game log database: %s", filepath.Clean(*gameDB))
	}

	vc := voiceclient.New(*voiceURL, *apiKey)
	h := hub.New()
	sm := session.NewManager(vc, h, store)

	r := httpserver.NewRouter(httpserver.Config{StaticDir: *staticDir, Store: store}, sm, vc, h)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("gateway listening on http://localhost%s (voice-worker: %s)", addr, *voiceURL)
	log.Printf("static files: %s", filepath.Clean(*staticDir))
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal(err)
	}
}
