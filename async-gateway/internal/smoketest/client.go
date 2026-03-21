package smoketest

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPollInterval = 10 * time.Second
	defaultTimeout      = 10 * time.Minute
)

type Config struct {
	GatewayBaseURL string
	APIKey         string
	Model          string
	Prompt         string
	RequestBody    []byte
	PollInterval   time.Duration
	Timeout        time.Duration
	HTTPClient     *http.Client
}

type Client struct {
	baseURL      string
	apiKey       string
	model        string
	prompt       string
	requestBody  []byte
	pollInterval time.Duration
	timeout      time.Duration
	httpClient   *http.Client
}

type Result struct {
	TaskID       string
	Status       string
	ImageURL     string
	ContentURL   string
	PollAttempts int
	Duration     time.Duration
}

type submitResponse struct {
	ID string `json:"id"`
}

type taskResponse struct {
	ID                 string          `json:"id"`
	Status             string          `json:"status"`
	Candidates         []taskCandidate `json:"candidates"`
	Error              *taskError      `json:"error"`
	TransportUncertain bool            `json:"transport_uncertain"`
}

type taskCandidate struct {
	Content struct {
		Parts []taskPart `json:"parts"`
	} `json:"content"`
}

type taskPart struct {
	Text       string `json:"text"`
	InlineData struct {
		Data string `json:"data"`
	} `json:"inlineData"`
}

type taskError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type listResponse struct {
	Items []listItem `json:"items"`
}

type listItem struct {
	ID         string `json:"id"`
	ContentURL string `json:"content_url"`
}

func NewClient(cfg Config) *Client {
	return &Client{
		baseURL:      strings.TrimRight(cfg.GatewayBaseURL, "/"),
		apiKey:       cfg.APIKey,
		model:        cfg.Model,
		prompt:       cfg.Prompt,
		requestBody:  append([]byte(nil), cfg.RequestBody...),
		pollInterval: durationOrDefault(cfg.PollInterval, defaultPollInterval),
		timeout:      durationOrDefault(cfg.Timeout, defaultTimeout),
		httpClient:   httpClientOrDefault(cfg.HTTPClient, cfg.Timeout),
	}
}

func (c *Client) Run(ctx context.Context) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	startedAt := time.Now()
	taskID, err := c.submit(ctx)
	if err != nil {
		return nil, err
	}

	pollAttempts := 0
	for {
		pollAttempts++
		task, retryAfter, err := c.getTask(ctx, taskID)
		if err != nil {
			return nil, err
		}

		switch strings.ToLower(strings.TrimSpace(task.Status)) {
		case "accepted", "queued", "running":
			if err := c.sleep(ctx, maxDuration(retryAfter, c.pollInterval)); err != nil {
				return nil, err
			}
			continue
		case "succeeded":
			imageURL, err := extractImageURL(task)
			if err != nil {
				return nil, err
			}
			contentURL, err := c.verifyListAndContent(ctx, taskID)
			if err != nil {
				return nil, err
			}
			return &Result{
				TaskID:       taskID,
				Status:       task.Status,
				ImageURL:     imageURL,
				ContentURL:   contentURL,
				PollAttempts: pollAttempts,
				Duration:     time.Since(startedAt),
			}, nil
		case "failed", "uncertain":
			if task.Error == nil {
				return nil, fmt.Errorf("task %s finished with status %s but missing error payload", taskID, task.Status)
			}
			return nil, fmt.Errorf("task %s finished with status %s: %s (%s)", taskID, task.Status, task.Error.Code, task.Error.Message)
		default:
			return nil, fmt.Errorf("task %s returned unexpected status %q", taskID, task.Status)
		}
	}
}

