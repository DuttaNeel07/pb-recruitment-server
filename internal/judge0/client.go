package judge0

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type Client struct {
	baseURL      string
	authToken    string
	callbackBase string
	httpClient   *http.Client
}

func NewClient() *Client {
	timeout := 30 * time.Second
	if raw := os.Getenv("JUDGE0_TIMEOUT_MS"); raw != "" {
		if ms, err := time.ParseDuration(raw + "ms"); err == nil {
			timeout = ms
		}
	}

	return &Client{
		baseURL:      os.Getenv("JUDGE0_URL"),
		authToken:    os.Getenv("JUDGE0_AUTH_TOKEN"),
		callbackBase: os.Getenv("JUDGE0_CALLBACK_BASE_URL"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *Client) CallbackURL(executionID string) string {
	if c.callbackBase == "" {
		return ""
	}
	return fmt.Sprintf("%s/internal/judge0/callback/%s", c.callbackBase, executionID)
}

func (c *Client) CreateBatch(ctx context.Context, jobs []SubmissionRequest) ([]SubmissionResult, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("%w: JUDGE0_URL is not set", ErrUnavailable)
	}
	if len(jobs) == 0 {
		return []SubmissionResult{}, nil
	}

	payload, err := json.Marshal(map[string][]SubmissionRequest{
		"submissions": jobs,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/submissions/batch?base64_encoded=false", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("X-Auth-Token", c.authToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d: %s", ErrUnavailable, resp.StatusCode, string(body))
	}

	var tokens []batchTokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	results := make([]SubmissionResult, len(jobs))
	for i := range jobs {
		if i >= len(tokens) {
			results[i] = SubmissionResult{Error: ErrInvalidResponse}
			continue
		}
		if tokens[i].Token == "" {
			results[i] = SubmissionResult{Error: ErrInvalidResponse}
			continue
		}
		results[i] = SubmissionResult{Token: tokens[i].Token}
	}

	return results, nil
}
