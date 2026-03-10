package whisper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/mafia-analyzer/config"
)

// Line represents a single transcribed line from whisper
type Line struct {
	Text string
	Raw  string // original whisper output before cleanup
}

// Runner manages the whisper subprocess
type Runner struct {
	cfg *config.WhisperConfig
}

func NewRunner(cfg *config.WhisperConfig) *Runner {
	return &Runner{cfg: cfg}
}

// TranscribeFile runs whisper on a single audio file and streams lines via channel.
// The channel is closed when whisper exits or ctx is cancelled.
func (r *Runner) TranscribeFile(ctx context.Context, audioFile string) (<-chan Line, <-chan error) {
	lines := make(chan Line, 32)
	errc := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errc)

		args := r.buildArgs(audioFile)
		cmd := exec.CommandContext(ctx, r.cfg.Binary, args...)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errc <- fmt.Errorf("stdout pipe: %w", err)
			return
		}

		// whisper writes progress/info to stderr — capture it separately
		stderr, err := cmd.StderrPipe()
		if err != nil {
			errc <- fmt.Errorf("stderr pipe: %w", err)
			return
		}

		if err := cmd.Start(); err != nil {
			errc <- fmt.Errorf("start whisper: %w", err)
			return
		}

		// drain stderr in background (prevents blocking)
		go drainStderr(stderr)

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			raw := scanner.Text()
			text := cleanLine(raw)
			if text == "" {
				continue
			}
			select {
			case lines <- Line{Text: text, Raw: raw}:
			case <-ctx.Done():
				return
			}
		}

		if err := cmd.Wait(); err != nil {
			// context cancellation causes exit error — treat as normal
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
		"--no-timestamps",        // clean output without [00:00 --> 00:05]
		"--output-txt",           // also write .txt file alongside audio
		"--print-special", "0",   // suppress [BLANK_AUDIO] etc.
	}
	args = append(args, r.cfg.ExtraArgs...)
	return args
}

// cleanLine strips whisper artifacts from output line
func cleanLine(s string) string {
	s = strings.TrimSpace(s)

	// skip empty, timing lines, and whisper meta-output
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return "" // [BLANK_AUDIO], [MUSIC], etc.
	}
	if strings.Contains(s, "-->") {
		return "" // timestamp lines if they slip through
	}
	// whisper sometimes prefixes lines with spaces or "  "
	s = strings.TrimLeft(s, " \t")

	return s
}

func drainStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		_, err := r.Read(buf)
		if err != nil {
			return
		}
	}
}
