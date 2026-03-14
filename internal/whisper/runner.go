package whisper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mafia-analyzer/config"
)

type Line struct {
	Text string
	Raw  string
}

type Runner struct {
	cfg        *config.WhisperConfig
	httpClient *http.Client
}

func NewRunner(cfg *config.WhisperConfig) *Runner {
	return &Runner{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

// applyHeaders выставляет Authorization и ngrok-заголовок на запрос к удалённому whisper.
// Единственное место — нет дублирования между transcribeFileRemote и sendAudioChunk.
func (r *Runner) applyHeaders(req *http.Request) {
	if r.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.APIKey)
	}
	if strings.Contains(r.cfg.RemoteURL, ".ngrok") {
		req.Header.Set("ngrok-skip-browser-warning", "true")
	}
}

func (r *Runner) TranscribeFile(ctx context.Context, audioFile string) (<-chan Line, <-chan error) {
	if r.cfg.Mode == "remote" {
		return r.transcribeFileRemote(ctx, audioFile)
	}
	// по умолчанию — локальный режим
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

// ==== УДАЛЁННЫЙ РЕЖИМ (Colab / HTTP‑сервис) ====

// ожидаемый формат ответа от удалённого сервиса (NDJSON или JSON‑массив):
// либо:
//   {"text": "реплика 1"}
//   {"text": "реплика 2"}
// либо:
//   [{"text":"реплика 1"}, {"text":"реплика 2"}]

type remoteLine struct {
	Text string `json:"text"`
	Raw  string `json:"raw,omitempty"`
}

func (r *Runner) transcribeFileRemote(ctx context.Context, audioFile string) (<-chan Line, <-chan error) {
	lines := make(chan Line, 32)
	errc := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errc)

		if r.cfg.RemoteURL == "" {
			errc <- fmt.Errorf("remote whisper: remote_url is empty in config.whisper")
			return
		}

		// Открываем файл и отправляем как multipart/form-data
		f, err := os.Open(audioFile)
		if err != nil {
			errc <- fmt.Errorf("open audio file: %w", err)
			return
		}
		defer f.Close()

		var body bytes.Buffer
		writer := multipart.NewWriter(&body)

		part, err := writer.CreateFormFile("audio", filepath.Base(audioFile))
		if err != nil {
			errc <- fmt.Errorf("create form file: %w", err)
			return
		}
		if _, err := io.Copy(part, f); err != nil {
			errc <- fmt.Errorf("copy audio: %w", err)
			return
		}

		// Дополнительные поля (язык и модель, если нужно)
		_ = writer.WriteField("language", r.cfg.Language)
		_ = writer.WriteField("model", r.cfg.Model)

		if err := writer.Close(); err != nil {
			errc <- fmt.Errorf("close multipart writer: %w", err)
			return
		}

		req, err := http.NewRequestWithContext(ctx, "POST", r.cfg.RemoteURL, &body)
		if err != nil {
			errc <- fmt.Errorf("create request: %w", err)
			return
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		r.applyHeaders(req)

		resp, err := r.httpClient.Do(req)
		if err != nil {
			errc <- fmt.Errorf("remote whisper request: %w", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			errc <- fmt.Errorf("remote whisper HTTP %d: %s", resp.StatusCode, string(b))
			return
		}

		// Читаем весь ответ в память, затем пробуем два формата: NDJSON и JSON‑массив.
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			errc <- fmt.Errorf("read remote whisper body: %w", err)
			return
		}

		text := string(data)
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}

		// 1) Пробуем как NDJSON: по строкам
		var hadLines bool
		for _, line := range strings.Split(text, "\n") {
			rawTrim := strings.TrimSpace(line)
			if rawTrim == "" {
				continue
			}
			var rl remoteLine
			if err := json.Unmarshal([]byte(rawTrim), &rl); err != nil {
				continue
			}
			txt := strings.TrimSpace(rl.Text)
			if txt == "" {
				continue
			}
			hadLines = true
			select {
			case lines <- Line{Text: txt, Raw: rawTrim}:
			case <-ctx.Done():
				return
			}
		}

		if hadLines {
			return
		}

		// 2) Пробуем как JSON‑массив
		var arr []remoteLine
		if err := json.Unmarshal(data, &arr); err != nil {
			// если не получилось — просто завершаемся без ошибок, сырые данные не нужны
			return
		}
		for _, rl := range arr {
			txt := strings.TrimSpace(rl.Text)
			if txt == "" {
				continue
			}
			select {
			case lines <- Line{Text: txt, Raw: rl.Raw}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return lines, errc
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

// TranscribeMicrophone захватывает аудио с микрофона через ffmpeg и отправляет чанки в облачный whisper
func (r *Runner) TranscribeMicrophone(ctx context.Context) (<-chan Line, <-chan error) {
	lines := make(chan Line, 32)
	errc := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errc)

		if r.cfg.RemoteURL == "" {
			errc <- fmt.Errorf("microphone mode requires remote_url in config.whisper")
			return
		}

		micCfg := r.cfg.Microphone
		if micCfg.FFmpegPath == "" {
			micCfg.FFmpegPath = "ffmpeg"
		}
		if micCfg.SampleRate == 0 {
			micCfg.SampleRate = 16000
		}
		if micCfg.Channels == 0 {
			micCfg.Channels = 1
		}
		if micCfg.ChunkSec == 0 {
			micCfg.ChunkSec = 5 // по умолчанию 5 секунд
		}
		if micCfg.Format == "" {
			micCfg.Format = "wav"
		}
		if micCfg.Device == "" {
			micCfg.Device = "default"
		}

		// Определяем формат входа для ffmpeg по ОС
		inputFormat := "pulse" // по умолчанию Linux
		if runtime.GOOS == "darwin" {
			inputFormat = "avfoundation"
		} else if runtime.GOOS == "windows" {
			inputFormat = "dshow"
		}

		// Строим команду ffmpeg для захвата микрофона
		// Linux:   ffmpeg -f pulse -i default -ar 16000 -ac 1 -f wav -t 5 -
		// macOS:   ffmpeg -f avfoundation -i ":0" -ar 16000 -ac 1 -f wav -t 5 -
		// Windows: ffmpeg -f dshow -i audio="Microphone" -ar 16000 -ac 1 -f wav -t 5 -
		args := []string{
			"-f", inputFormat,
			"-i", micCfg.Device,
			"-ar", fmt.Sprintf("%d", micCfg.SampleRate),
			"-ac", fmt.Sprintf("%d", micCfg.Channels),
			"-f", micCfg.Format,
			"-t", fmt.Sprintf("%d", micCfg.ChunkSec), // длительность чанка
			"-", // вывод в stdout
		}

		// Семафор — не более 2 одновременных запросов к whisper.
		// Без этого горутины накапливаются быстрее чем whisper успевает обрабатывать:
		// каждый процесс whisper на Colab грузит модель в RAM (~1.5 GB), при 9 параллельных — OOM.
		const maxConcurrent = 2
		sem := make(chan struct{}, maxConcurrent)

		chunkNum := 0
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			chunkNum++
			cmd := exec.CommandContext(ctx, micCfg.FFmpegPath, args...)
			cmd.Stderr = io.Discard

			audioData, err := cmd.Output()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				errc <- fmt.Errorf("ffmpeg capture chunk %d: %w", chunkNum, err)
				time.Sleep(1 * time.Second)
				continue
			}

			if len(audioData) == 0 {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// Если семафор полон — whisper не успевает за записью.
			// Ждём слота вместо того чтобы плодить горутины.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}

			capturedData := audioData
			capturedChunk := chunkNum
			go func() {
				defer func() { <-sem }()

				transcriptLines, err := r.sendAudioChunk(ctx, capturedData, micCfg.Format)
				if err != nil {
					if ctx.Err() == nil {
						errc <- fmt.Errorf("send chunk %d to whisper: %w", capturedChunk, err)
					}
					return
				}
				for _, txt := range transcriptLines {
					if txt == "" {
						continue
					}
					select {
					case lines <- Line{Text: txt, Raw: txt}:
					case <-ctx.Done():
						return
					}
				}
			}()
		}
	}()

	return lines, errc
}

