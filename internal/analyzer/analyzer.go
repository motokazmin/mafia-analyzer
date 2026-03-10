package analyzer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mafia-analyzer/config"
	"github.com/mafia-analyzer/internal/ollama"
)

// Analyzer accumulates transcript lines and periodically sends them to Ollama
type Analyzer struct {
	cfg    *config.Config
	ollama *ollama.Client

	buffer  []string // current pending lines (not yet sent)
	history []string // full transcript history (for context window)
}

func New(cfg *config.Config, ollamaClient *ollama.Client) *Analyzer {
	return &Analyzer{
		cfg:    cfg,
		ollama: ollamaClient,
	}
}

// AddLine adds a transcribed line. When buffer is full, triggers analysis.
// Returns AnalysisResult if analysis was triggered, nil otherwise.
func (a *Analyzer) AddLine(ctx context.Context, line string) (*ollama.AnalysisResult, error) {
	a.buffer = append(a.buffer, line)
	a.history = append(a.history, line)

	if len(a.buffer) >= a.cfg.Analysis.BufferLines {
		return a.flush(ctx)
	}
	return nil, nil
}

// Flush forces analysis even if buffer isn't full (call at end of file)
func (a *Analyzer) Flush(ctx context.Context) (*ollama.AnalysisResult, error) {
	if len(a.buffer) == 0 {
		return nil, nil
	}
	return a.flush(ctx)
}

func (a *Analyzer) flush(ctx context.Context) (*ollama.AnalysisResult, error) {
	fragment := strings.Join(a.buffer, "\n")
	a.buffer = nil

	userPrompt := fmt.Sprintf(a.cfg.Prompts.UserTemplate, fragment)

	// append rolling context summary if we have history
	if len(a.history) > a.cfg.Analysis.BufferLines {
		contextLines := a.contextWindow()
		userPrompt = fmt.Sprintf(
			"Контекст игры (последние реплики):\n---\n%s\n---\n\n%s",
			strings.Join(contextLines, "\n"),
			userPrompt,
		)
	}

	result, err := a.ollama.Analyze(ctx, a.cfg.Prompts.System, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("ollama analyze: %w", err)
	}

	return result, nil
}

// contextWindow returns the last N lines of history (excluding current buffer)
func (a *Analyzer) contextWindow() []string {
	n := a.cfg.Analysis.ContextWindow
	if len(a.history) <= n {
		return a.history
	}
	return a.history[len(a.history)-n:]
}

// Stats returns current buffer state for logging
func (a *Analyzer) Stats() string {
	return fmt.Sprintf("buffer=%d/%d history=%d",
		len(a.buffer), a.cfg.Analysis.BufferLines, len(a.history))
}

// FormatResult pretty-prints the analysis result for stdout
func FormatResult(result *ollama.AnalysisResult, elapsed time.Duration) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("\n%s АНАЛИЗ (%.1fs) %s\n",
		strings.Repeat("─", 20), elapsed.Seconds(), strings.Repeat("─", 20)))

	if len(result.Suspicion) > 0 {
		sb.WriteString("🎭 Подозрения:\n")
		for _, s := range result.Suspicion {
			bar := suspicionBar(s.Score)
			sb.WriteString(fmt.Sprintf("  %-12s %s %d/10  %s\n",
				s.Player, bar, s.Score, s.Reason))
		}
	}

	if len(result.KeyPhrases) > 0 {
		sb.WriteString(fmt.Sprintf("🔑 Ключевые фразы: %s\n",
			strings.Join(result.KeyPhrases, " | ")))
	}

	if result.Summary != "" {
		sb.WriteString(fmt.Sprintf("📝 Вывод: %s\n", result.Summary))
	}

	if len(result.Suspicion) == 0 && result.Summary == "" {
		// JSON parse failed — show raw
		sb.WriteString("⚠️  Сырой ответ модели:\n")
		sb.WriteString(result.Raw)
		sb.WriteString("\n")
	}

	sb.WriteString(strings.Repeat("─", 52) + "\n")
	return sb.String()
}

func suspicionBar(score int) string {
	filled := score
	if filled > 10 {
		filled = 10
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", 10-filled) + "]"
}
