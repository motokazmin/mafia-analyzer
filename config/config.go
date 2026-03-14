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
	// Mode определяет, как мы работаем с whisper:
	// "local"  — запуск локального бинарника (как сейчас)
	// "remote" — отправка аудио на удалённый HTTP‑сервис (Colab)
	Mode string `json:"mode"` // optional, по умолчанию "local"

	// Локальный режим
	Binary    string   `json:"binary"`
	Model     string   `json:"model"`
	Language  string   `json:"language"`
	ExtraArgs []string `json:"extra_args"`

	// Удалённый режим (Colab / облако)
	RemoteURL string `json:"remote_url"` // базовый URL сервиса транскрипции, напр. https://.../whisper
	APIKey    string `json:"api_key"`    // опциональный ключ для авторизации

	// Режим микрофона (через ffmpeg)
	Microphone MicrophoneConfig `json:"microphone"`
}

type MicrophoneConfig struct {
	Device     string `json:"device"`      // устройство захвата (например, "default" или "pulse")
	SampleRate int    `json:"sample_rate"` // частота дискретизации (16000, 44100 и т.д.)
	Channels   int    `json:"channels"`    // количество каналов (1=моно, 2=стерео)
	ChunkSec   int    `json:"chunk_sec"`   // длительность чанка в секундах (отправка в whisper)
	Format     string `json:"format"`      // формат аудио (wav, flac, opus)
	FFmpegPath string `json:"ffmpeg_path"` // путь к ffmpeg (по умолчанию "ffmpeg")
}

type OllamaConfig struct {
	BaseURL     string            `json:"base_url"`    // URL Ollama сервера (локальный или облачный)
	Model       string            `json:"model"`       // Название модели
	Temperature float64           `json:"temperature"` // Температура генерации
	Stream      bool              `json:"stream"`      // Использовать стриминг
	APIKey      string            `json:"api_key"`     // API ключ для облачных сервисов (опционально)
	Headers     map[string]string `json:"headers"`     // Дополнительные HTTP заголовки (опционально)
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
