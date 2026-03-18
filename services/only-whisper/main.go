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
	"strconv"
	"time"
)

const (
	APIKey        = "barchik"
	minWAVSize    = 44 + 16000*1*2*2           // WAV-заголовок + 2 сек @ 16kHz mono int16
	chunkDuration = 30                         // длина чанка в секундах
	overlapSec    = 10                         // перекрытие между чанками
	stepSec       = chunkDuration - overlapSec // шаг = 20 сек

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
	// Увеличен таймаут — overlap + фильтрация требуют больше времени на обработку
	client = &http.Client{Timeout: 180 * time.Second}
)

type Segment struct {
	Speaker string
	Text    string
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

func sendChunk(audioData []byte, chunkID int) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", fmt.Sprintf("chunk_%d.wav", chunkID))
	if err != nil {
		log.Printf(">>> Ошибка создания form-file для чанка %d: %v", chunkID, err)
		return
	}
	if _, err := part.Write(audioData); err != nil {
		log.Printf(">>> Ошибка записи аудио в форму: %v", err)
		return
	}

	// Передаём только максимум — диаризатор сам решает сколько голосов в чанке.
	// Глобальное разделение на N игроков делает реестр эмбеддингов.
	if NumSpeakers > 0 {
		if err := writer.WriteField("max_speakers", strconv.Itoa(NumSpeakers)); err != nil {
			log.Printf(">>> Ошибка записи max_speakers: %v", err)
			return
		}
		// Минимум — 1, диаризатор не обязан найти всех в каждом чанке
		if err := writer.WriteField("min_speakers", "1"); err != nil {
			log.Printf(">>> Ошибка записи min_speakers: %v", err)
			return
		}
	}

	if err := writer.Close(); err != nil {
		log.Printf(">>> Ошибка закрытия multipart writer: %v", err)
		return
	}

	req, err := http.NewRequest("POST", BaseURL+"/process_chunk", body)
	if err != nil {
		log.Printf(">>> Ошибка создания запроса для чанка %d: %v", chunkID, err)
		return
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-API-Key", APIKey)

	resp, err := client.Do(req)
	if err != nil {
		log.Printf(">>> Ошибка отправки чанка %d: %v", chunkID, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf(">>> Сервер вернул статус %d для чанка %d", resp.StatusCode, chunkID)
		return
	}

	var data struct {
		Segments []Segment
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf(">>> Ошибка декодирования ответа для чанка %d: %v", chunkID, err)
		return
	}

	for _, s := range data.Segments {
		if s.Text != "" {
			saveAndPrint(s)
		}
	}
}

func runRecord(ctx context.Context) {
	fmt.Println(">>> Режим записи микрофона. Ctrl+C для остановки.")
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			fmt.Println(">>> Запись остановлена.")
			return
		default:
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
		sendChunk(data, i)
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

		offset := i * stepSec // 0, 20, 40, 60... сек

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
		sendChunk(data, i)
	}
}

func main() {
	if len(os.Args) < 4 {
		fmt.Println("Использование:")
		fmt.Println("  запись: go run main.go record <URL> <СПИКЕРЫ>")
		fmt.Println("  файл:   go run main.go file <URL> <ПУТЬ_К_ФАЙЛУ> <СПИКЕРЫ>")
		os.Exit(1)
	}

	mode := os.Args[1]
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
		log.Fatalf("Неизвестный режим: %q. Используйте record или file.", mode)
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
