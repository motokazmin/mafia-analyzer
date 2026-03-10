package analyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"mafia-analyzer/config"
	"mafia-analyzer/internal/ollama"
)

// Analyzer накапливает реплики и периодически обновляет карту игры через Ollama
type Analyzer struct {
	cfg    *config.Config
	ollama *ollama.Client

	buffer  []string        // текущие реплики ещё не отправленные
	gameMap *ollama.GameMap // накопительная карта игры
	round   int             // номер текущего анализа
}

func New(cfg *config.Config, ollamaClient *ollama.Client) *Analyzer {
	return &Analyzer{
		cfg:     cfg,
		ollama:  ollamaClient,
		gameMap: &ollama.GameMap{},
	}
}

// AddLine добавляет транскрибированную реплику.
// Когда буфер заполнен — запускает анализ и обновляет карту игры.
func (a *Analyzer) AddLine(ctx context.Context, line string) (*ollama.GameMap, error) {
	a.buffer = append(a.buffer, line)

	if len(a.buffer) >= a.cfg.Analysis.BufferLines {
		return a.flush(ctx)
	}
	return nil, nil
}

// Flush принудительно запускает анализ остатка буфера (вызывается в конце файла)
func (a *Analyzer) Flush(ctx context.Context) (*ollama.GameMap, error) {
	if len(a.buffer) == 0 {
		return nil, nil
	}
	return a.flush(ctx)
}

func (a *Analyzer) flush(ctx context.Context) (*ollama.GameMap, error) {
	chunk := strings.Join(a.buffer, "\n")
	a.buffer = nil
	a.round++

	userPrompt := a.buildPrompt(chunk)

	gameMap, err := a.ollama.Analyze(ctx, a.cfg.Prompts.System, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("ollama analyze: %w", err)
	}

	// обновляем накопленную карту только если получили валидный ответ
	if !gameMap.IsEmpty() {
		a.gameMap = gameMap
	}

	return gameMap, nil
}

// buildPrompt формирует промпт с текущей картой игры как контекстом
func (a *Analyzer) buildPrompt(chunk string) string {
	var sb strings.Builder

	if a.gameMap.IsEmpty() {
		// первый чанк — карты ещё нет
		sb.WriteString("Это начало игры. Карта игры пока пуста.\n\n")
	} else {
		// передаём текущую карту как контекст
		sb.WriteString("ТЕКУЩАЯ КАРТА ИГРЫ (накоплено к этому моменту):\n")
		sb.WriteString(a.gameMap.ToJSON())
		sb.WriteString("\n\n")
	}

	sb.WriteString(fmt.Sprintf("НОВЫЙ ФРАГМЕНТ (раунд анализа #%d):\n", a.round))
	sb.WriteString("---\n")
	sb.WriteString(chunk)
	sb.WriteString("\n---\n\n")
	sb.WriteString(a.cfg.Prompts.UserTemplate)

	return sb.String()
}

// CurrentGameMap возвращает текущую накопленную карту игры
func (a *Analyzer) CurrentGameMap() *ollama.GameMap {
	return a.gameMap
}

// Stats возвращает текущее состояние буфера для логирования
func (a *Analyzer) Stats() string {
	return fmt.Sprintf("buffer=%d/%d round=%d players=%d",
		len(a.buffer), a.cfg.Analysis.BufferLines, a.round, len(a.gameMap.PlayerProfiles))
}

// FormatResult красиво выводит карту игры в stdout
func FormatResult(gameMap *ollama.GameMap, round int, elapsed time.Duration) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("\n%s АНАЛИЗ #%d (%.1fs) %s\n",
		strings.Repeat("─", 15), round, elapsed.Seconds(), strings.Repeat("─", 15)))

	if gameMap.GameFlow != "" {
		sb.WriteString(fmt.Sprintf("📋 Ход игры: %s\n", gameMap.GameFlow))
	}

	if len(gameMap.PlayerProfiles) > 0 {
		sb.WriteString("\n🎭 Карта игроков:\n")
		for _, p := range gameMap.PlayerProfiles {
			mafiaTag := "  "
			if p.IsLikelyMafia {
				mafiaTag = "🔴"
			} else {
				mafiaTag = "🟢"
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

	// если JSON не распарсился — показываем сырой ответ
	if gameMap.IsEmpty() {
		sb.WriteString("⚠️  Сырой ответ модели:\n")
		sb.WriteString(gameMap.Raw)
		sb.WriteString("\n")
	}

	sb.WriteString(strings.Repeat("─", 47) + "\n")
	return sb.String()
}

// FormatFinalMap выводит финальную карту игры в конце
func FormatFinalMap(gameMap *ollama.GameMap) string {
	if gameMap.IsEmpty() {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n%s ФИНАЛЬНАЯ КАРТА ИГРЫ %s\n",
		strings.Repeat("═", 15), strings.Repeat("═", 15)))

	sb.WriteString(fmt.Sprintf("📋 %s\n\n", gameMap.GameFlow))

	mafia := []ollama.PlayerProfile{}
	town := []ollama.PlayerProfile{}
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
	}

	if len(town) > 0 {
		sb.WriteString("🟢 Мирные жители:\n")
		for _, p := range town {
			sb.WriteString(fmt.Sprintf("   %-12s — %s\n", p.IdentifiedID, p.Reasoning))
		}
	}

	// финальный JSON для дебага / будущего фронтенда
	sb.WriteString("\n📄 JSON:\n")
	b, _ := json.MarshalIndent(gameMap, "", "  ")
	sb.WriteString(string(b))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("═", 51) + "\n")

	return sb.String()
}

func aggressionBar(level int) string {
	if level > 10 {
		level = 10
	}
	return "[" + strings.Repeat("█", level) + strings.Repeat("░", 10-level) + "]"
}

// IsBufferFull возвращает true если добавление этой строки заполнит буфер
// используется для логирования момента отправки в ollama
func (a *Analyzer) IsBufferFull(nextLine string) bool {
	_ = nextLine
	return len(a.buffer)+1 >= a.cfg.Analysis.BufferLines
}
