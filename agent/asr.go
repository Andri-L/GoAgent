package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ASRClient sends audio to the Hugging Face Inference API for speech-to-text.
type ASRClient struct {
	url        string
	token      string
	maxRetries int
	client     *http.Client
}

// NewASRClient creates a client for the Hugging Face Whisper API.
func NewASRClient(url, token string) *ASRClient {
	return &ASRClient{
		url:        url,
		token:      token,
		maxRetries: 3,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Transcribe sends a WAV file to the Whisper API and returns the transcription text.
func (c *ASRClient) Transcribe(wav []byte) (string, error) {
	if c.token == "" {
		return "", fmt.Errorf("HF_TOKEN is not configured")
	}

	for attempt := 0; attempt < c.maxRetries; attempt++ {
		req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(wav))
		if err != nil {
			return "", fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "audio/wav")

		resp, err := c.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("http request: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("read response: %w", err)
		}

		// Success
		if resp.StatusCode == http.StatusOK {
			var result map[string]interface{}
			if err := json.Unmarshal(body, &result); err != nil {
				return "", fmt.Errorf("unmarshal response: %w", err)
			}
			text, ok := result["text"].(string)
			if !ok {
				return "", fmt.Errorf("response missing 'text' field: %s", string(body))
			}
			return text, nil
		}

		// Cold start — model is loading
		if resp.StatusCode == http.StatusServiceUnavailable {
			var errData map[string]interface{}
			if err := json.Unmarshal(body, &errData); err == nil {
				if estimated, ok := errData["estimated_time"].(float64); ok && estimated > 0 {
					time.Sleep(time.Duration(estimated) * time.Second)
					continue
				}
			}
			// Fallback sleep if estimated_time not present
			time.Sleep(20 * time.Second)
			continue
		}

		// Rate limit
		if resp.StatusCode == http.StatusTooManyRequests {
			return "", fmt.Errorf("rate limit exceeded (429)")
		}

		return "", fmt.Errorf("HF API returned %d: %s", resp.StatusCode, string(body))
	}

	return "", fmt.Errorf("max retries (%d) exceeded", c.maxRetries)
}
