package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"banana-async-gateway/internal/security"
)

const (
	defaultListenAddr            = ":8080"
	defaultMaxInflightTasks      = 32
	defaultMaxQueueSize          = 256
	defaultTaskPollRetryAfterSec = 10
	defaultPostgresMaxOpenConns  = 20
	defaultPostgresMaxIdleConns  = 10
	defaultNewAPIRequestTimeout  = 20 * time.Minute
	defaultShutdownGracePeriod   = 30 * time.Second
)

type Config struct {
	ListenAddr               string
	NewAPIBaseURL            string
	PostgresDSN              string
	OwnerHashSecret          string
	TaskPayloadEncryptionKey string
	PostgresMaxOpenConns     int
	PostgresMaxIdleConns     int
	MaxInflightTasks         int
	MaxQueueSize             int
	TaskPollRetryAfterSec    int
	NewAPIRequestTimeout     time.Duration
	ShutdownGracePeriod      time.Duration
}

func LoadFromEnv() (Config, error) {
	cfg := Config{
		ListenAddr:            defaultListenAddr,
		PostgresMaxOpenConns:  defaultPostgresMaxOpenConns,
		PostgresMaxIdleConns:  defaultPostgresMaxIdleConns,
		MaxInflightTasks:      defaultMaxInflightTasks,
		MaxQueueSize:          defaultMaxQueueSize,
		TaskPollRetryAfterSec: defaultTaskPollRetryAfterSec,
		NewAPIRequestTimeout:  defaultNewAPIRequestTimeout,
		ShutdownGracePeriod:   defaultShutdownGracePeriod,
	}

	cfg.ListenAddr = getEnvOrDefault("LISTEN_ADDR", defaultListenAddr)
	cfg.NewAPIBaseURL = strings.TrimSpace(os.Getenv("NEWAPI_BASE_URL"))
	cfg.PostgresDSN = strings.TrimSpace(os.Getenv("POSTGRES_DSN"))
	cfg.OwnerHashSecret = strings.TrimSpace(os.Getenv("OWNER_HASH_SECRET"))
	cfg.TaskPayloadEncryptionKey = strings.TrimSpace(os.Getenv("TASK_PAYLOAD_ENCRYPTION_KEY"))

	var missing []string
	if cfg.NewAPIBaseURL == "" {
		missing = append(missing, "NEWAPI_BASE_URL")
	}
	if cfg.PostgresDSN == "" {
		missing = append(missing, "POSTGRES_DSN")
	}
	if cfg.OwnerHashSecret == "" {
		missing = append(missing, "OWNER_HASH_SECRET")
	}
	if cfg.TaskPayloadEncryptionKey == "" {
		missing = append(missing, "TASK_PAYLOAD_ENCRYPTION_KEY")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}

	if err := validateBaseURL(cfg.NewAPIBaseURL); err != nil {
		return Config{}, fmt.Errorf("invalid NEWAPI_BASE_URL: %w", err)
	}
	if _, err := security.ParseEncryptionKey(cfg.TaskPayloadEncryptionKey); err != nil {
		return Config{}, err
	}

	cfg.MaxInflightTasks = getPositiveIntOrDefault("MAX_INFLIGHT_TASKS", defaultMaxInflightTasks)
	cfg.MaxQueueSize = getPositiveIntOrDefault("MAX_QUEUE_SIZE", defaultMaxQueueSize)
	cfg.TaskPollRetryAfterSec = getPositiveIntOrDefault("TASK_POLL_RETRY_AFTER_SEC", defaultTaskPollRetryAfterSec)
	cfg.PostgresMaxOpenConns = getPositiveIntOrDefault("POSTGRES_MAX_OPEN_CONNS", defaultPostgresMaxOpenConns)
	cfg.PostgresMaxIdleConns = getPositiveIntOrDefault("POSTGRES_MAX_IDLE_CONNS", defaultPostgresMaxIdleConns)
	cfg.NewAPIRequestTimeout = time.Duration(getPositiveIntOrDefault("NEWAPI_REQUEST_TIMEOUT_MS", int(defaultNewAPIRequestTimeout/time.Millisecond))) * time.Millisecond
	cfg.ShutdownGracePeriod = time.Duration(getPositiveIntOrDefault("SHUTDOWN_GRACE_PERIOD_SEC", int(defaultShutdownGracePeriod/time.Second))) * time.Second

	return cfg, nil
}

func getEnvOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getPositiveIntOrDefault(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("scheme and host are required")
	}
	return nil
}
