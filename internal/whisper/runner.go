package whisper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"mafia-analyzer/config"
)

type Line struct {
	Text string
	Raw  string
}

type Runner struct {
	cfg *config.WhisperConfig
}

func NewRunner(cfg *config.WhisperConfig) *Runner {
	return &Runner{cfg: cfg}
}

func (r *Runner) TranscribeFile(ctx context.Context, audioFile string) (<-chan Line, <-chan error) {
	lines := make(chan Line, 32)
	errc := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errc)

		args := r.buildArgs(audioFile)
		cmd := exec.CommandContext(ctx, r.cfg.Binary, args...)
		cmd.Stderr = io.Discard

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errc <- fmt.Errorf("stdout pipe: %w", err)
			return
		}

		if err := cmd.Start(); err != nil {
			errc <- fmt.Errorf("start whisper: %w", err)
			return
		}

		const maxRepeat = 3 // максимум одинаковых строк подряд

		var lastText string
		repeatCount := 0

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			raw := scanner.Text()
			text := cleanLine(raw)
			if text == "" {
				continue
			}

			// фильтруем зацикливание whisper
			if text == lastText {
				repeatCount++
				if repeatCount >= maxRepeat {
					// пропускаем но не сбрасываем счётчик —
					// продолжаем игнорировать пока не придёт новый текст
					continue
				}
			} else {
				lastText = text
				repeatCount = 1
			}

			select {
			case lines <- Line{Text: text, Raw: raw}:
			case <-ctx.Done():
				return
			}
		}

		if err := cmd.Wait(); err != nil {
			if ctx.Err() == nil {
				errc <- fmt.Errorf("whisper exit: %w", err)
			}
		}
	}()

	return lines, errc
}

func (r *Runner) buildArgs(audioFile string) []string {
	args := []string{
		"-m", r.cfg.Model,
		"-f", audioFile,
		"--language", r.cfg.Language,
	}
	args = append(args, r.cfg.ExtraArgs...)
	return args
}

func cleanLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// вырезаем тайминги [00:00:00.000 --> 00:00:05.000]
	if strings.HasPrefix(s, "[") {
		idx := strings.Index(s, "]")
		if idx != -1 {
			s = strings.TrimSpace(s[idx+1:])
		}
	}

	// фильтруем служебные теги [BLANK_AUDIO] и т.д.
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return ""
	}

	return s
}