// sendAudioChunk отправляет аудио чанк в облачный whisper и возвращает транскрипцию
func (r *Runner) sendAudioChunk(ctx context.Context, audioData []byte, format string) ([]string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Добавляем аудио файл
	part, err := writer.CreateFormFile("audio", fmt.Sprintf("chunk.%s", format))
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(audioData); err != nil {
		return nil, fmt.Errorf("write audio data: %w", err)
	}

	// Добавляем параметры
	_ = writer.WriteField("language", r.cfg.Language)
	_ = writer.WriteField("model", r.cfg.Model)
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	// Создаём HTTP запрос
	req, err := http.NewRequestWithContext(ctx, "POST", r.cfg.RemoteURL, &body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.applyHeaders(req)

	// Отправляем запрос
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}

	// Читаем ответ (NDJSON или JSON массив)
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var result []string

	// Пробуем NDJSON (построчный формат)
	text := string(data)
	for _, line := range strings.Split(text, "\n") {
		rawTrim := strings.TrimSpace(line)
		if rawTrim == "" {
			continue
		}
		var rl remoteLine
		if err := json.Unmarshal([]byte(rawTrim), &rl); err == nil {
			if rl.Text != "" {
				result = append(result, rl.Text)
			}
		}
	}

	// Если не получилось как NDJSON, пробуем JSON массив
	if len(result) == 0 {
		var arr []remoteLine
		if err := json.Unmarshal(data, &arr); err == nil {
			for _, rl := range arr {
				if rl.Text != "" {
					result = append(result, rl.Text)
				}
			}
		}
	}

	return result, nil
}
