package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	Whisper  WhisperConfig  `json:"whisper"`
	Ollama   OllamaConfig   `json:"ollama"`
	Analysis AnalysisConfig `json:"analysis"`
	Prompts  PromptsConfig  `json:"prompts"`
}

type WhisperConfig struct {
	Binary    string   `json:"binary"`
	Model     string   `json:"model"`
	Language  string   `json:"language"`
	ExtraArgs []string `json:"extra_args"`
}

type OllamaConfig struct {
	BaseURL     string  `json:"base_url"`
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	Stream      bool    `json:"stream"`
}

type AnalysisConfig struct {
	BufferLines   int `json:"buffer_lines"`
	ContextWindow int `json:"context_window"`
}

type PromptsConfig struct {
	System       string `json:"system"`
	UserTemplate string `json:"user_template"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return &cfg, nil
}
