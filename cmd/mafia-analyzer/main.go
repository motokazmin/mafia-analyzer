package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mafia-analyzer/config"
	"mafia-analyzer/internal/analyzer"
	"mafia-analyzer/internal/ollama"
	"mafia-analyzer/internal/whisper"
)

const (
	colorReset  = "\033[0m"
	colorCyan   = "\033[36m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorGray   = "\033[90m"
)

func main() {
	configPath := flag.String("config", "config/config.json", "path to config file")
	audioFile := flag.String("audio", "", "audio file to transcribe (required)")
	flag.Parse()

	if *audioFile == "" {
		fmt.Fprintf(os.Stderr, "Usage: mafia-analyzer -audio <file> [-config <config.json>]\n")
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatalf("load config: %v", err)
	}

	logf(colorCyan, "CONFIG", "model=%s | buffer=%d lines | context=%d lines",
		cfg.Ollama.Model, cfg.Analysis.BufferLines, cfg.Analysis.ContextWindow)
	logf(colorCyan, "CONFIG", "whisper=%s | lang=%s", cfg.Whisper.Binary, cfg.Whisper.Language)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	ollamaClient := ollama.NewClient(&cfg.Ollama)
	whisperRunner := whisper.NewRunner(&cfg.Whisper)
	an := analyzer.New(cfg, ollamaClient)

	logf(colorGray, "INIT", "checking ollama at %s ...", cfg.Ollama.BaseURL)
	if err := checkOllama(&cfg.Ollama); err != nil {
		logf(colorYellow, "WARN", "ollama check failed: %v", err)
	} else {
		location := "локальный"
		if cfg.Ollama.APIKey != "" || strings.HasPrefix(cfg.Ollama.BaseURL, "https://") {
			location = "облачный"
		}
		logf(colorGreen, "INIT", "ollama OK (%s) — model: %s", location, cfg.Ollama.Model)
	}

	logf(colorGreen, "START", "launching whisper on: %s", *audioFile)
	lines, errc := whisperRunner.TranscribeFile(ctx, *audioFile)

	totalLines := 0
	totalAnalyses := 0
	startTime := time.Now()

loop:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				break loop
			}
			totalLines++
			logf(colorGray, "TRANSCRIPT", "[%d] %s", totalLines, line.Text)

			// логируем отправку в ollama когда буфер заполнен
			if an.IsBufferFull(line.Text) {
				logf(colorYellow, "OLLAMA", "→ sending chunk #%d to ollama | %s",
					totalAnalyses+1, an.Stats())
			}

			t0 := time.Now()
			gameMap, err := an.AddLine(ctx, line.Text)
			if err != nil {
				logf(colorRed, "ERROR", "analysis: %v", err)
				continue
			}
			if gameMap != nil {
				totalAnalyses++
				elapsed := time.Since(t0)
				logf(colorGreen, "OLLAMA", "← got response #%d in %.1fs", totalAnalyses, elapsed.Seconds())
				fmt.Print(analyzer.FormatResult(gameMap, totalAnalyses, elapsed))
			}

		case err, ok := <-errc:
			if !ok {
				break loop
			}
			if err != nil {
				logf(colorRed, "ERROR", "whisper: %v", err)
				cancel()
				break loop
			}

		case <-ctx.Done():
			logf(colorYellow, "SIGNAL", "shutting down...")
			break loop
		}
	}

	// финальный flush остатка буфера
	if ctx.Err() == nil {
		logf(colorCyan, "FLUSH", "finalizing... %s", an.Stats())
		logf(colorYellow, "OLLAMA", "→ sending final chunk to ollama")
		t0 := time.Now()
		gameMap, err := an.Flush(ctx)
		if err != nil {
			logf(colorRed, "ERROR", "final flush: %v", err)
		} else if gameMap != nil {
			totalAnalyses++
			elapsed := time.Since(t0)
			logf(colorGreen, "OLLAMA", "← got final response in %.1fs", elapsed.Seconds())
			fmt.Print(analyzer.FormatResult(gameMap, totalAnalyses, elapsed))
		}

		// финальная карта игры
		fmt.Print(analyzer.FormatFinalMap(an.CurrentGameMap()))
	}

	logf(colorGreen, "DONE", "lines=%d analyses=%d time=%s",
		totalLines, totalAnalyses, time.Since(startTime).Round(time.Second))
}

func logf(color, tag, format string, args ...any) {
	ts := time.Now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("%s%s [%-10s] %s%s\n", color, ts, tag, msg, colorReset)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}

func checkOllama(cfg *config.OllamaConfig) error {
	client := &http.Client{Timeout: 5 * time.Second}

	// Нормализуем URL (убираем лишние слэши)
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	apiURL := baseURL + "/api/tags"

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return err
	}

	// Добавляем API ключ, если указан
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	// Автоматически добавляем заголовок для ngrok-free.dev
	if strings.Contains(baseURL, "ngrok-free.dev") {
		req.Header.Set("ngrok-skip-browser-warning", "true")
	}

	// Добавляем кастомные заголовки
	for key, value := range cfg.Headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
