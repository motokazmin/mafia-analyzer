package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"
)

const (
	APIKey        = "barchik"
	minWAVSize    = 44 + 16000*1*2*2           // WAV-заголовок + 2 сек @ 16kHz mono int16
	chunkDuration = 20                         // длина чанка в секундах
	overlapSec    = 4                          // перекрытие между чанками
	stepSec       = chunkDuration - overlapSec // шаг = 16 сек

	// Голосовой фильтр:
	//   highpass=300   — убирает низкочастотный гул и фоновый шум
	//   lowpass=3400   — убирает высокочастотный шум выше диапазона голоса
	//   dynaudnorm     — динамическая нормализация громкости
	audioFilter = "highpass=f=300,lowpass=f=3400,dynaudnorm=p=0.9"
)

var (
	BaseURL     string
	NumSpeakers int
	LogFile     *os.File
	client      = &http.Client{Timeout: 180 * time.Second}
	// Целый файл (process_chunk + full_file) может обрабатываться долго — отдельный клиент.
	ingestClient = &http.Client{Timeout: 2 * time.Hour}
)

type Segment struct {
	Speaker  string  `json:"speaker"`
	Text     string  `json:"text"`
	AbsStart float64 `json:"abs_start"`
	AbsEnd   float64 `json:"abs_end"`
}

