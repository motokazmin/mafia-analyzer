package analyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"mafia-analyzer/config"
	"mafia-analyzer/internal/ollama"
)

type Analyzer struct {
	cfg     *config.Config
	ollama  *ollama.Client
	mu      sync.Mutex // защищает buffer, gameMap, round
	buffer  []string
	gameMap *ollama.GameMap
	round   int
}

func New(cfg *config.Config, ollamaClient *ollama.Client) *Analyzer {
	return &Analyzer{
		cfg:     cfg,
		ollama:  ollamaClient,
		gameMap: &ollama.GameMap{},
	}
}

func (a *Analyzer) AddLine(ctx context.Context, line string) (*ollama.GameMap, error) {
	a.mu.Lock()
	a.buffer = append(a.buffer, line)
	full := len(a.buffer) >= a.cfg.Analysis.BufferLines
	a.mu.Unlock()

	if full {
		return a.flush(ctx)
	}
	return nil, nil
}

func (a *Analyzer) Flush(ctx context.Context) (*ollama.GameMap, error) {
	a.mu.Lock()
	empty := len(a.buffer) == 0
	a.mu.Unlock()

	if empty {
		return nil, nil
	}
	return a.flush(ctx)
}

func (a *Analyzer) flush(ctx context.Context) (*ollama.GameMap, error) {
	a.mu.Lock()
	chunk := strings.Join(a.buffer, "\n")
	a.buffer = nil
	a.round++
	a.mu.Unlock()

	a.mu.Lock()
	userPrompt := a.buildPrompt(chunk)
	a.mu.Unlock()

	gameMap, err := a.ollama.Analyze(ctx, a.cfg.Prompts.System, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("ollama analyze: %w", err)
	}

	if !gameMap.IsEmpty() {
		a.mu.Lock()
		a.gameMap = gameMap
		a.mu.Unlock()
	}

	return gameMap, nil
}

func (a *Analyzer) buildPrompt(chunk string) string {
	// вызывается под mu.Lock() из flush
	var gameMapStr string
	if a.gameMap.IsEmpty() {
		gameMapStr = "Карта пуста — это начало игры."
	} else {
		gameMapStr = a.gameMap.ToJSON()
	}
	return fmt.Sprintf(a.cfg.Prompts.UserTemplate, gameMapStr, chunk)
}

func (a *Analyzer) CurrentGameMap() *ollama.GameMap {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gameMap
}

func (a *Analyzer) IsBufferFull(nextLine string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = nextLine
	return len(a.buffer)+1 >= a.cfg.Analysis.BufferLines
}

func (a *Analyzer) Stats() string {
	a.mu.Lock()
	bufLen := len(a.buffer)
	round := a.round
	phase := a.gameMap.CurrentPhase
	players := len(a.gameMap.PlayerProfiles)
	a.mu.Unlock()

	if phase == "" {
		phase = "неизвестно"
	}
	return fmt.Sprintf("buffer=%d/%d round=%d players=%d phase=%s",
		bufLen, a.cfg.Analysis.BufferLines, round, players, phase)
}

