package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mafia-analyzer/config"
	"github.com/mafia-analyzer/internal/analyzer"
	"github.com/mafia-analyzer/internal/ollama"
	"github.com/mafia-analyzer/internal/whisper"
)

// ANSI color codes for readable stdout
const (
	colorReset  = "\033[0m"
	colorCyan   = "\033[36m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorGray   = "\033[90m"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "path to config file")
	audioFile := flag.String("audio", "", "audio file to transcribe (required)")
	flag.Parse()

	if *audioFile == "" {
		fmt.Fprintf(os.Stderr, "Usage: mafia-analyzer -audio <file.wav> [-config <config.yaml>]\n")
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

	// verify ollama is reachable
	logf(colorGray, "INIT", "checking ollama at %s ...", cfg.Ollama.BaseURL)
	if err := checkOllama(cfg.Ollama.BaseURL); err != nil {
		logf(colorYellow, "WARN", "ollama check failed: %v (will retry on first analysis)", err)
	} else {
		logf(colorGreen, "INIT", "ollama OK — model: %s", cfg.Ollama.Model)
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

			t0 := time.Now()
			result, err := an.AddLine(ctx, line.Text)
			if err != nil {
				logf(colorRed, "ERROR", "analysis: %v", err)
				continue
			}
			if result != nil {
				totalAnalyses++
				elapsed := time.Since(t0)
				fmt.Print(analyzer.FormatResult(result, elapsed))
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

	// flush remaining buffer
	if ctx.Err() == nil {
		logf(colorCyan, "FLUSH", "flushing remaining %s", an.Stats())
		t0 := time.Now()
		result, err := an.Flush(ctx)
		if err != nil {
			logf(colorRed, "ERROR", "final flush: %v", err)
		} else if result != nil {
			totalAnalyses++
			fmt.Print(analyzer.FormatResult(result, time.Since(t0)))
		}
	}

	logf(colorGreen, "DONE", "transcript lines=%d analyses=%d total_time=%s",
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

func checkOllama(baseURL string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
