package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"voice-server/internal/domain"
	"voice-server/internal/voiceclient"
)

const (
	MinWAVSize    = 44 + 16000*1*2*2
	ChunkDuration = 20
	OverlapSec    = 4
	StepSec       = ChunkDuration - OverlapSec
	audioFilter   = "highpass=f=300,lowpass=f=3400,dynaudnorm=p=0.9"
)

func SendChunk(vc *voiceclient.Client, numSpeakers int, onSegment func(domain.Segment), audioData []byte, chunkIndex int, absStart float64) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", fmt.Sprintf("chunk_%d.wav", chunkIndex))
	if err != nil {
		log.Printf("chunk %d: form file: %v", chunkIndex, err)
		return
	}
	if _, err := part.Write(audioData); err != nil {
		log.Printf("chunk %d: write audio: %v", chunkIndex, err)
		return
	}

	if numSpeakers > 0 {
		if err := writer.WriteField("max_speakers", strconv.Itoa(numSpeakers)); err != nil {
			log.Printf("chunk %d: max_speakers: %v", chunkIndex, err)
			return
		}
		if err := writer.WriteField("min_speakers", "1"); err != nil {
			log.Printf("chunk %d: min_speakers: %v", chunkIndex, err)
			return
		}
	}

	if err := writer.WriteField("chunk_abs_start", fmt.Sprintf("%.3f", absStart)); err != nil {
		log.Printf("chunk %d: chunk_abs_start: %v", chunkIndex, err)
		return
	}
	if err := writer.WriteField("overlap_sec", fmt.Sprintf("%.1f", float64(OverlapSec))); err != nil {
		log.Printf("chunk %d: overlap_sec: %v", chunkIndex, err)
		return
	}

	if err := writer.Close(); err != nil {
		log.Printf("chunk %d: close writer: %v", chunkIndex, err)
		return
	}

	resp, err := vc.ProcessChunk(body, writer.FormDataContentType(), false)
	if err != nil {
		log.Printf("chunk %d: request: %v", chunkIndex, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("chunk %d: status %d", chunkIndex, resp.StatusCode)
		return
	}

	var data struct {
		Segments []domain.Segment `json:"segments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("chunk %d: decode: %v", chunkIndex, err)
		return
	}

	for _, s := range data.Segments {
		if s.Text != "" {
			onSegment(s)
		}
	}
}

func SendIngestFull(vc *voiceclient.Client, numSpeakers int, onSegment func(domain.Segment), filePath string) error {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("multipart file: %w", err)
	}
	if _, err := part.Write(raw); err != nil {
		return fmt.Errorf("write body: %w", err)
	}

	if numSpeakers > 0 {
		if err := writer.WriteField("max_speakers", strconv.Itoa(numSpeakers)); err != nil {
			return fmt.Errorf("max_speakers: %w", err)
		}
		if err := writer.WriteField("min_speakers", "1"); err != nil {
			return fmt.Errorf("min_speakers: %w", err)
		}
	}
	if err := writer.WriteField("full_file", "true"); err != nil {
		return fmt.Errorf("full_file: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart: %w", err)
	}

	resp, err := vc.ProcessChunk(body, writer.FormDataContentType(), true)
	if err != nil {
		return fmt.Errorf("process_chunk: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server status %d: %s", resp.StatusCode, string(b))
	}

	var data struct {
		Segments []domain.Segment `json:"segments"`
		Message  string           `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if data.Message != "" {
		log.Printf("ingest message: %s", data.Message)
	}
	for _, s := range data.Segments {
		if s.Text != "" {
			onSegment(s)
		}
	}
	return nil
}

func RunRecord(ctx context.Context, vc *voiceclient.Client, numSpeakers int, onSegment func(domain.Segment)) {
	recordStart := time.Now()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		absStart := time.Since(recordStart).Seconds() - float64(ChunkDuration)
		if absStart < 0 {
			absStart = 0
		}

		cmd := exec.CommandContext(ctx,
			"ffmpeg",
			"-f", "alsa", "-i", "default",
			"-t", strconv.Itoa(ChunkDuration),
			"-ar", "16000", "-ac", "1",
			"-af", audioFilter,
			"-f", "wav", "pipe:1",
		)
		data, err := cmd.Output()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("ffmpeg chunk %d: %v", i, err)
			continue
		}
		SendChunk(vc, numSpeakers, onSegment, data, i, absStart)
	}
}

func RunFile(ctx context.Context, vc *voiceclient.Client, numSpeakers int, onSegment func(domain.Segment), filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("file not found: %w", err)
	}

	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		offset := i * StepSec
		absStart := float64(offset)

		cmd := exec.CommandContext(ctx,
			"ffmpeg",
			"-ss", strconv.Itoa(offset),
			"-t", strconv.Itoa(ChunkDuration),
			"-i", filePath,
			"-ar", "16000", "-ac", "1",
			"-af", audioFilter,
			"-f", "wav", "pipe:1",
		)
		data, err := cmd.Output()
		if err != nil || len(data) < MinWAVSize {
			return nil
		}
		SendChunk(vc, numSpeakers, onSegment, data, i, absStart)
	}
}