func resetServerMemory() {
	req, err := http.NewRequest("POST", BaseURL+"/reset", nil)
	if err != nil {
		log.Printf(">>> Ошибка создания запроса reset: %v", err)
		return
	}
	req.Header.Set("X-API-Key", APIKey)

	resp, err := client.Do(req)
	if err != nil {
		log.Printf(">>> Ошибка сброса памяти сервера: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		fmt.Println(">>> Память сервера очищена.")
	} else {
		log.Printf(">>> Сервер вернул статус %d при сбросе памяти", resp.StatusCode)
	}
}

func saveAndPrint(s Segment) {
	entry := fmt.Sprintf("[%s] %s: %s\n", time.Now().Format("15:04:05"), s.Speaker, s.Text)
	fmt.Print(entry)
	if LogFile != nil {
		if _, err := LogFile.WriteString(entry); err != nil {
			log.Printf(">>> Ошибка записи в лог: %v", err)
		}
	}
}

// sendChunk отправляет один аудио-чанк на сервер.
//
// chunkIndex — порядковый номер чанка (0, 1, 2, ...).
// absStart   — абсолютное начало чанка в потоке (секунды от старта).
//
// Разница между absStart и chunkIndex * stepSec:
//   - В режиме файла: absStart = chunkIndex * stepSec (детерминировано).
//   - В режиме записи: absStart считается по реальному времени старта чанка,
//     чтобы учесть возможные задержки ffmpeg.
func sendChunk(audioData []byte, chunkIndex int, absStart float64) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", fmt.Sprintf("chunk_%d.wav", chunkIndex))
	if err != nil {
		log.Printf(">>> Ошибка создания form-file для чанка %d: %v", chunkIndex, err)
		return
	}
	if _, err := part.Write(audioData); err != nil {
		log.Printf(">>> Ошибка записи аудио в форму: %v", err)
		return
	}

	if NumSpeakers > 0 {
		if err := writer.WriteField("max_speakers", strconv.Itoa(NumSpeakers)); err != nil {
			log.Printf(">>> Ошибка записи max_speakers: %v", err)
			return
		}
		if err := writer.WriteField("min_speakers", "1"); err != nil {
			log.Printf(">>> Ошибка записи min_speakers: %v", err)
			return
		}
	}

	// --- Новые поля для дедупликации overlap ---
	if err := writer.WriteField("chunk_abs_start", fmt.Sprintf("%.3f", absStart)); err != nil {
		log.Printf(">>> Ошибка записи chunk_abs_start: %v", err)
		return
	}
	if err := writer.WriteField("overlap_sec", fmt.Sprintf("%.1f", float64(overlapSec))); err != nil {
		log.Printf(">>> Ошибка записи overlap_sec: %v", err)
		return
	}

	if err := writer.Close(); err != nil {
		log.Printf(">>> Ошибка закрытия multipart writer: %v", err)
		return
	}

	req, err := http.NewRequest("POST", BaseURL+"/process_chunk", body)
	if err != nil {
		log.Printf(">>> Ошибка создания запроса для чанка %d: %v", chunkIndex, err)
		return
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-API-Key", APIKey)

	resp, err := client.Do(req)
	if err != nil {
		log.Printf(">>> Ошибка отправки чанка %d: %v", chunkIndex, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf(">>> Сервер вернул статус %d для чанка %d", resp.StatusCode, chunkIndex)
		return
	}

	var data struct {
		Segments []Segment `json:"segments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf(">>> Ошибка декодирования ответа для чанка %d: %v", chunkIndex, err)
		return
	}

	for _, s := range data.Segments {
		if s.Text != "" {
			saveAndPrint(s)
		}
	}
}

// sendIngestFull отправляет целый файл на voice-server POST /process_chunk (full_file=true)
// (глобальная диаризация по полной длине; Python нужен только на сервере).
func sendIngestFull(filePath string) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		log.Fatalf(">>> Не удалось прочитать файл: %v", err)
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		log.Fatalf(">>> multipart file: %v", err)
	}
	if _, err := part.Write(raw); err != nil {
		log.Fatalf(">>> запись тела файла: %v", err)
	}

	if NumSpeakers > 0 {
		if err := writer.WriteField("max_speakers", strconv.Itoa(NumSpeakers)); err != nil {
			log.Fatalf(">>> max_speakers: %v", err)
		}
		if err := writer.WriteField("min_speakers", "1"); err != nil {
			log.Fatalf(">>> min_speakers: %v", err)
		}
	}
	if err := writer.WriteField("full_file", "true"); err != nil {
		log.Fatalf(">>> full_file: %v", err)
	}

	if err := writer.Close(); err != nil {
		log.Fatalf(">>> закрытие multipart: %v", err)
	}

	req, err := http.NewRequest("POST", BaseURL+"/process_chunk", body)
	if err != nil {
		log.Fatalf(">>> запрос process_chunk (full_file): %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-API-Key", APIKey)

	fmt.Printf(">>> Отправка %s на %s/process_chunk (full_file) …\n", filePath, BaseURL)
	resp, err := ingestClient.Do(req)
	if err != nil {
		log.Fatalf(">>> process_chunk: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Fatalf(">>> сервер вернул статус %d", resp.StatusCode)
	}

	var data struct {
		Segments []Segment `json:"segments"`
		Message  string    `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Fatalf(">>> декодирование ответа: %v", err)
	}
	if data.Message != "" {
		fmt.Println(">>>", data.Message)
	}
	fmt.Printf(">>> Сегментов в ответе: %d\n", len(data.Segments))
	for _, s := range data.Segments {
		if s.Text != "" {
			saveAndPrint(s)
		}
	}
}

func runRecord(ctx context.Context) {
	fmt.Println(">>> Режим записи микрофона. Ctrl+C для остановки.")

	// recordStart фиксирует момент старта записи.
	// absStart для каждого чанка считается от него, а не от chunkIndex*stepSec,
	// чтобы накопленные задержки ffmpeg не сдвигали таймлайн.
	recordStart := time.Now()

	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			fmt.Println(">>> Запись остановлена.")
			return
		default:
		}

		// Абсолютное начало чанка — реальное время от старта записи.
		// Для первого чанка = 0, для второго ≈ stepSec, и т.д.
		// Небольшое отклонение от stepSec*i допустимо — сервер фильтрует по committed_end.
		absStart := time.Since(recordStart).Seconds() - float64(chunkDuration)
		if absStart < 0 {
			absStart = 0
		}

		cmd := exec.CommandContext(ctx,
			"ffmpeg",
			"-f", "alsa", "-i", "default",
			"-t", strconv.Itoa(chunkDuration),
			"-ar", "16000", "-ac", "1",
			"-af", audioFilter,
			"-f", "wav", "pipe:1",
		)
		data, err := cmd.Output()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf(">>> Ошибка ffmpeg (чанк %d): %v", i, err)
			continue
		}
		sendChunk(data, i, absStart)
	}
}

