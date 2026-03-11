package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"mafia-analyzer/config"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type PlayerProfile struct {
	IdentifiedID    string `json:"identified_id"`
	Suspicions      string `json:"suspicions"`
	AggressionLevel int    `json:"aggression_level"`
	IsLikelyMafia   bool   `json:"is_likely_mafia"`
	Reasoning       string `json:"reasoning"`
}

// GameMap — накопительная карта игры, обновляется с каждым чанком
type GameMap struct {
	CurrentPhase      string          `json:"current_phase"`      // "день", "ночь", "переход"
	DayNumber         int             `json:"day_number"`         // номер текущего дня
	GameFlow          string          `json:"game_flow"`          // хронология событий
	EliminatedPlayers []string        `json:"eliminated_players"` // выбывшие игроки
	PlayerProfiles    []PlayerProfile `json:"player_profiles"`
	Raw               string          `json:"-"`
}

func (g *GameMap) IsEmpty() bool {
	return g.GameFlow == "" && len(g.PlayerProfiles) == 0
}

func (g *GameMap) ToJSON() string {
	if g.IsEmpty() {
		return "{}"
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

type Client struct {
	cfg        *config.OllamaConfig
	httpClient *http.Client
}

func NewClient(cfg *config.OllamaConfig) *Client {
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	Options  struct {
		Temperature float64 `json:"temperature"`
	} `json:"options"`
}

type streamChunk struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

func (c *Client) Analyze(ctx context.Context, systemPrompt, userPrompt string) (*GameMap, error) {
	req := chatRequest{
		Model: c.cfg.Model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Stream: c.cfg.Stream,
	}
	req.Options.Temperature = c.cfg.Temperature

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.cfg.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, string(b))
	}

	var fullText strings.Builder

	if c.cfg.Stream {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			var chunk streamChunk
			if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
				continue
			}
			fullText.WriteString(chunk.Message.Content)
			if chunk.Done {
				break
			}
		}
	} else {
		var single struct {
			Message Message `json:"message"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&single); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		fullText.WriteString(single.Message.Content)
	}

	raw := fullText.String()
	gameMap := &GameMap{Raw: raw}

	jsonStr := extractJSON(raw)
	if jsonStr != "" {
		if err := json.Unmarshal([]byte(jsonStr), gameMap); err != nil {
			return gameMap, nil
		}
	}

	return gameMap, nil
}

func extractJSON(s string) string {
	if idx := strings.Index(s, "```json"); idx != -1 {
		s = s[idx+7:]
		if end := strings.Index(s, "```"); end != -1 {
			s = s[:end]
		}
	} else if idx := strings.Index(s, "```"); idx != -1 {
		s = s[idx+3:]
		if end := strings.Index(s, "```"); end != -1 {
			s = s[:end]
		}
	}
	s = strings.TrimSpace(s)

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || end <= start {
		return ""
	}
	return s[start : end+1]
}
