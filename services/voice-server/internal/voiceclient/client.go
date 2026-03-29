package voiceclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

func (c *Client) ProcessChunk(body *bytes.Buffer, contentType string, ingest bool) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/process_chunk", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-API-Key", c.APIKey)
	cl := c.Client
	if ingest {
		cl = c.IngestClient
	}
	return cl.Do(req)
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