func runFile(ctx context.Context, filePath string) {
	fmt.Printf(">>> Режим обработки файла: %s\n", filePath)
	fmt.Printf(">>> Чанк %d сек, шаг %d сек, перекрытие %d сек\n", chunkDuration, stepSec, overlapSec)

	if _, err := os.Stat(filePath); err != nil {
		log.Fatalf(">>> Файл не найден: %s", filePath)
	}

	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			fmt.Println(">>> Обработка остановлена.")
			return
		default:
		}

		// offset — откуда ffmpeg начинает читать файл (с учётом overlap).
		// Для чанка 0: offset=0, чанк читает [0..20s].
		// Для чанка 1: offset=16, чанк читает [16..36s] (4с overlap).
		// Для чанка 2: offset=32, чанк читает [32..52s] и т.д.
		offset := i * stepSec

		// absStart — абсолютное начало чанка в потоке для сервера.
		// Сервер отдаёт только сегменты с abs_start >= committed_end,
		// поэтому 4-секундная зона [offset .. offset+overlapSec] будет отброшена
		// начиная со второго чанка.
		absStart := float64(offset)

		cmd := exec.CommandContext(ctx,
			"ffmpeg",
			"-ss", strconv.Itoa(offset),
			"-t", strconv.Itoa(chunkDuration),
			"-i", filePath,
			"-ar", "16000", "-ac", "1",
			"-af", audioFilter,
			"-f", "wav", "pipe:1",
		)
		data, err := cmd.Output()
		if err != nil || len(data) < minWAVSize {
			fmt.Println(">>> Файл обработан полностью.")
			return
		}
		sendChunk(data, i, absStart)
	}
}

func printUsage() {
	fmt.Println("Использование:")
	fmt.Println("  запись: go run main.go record <URL> <СПИКЕРЫ>")
	fmt.Println("  файл:   go run main.go file <URL> <ПУТЬ_К_ФАЙЛУ> <СПИКЕРЫ>")
	fmt.Println("  ingest: go run main.go ingest <URL> <ПУТЬ_К_ФАЙЛУ> [СПИКЕРЫ]  — целый файл: POST /process_chunk, full_file=true")
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	mode := os.Args[1]

	if mode == "ingest" {
		if len(os.Args) < 4 {
			log.Fatal("ingest: go run main.go ingest <URL> <ПУТЬ_К_ФАЙЛУ> [СПИКЕРЫ]")
		}
		BaseURL = os.Args[2]
		filePath := os.Args[3]
		if len(os.Args) >= 5 {
			NumSpeakers, _ = strconv.Atoi(os.Args[4])
		}

		logPath := fmt.Sprintf("session_%d.txt", time.Now().Unix())
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.Fatalf(">>> Не удалось открыть лог-файл: %v", err)
		}
		LogFile = f
		defer f.Close()
		fmt.Printf(">>> Лог сохраняется в: %s\n", logPath)

		// Не вызываем reset: реестр голосов на сервере в БД, ingest только дополняет.
		sendIngestFull(filePath)
		return
	}

	if len(os.Args) < 4 {
		printUsage()
		os.Exit(1)
	}

	BaseURL = os.Args[2]

	switch mode {
	case "record":
		if len(os.Args) < 4 {
			log.Fatal("record mode: go run main.go record <URL> <СПИКЕРЫ>")
		}
		NumSpeakers, _ = strconv.Atoi(os.Args[3])
	case "file":
		if len(os.Args) < 5 {
			log.Fatal("file mode: go run main.go file <URL> <ПУТЬ_К_ФАЙЛУ> <СПИКЕРЫ>")
		}
		NumSpeakers, _ = strconv.Atoi(os.Args[4])
	default:
		log.Fatalf("Неизвестный режим: %q.", mode)
	}

	logPath := fmt.Sprintf("session_%d.txt", time.Now().Unix())
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf(">>> Не удалось открыть лог-файл: %v", err)
	}
	LogFile = f
	defer f.Close()
	fmt.Printf(">>> Лог сохраняется в: %s\n", logPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	resetServerMemory()

	switch mode {
	case "record":
		runRecord(ctx)
	case "file":
		runFile(ctx, os.Args[3])
	}
}
