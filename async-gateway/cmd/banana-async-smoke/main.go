package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"banana-async-gateway/internal/smoketest"
)

const (
	defaultGatewayBaseURL = "http://127.0.0.1:18080"
	defaultModel          = "gemini-3-pro-image-preview"
	defaultPrompt         = "draw a single ripe yellow banana on a clean white background"
	defaultPollInterval   = 3 * time.Second
	defaultTimeout        = 10 * time.Minute
)

func main() {
	logger := log.New(os.Stdout, "banana-async-smoke ", log.LstdFlags|log.LUTC)

	apiKey := strings.TrimSpace(os.Getenv("SMOKE_API_KEY"))
	if apiKey == "" {
		logger.Fatal("SMOKE_API_KEY is required")
	}

	requestBody, err := loadRequestBodyFromFile(strings.TrimSpace(os.Getenv("SMOKE_BODY_FILE")))
	if err != nil {
		logger.Fatalf("load smoke request body: %v", err)
	}

	client := smoketest.NewClient(smoketest.Config{
		GatewayBaseURL: envOrDefault("SMOKE_GATEWAY_BASE_URL", defaultGatewayBaseURL),
		APIKey:         apiKey,
		Model:          envOrDefault("SMOKE_MODEL", defaultModel),
		Prompt:         envOrDefault("SMOKE_PROMPT", defaultPrompt),
		RequestBody:    requestBody,
		PollInterval:   parseDurationSeconds("SMOKE_POLL_INTERVAL_SEC", defaultPollInterval),
		Timeout:        parseDurationSeconds("SMOKE_TIMEOUT_SEC", defaultTimeout),
	})

	result, err := client.Run(context.Background())
	if err != nil {
		logger.Fatalf("smoke test failed: %v", err)
	}

	fmt.Printf("task_id=%s status=%s image_url=%s content_url=%s duration=%s polls=%d\n",
		result.TaskID,
		result.Status,
		result.ImageURL,
		result.ContentURL,
		result.Duration.Round(time.Millisecond),
		result.PollAttempts,
	)
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func parseDurationSeconds(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func loadRequestBodyFromFile(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return body, nil
}
