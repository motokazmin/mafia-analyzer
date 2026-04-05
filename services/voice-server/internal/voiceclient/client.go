package voiceclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client — HTTP-клиент к Python voice-worker (FastAPI).
type Client struct {
	BaseURL      string
	APIKey       string
	Client       *http.Client
	IngestClient *http.Client
}

func New(baseURL, apiKey string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &Client{
		BaseURL:      baseURL,
		APIKey:       apiKey,
		Client:       &http.Client{Timeout: 180 * time.Second},
		IngestClient: &http.Client{Timeout: 2 * time.Hour},
	}
}

func (c *Client) WipeVoices() error {
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/voices/wipe", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("wipe voices: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *Client) Reset() error {
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/reset", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("reset: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// HealthCheck pings /health on the voice worker. Returns nil if reachable.
func (c *Client) HealthCheck() error {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("health: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *Client) ProcessChunk(body *bytes.Buffer, contentType string, ingest bool) (*http.Response, error) {
	startTotal := time.Now()

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/process_chunk", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-API-Key", c.APIKey)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("process_chunk: status %d: %s", resp.StatusCode, string(b))
	}

	var submitted struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil {
		return nil, fmt.Errorf("process_chunk: decode job response: %w", err)
	}
	if submitted.JobID == "" {
		return nil, fmt.Errorf("process_chunk: empty job_id")
	}
	log.Printf("[voiceclient] job %s accepted", submitted.JobID[:8])

	deadline := time.Now().Add(2 * time.Hour)
	pollN := 0
	consecErrors := 0
	const maxConsecErrors = 3

	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		pollN++

		pollReq, err := http.NewRequest(http.MethodGet, c.BaseURL+"/jobs/"+submitted.JobID, nil)
		if err != nil {
			return nil, err
		}
		pollReq.Header.Set("X-API-Key", c.APIKey)

		pollResp, err := c.Client.Do(pollReq)
		if err != nil {
			consecErrors++
			log.Printf("[voiceclient] job %s poll #%d error (%d/%d): %v",
				submitted.JobID[:8], pollN, consecErrors, maxConsecErrors, err)

			if consecErrors >= maxConsecErrors {
				if hErr := c.HealthCheck(); hErr != nil {
					return nil, fmt.Errorf("process_chunk: worker unreachable after %d poll errors: %w", consecErrors, hErr)
				}
				log.Printf("[voiceclient] job %s health OK despite poll errors, continuing", submitted.JobID[:8])
				consecErrors = 0
			}
			continue
		}
		consecErrors = 0

		raw, err := io.ReadAll(pollResp.Body)
		pollResp.Body.Close()
		if err != nil {
			return nil, err
		}

		if pollResp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("poll job: status %d: %s", pollResp.StatusCode, string(raw))
		}

		var probe struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Status == "processing" {
			if pollN%12 == 0 {
				elapsed := time.Since(startTotal).Round(time.Second)
				log.Printf("[voiceclient] job %s still processing (%s elapsed, %d polls)", submitted.JobID[:8], elapsed, pollN)
			}
			continue
		}

		elapsed := time.Since(startTotal).Round(time.Second)
		log.Printf("[voiceclient] job %s done — %d polls, total=%s", submitted.JobID[:8], pollN, elapsed)

		fakeResp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(raw)),
		}
		fakeResp.Header.Set("Content-Type", "application/json")
		return fakeResp, nil
	}

	return nil, fmt.Errorf("process_chunk: job %s timed out after 2h", submitted.JobID)
}

func (c *Client) ListVoices() (body []byte, status int, err error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/voices", nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

func (c *Client) MergeVoices(sourceID, targetID string) error {
	payload, err := json.Marshal(map[string]string{
		"source_id": sourceID,
		"target_id": targetID,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/voices/merge", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.APIKey)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("merge: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *Client) SetVoiceFlags(voiceID string, unreliable bool) error {
	payload, err := json.Marshal(map[string]bool{"unreliable": unreliable})
	if err != nil {
		return err
	}
	u := c.BaseURL + "/voices/" + url.PathEscape(voiceID) + "/flags"
	req, err := http.NewRequest(http.MethodPatch, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.APIKey)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("flags: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *Client) LabelVoice(voiceID, displayName string) error {
	payload, err := json.Marshal(struct {
		DisplayName string `json:"display_name"`
	}{DisplayName: displayName})
	if err != nil {
		return err
	}
	u := c.BaseURL + "/voices/" + url.PathEscape(voiceID)
	req, err := http.NewRequest(http.MethodPatch, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.APIKey)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("label: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// SplitCandidate — профиль с бимодальным распределением эмбеддингов.
type SplitCandidate struct {
	VoiceID         string  `json:"voice_id"`
	DisplayName     string  `json:"display_name"`
	EmbeddingCount  int     `json:"embedding_count"`
	MaxPairwiseDist float64 `json:"max_pairwise_dist"`
	ClusterASize    int     `json:"cluster_a_size"`
	ClusterBSize    int     `json:"cluster_b_size"`
}

// GetSplitCandidates возвращает список профилей-кандидатов на разделение.
func (c *Client) GetSplitCandidates() ([]SplitCandidate, error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/voices/split_candidates", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("split_candidates: status %d: %s", resp.StatusCode, string(b))
	}
	var candidates []SplitCandidate
	if err := json.NewDecoder(resp.Body).Decode(&candidates); err != nil {
		return nil, fmt.Errorf("split_candidates: decode: %w", err)
	}
	return candidates, nil
}

// SplitVoice разбивает профиль voiceID на N профилей.
// Возвращает keptID (исходный профиль) и slice newIDs (новые профили).
func (c *Client) SplitVoice(voiceID string, clusterA, clusterB []int, extraClusters [][]int) (keptID string, newIDs []string, err error) {
	payload := map[string]interface{}{
		"voice_id": voiceID,
	}
	if clusterA != nil {
		payload["cluster_a"] = clusterA
	}
	if clusterB != nil {
		payload["cluster_b"] = clusterB
	}
	if len(extraClusters) > 0 {
		payload["extra_clusters"] = extraClusters
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/voices/split", bytes.NewReader(b))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.APIKey)
	resp, err := c.Client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("split: status %d: %s", resp.StatusCode, string(raw))
	}
	var result struct {
		Kept struct {
			VoiceID string `json:"voice_id"`
		} `json:"kept"`
		New []struct {
			VoiceID string `json:"voice_id"`
		} `json:"new"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", nil, fmt.Errorf("split: decode: %w", err)
	}
	ids := make([]string, 0, len(result.New))
	for _, n := range result.New {
		ids = append(ids, n.VoiceID)
	}
	return result.Kept.VoiceID, ids, nil
}

// DeleteVoiceSegments удаляет историю эмбеддингов профиля (сбрасывает детектор расхождения).
func (c *Client) DeleteVoiceSegments(voiceID string) error {
	u := c.BaseURL + "/voices/" + url.PathEscape(voiceID) + "/segments"
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete segments: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