func (c *Client) submit(ctx context.Context) (string, error) {
	requestBody, err := c.buildRequestBody()
	if err != nil {
		return "", err
	}

	var gzipped bytes.Buffer
	zw := gzip.NewWriter(&gzipped)
	if _, err := zw.Write(requestBody); err != nil {
		return "", fmt.Errorf("gzip smoke request: %w", err)
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("close gzip smoke request: %w", err)
	}

	requestURL := fmt.Sprintf("%s/v1beta/models/%s:generateContent?output=url", c.baseURL, url.PathEscape(c.model))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(gzipped.Bytes()))
	if err != nil {
		return "", fmt.Errorf("create submit request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("submit smoke request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("submit smoke request returned %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var payload submitResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode submit response: %w", err)
	}
	if payload.ID == "" {
		return "", fmt.Errorf("submit response missing task id")
	}
	return payload.ID, nil
}

func (c *Client) buildRequestBody() ([]byte, error) {
	if len(c.requestBody) > 0 {
		return append([]byte(nil), c.requestBody...), nil
	}

	requestBody, err := json.Marshal(map[string]any{
		"contents": []any{
			map[string]any{
				"parts": []any{
					map[string]string{"text": c.prompt},
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal smoke request: %w", err)
	}
	return requestBody, nil
}

func (c *Client) getTask(ctx context.Context, taskID string) (*taskResponse, time.Duration, error) {
	requestURL := fmt.Sprintf("%s/v1/tasks/%s", c.baseURL, url.PathEscape(taskID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create task request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("query task status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("query task status returned %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var payload taskResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, 0, fmt.Errorf("decode task response: %w", err)
	}
	return &payload, parseRetryAfter(resp.Header.Get("Retry-After")), nil
}

func (c *Client) verifyListAndContent(ctx context.Context, taskID string) (string, error) {
	requestURL := fmt.Sprintf("%s/v1/tasks?days=3&limit=20", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", fmt.Errorf("create list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("query task list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("query task list returned %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var list listResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", fmt.Errorf("decode task list: %w", err)
	}

	found := false
	for _, item := range list.Items {
		if item.ID == taskID {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("task %s not found in recent task list", taskID)
	}

	contentReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/tasks/%s/content", c.baseURL, url.PathEscape(taskID)), nil)
	if err != nil {
		return "", fmt.Errorf("create content request: %w", err)
	}
	contentReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	contentResp, err := c.httpClient.Do(contentReq)
	if err != nil {
		return "", fmt.Errorf("query task content: %w", err)
	}
	defer contentResp.Body.Close()

	if contentResp.StatusCode != http.StatusFound {
		bodyBytes, _ := io.ReadAll(contentResp.Body)
		return "", fmt.Errorf("query task content returned %d: %s", contentResp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	location := strings.TrimSpace(contentResp.Header.Get("Location"))
	if location == "" {
		return "", fmt.Errorf("task content redirect missing location")
	}
	return location, nil
}

func extractImageURL(task *taskResponse) (string, error) {
	for _, candidate := range task.Candidates {
		for _, part := range candidate.Content.Parts {
			if imageURL := strings.TrimSpace(part.InlineData.Data); imageURL != "" {
				if _, err := parseHTTPURL(imageURL); err != nil {
					return "", fmt.Errorf("task %s succeeded but returned invalid image url %q: %w", task.ID, imageURL, err)
				}
				return imageURL, nil
			}
		}
	}
	return "", fmt.Errorf("task %s succeeded but returned no image url", task.ID)
}

func parseHTTPURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if parsed == nil || parsed.Host == "" {
		return nil, fmt.Errorf("host is required")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed, nil
	default:
		return nil, fmt.Errorf("scheme must be http or https")
	}
}

func parseRetryAfter(raw string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (c *Client) sleep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func httpClientOrDefault(client *http.Client, timeout time.Duration) *http.Client {
	if client != nil {
		cloned := *client
		cloned.Timeout = durationOrDefault(cloned.Timeout, durationOrDefault(timeout, defaultTimeout))
		cloned.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}
		return &cloned
	}
	return &http.Client{
		Timeout: durationOrDefault(timeout, defaultTimeout),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