// FormatResult выводит карту игры в stdout
func FormatResult(gameMap *ollama.GameMap, round int, elapsed time.Duration) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("\n%s АНАЛИЗ #%d (%.1fs) %s\n",
		strings.Repeat("─", 15), round, elapsed.Seconds(), strings.Repeat("─", 15)))

	// фаза и день
	if gameMap.CurrentPhase != "" {
		phaseIcon := phaseIcon(gameMap.CurrentPhase)
		dayStr := ""
		if gameMap.DayNumber > 0 {
			dayStr = fmt.Sprintf(" | День %d", gameMap.DayNumber)
		}
		sb.WriteString(fmt.Sprintf("%s Фаза: %s%s\n", phaseIcon, gameMap.CurrentPhase, dayStr))
	}

	if gameMap.GameFlow != "" {
		sb.WriteString(fmt.Sprintf("📋 Ход игры: %s\n", gameMap.GameFlow))
	}

	if len(gameMap.EliminatedPlayers) > 0 {
		sb.WriteString(fmt.Sprintf("💀 Выбыли: %s\n", strings.Join(gameMap.EliminatedPlayers, ", ")))
	}

	if len(gameMap.PlayerProfiles) > 0 {
		sb.WriteString("\n🎭 Карта игроков:\n")
		for _, p := range gameMap.PlayerProfiles {
			mafiaTag := "🟢"
			if p.IsLikelyMafia {
				mafiaTag = "🔴"
			}
			aggrBar := aggressionBar(p.AggressionLevel)
			sb.WriteString(fmt.Sprintf("  %s %-12s агрессия:%s %d/10\n",
				mafiaTag, p.IdentifiedID, aggrBar, p.AggressionLevel))
			if p.Suspicions != "" {
				sb.WriteString(fmt.Sprintf("     подозревает: %s\n", p.Suspicions))
			}
			if p.Reasoning != "" {
				sb.WriteString(fmt.Sprintf("     вывод: %s\n", p.Reasoning))
			}
		}
	}

	if gameMap.IsEmpty() {
		sb.WriteString("⚠️  Сырой ответ модели:\n")
		sb.WriteString(gameMap.Raw)
		sb.WriteString("\n")
	}

	sb.WriteString(strings.Repeat("─", 47) + "\n")
	return sb.String()
}

// FormatFinalMap выводит финальную карту игры
func FormatFinalMap(gameMap *ollama.GameMap) string {
	if gameMap.IsEmpty() {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n%s ФИНАЛЬНАЯ КАРТА ИГРЫ %s\n",
		strings.Repeat("═", 15), strings.Repeat("═", 15)))

	if gameMap.CurrentPhase != "" {
		sb.WriteString(fmt.Sprintf("%s Последняя фаза: %s | День %d\n\n",
			phaseIcon(gameMap.CurrentPhase), gameMap.CurrentPhase, gameMap.DayNumber))
	}

	if gameMap.GameFlow != "" {
		sb.WriteString(fmt.Sprintf("📋 %s\n\n", gameMap.GameFlow))
	}

	if len(gameMap.EliminatedPlayers) > 0 {
		sb.WriteString(fmt.Sprintf("💀 Выбыли за игру: %s\n\n", strings.Join(gameMap.EliminatedPlayers, ", ")))
	}

	var mafia, town []ollama.PlayerProfile
	for _, p := range gameMap.PlayerProfiles {
		if p.IsLikelyMafia {
			mafia = append(mafia, p)
		} else {
			town = append(town, p)
		}
	}

	if len(mafia) > 0 {
		sb.WriteString("🔴 Вероятная мафия:\n")
		for _, p := range mafia {
			sb.WriteString(fmt.Sprintf("   %-12s — %s\n", p.IdentifiedID, p.Reasoning))
		}
		sb.WriteString("\n")
	}

	if len(town) > 0 {
		sb.WriteString("🟢 Мирные жители:\n")
		for _, p := range town {
			sb.WriteString(fmt.Sprintf("   %-12s — %s\n", p.IdentifiedID, p.Reasoning))
		}
	}

	sb.WriteString("\n📄 JSON:\n")
	b, _ := json.MarshalIndent(gameMap, "", "  ")
	sb.WriteString(string(b))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("═", 51) + "\n")

	return sb.String()
}

func phaseIcon(phase string) string {
	switch strings.ToLower(phase) {
	case "ночь", "night":
		return "🌙"
	case "день", "day":
		return "☀️"
	default:
		return "🔄"
	}
}

func aggressionBar(level int) string {
	if level > 10 {
		level = 10
	}
	return "[" + strings.Repeat("█", level) + strings.Repeat("░", 10-level) + "]"
}
