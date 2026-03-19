package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadFromEnvRequiresMandatorySettings(t *testing.T) {
	cases := []struct {
		name       string
		missingKey string
	}{
		{name: "missing newapi base url", missingKey: "NEWAPI_BASE_URL"},
		{name: "missing postgres dsn", missingKey: "POSTGRES_DSN"},
		{name: "missing owner hash secret", missingKey: "OWNER_HASH_SECRET"},
		{name: "missing payload encryption key", missingKey: "TASK_PAYLOAD_ENCRYPTION_KEY"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			setValidEnv(t)
			t.Setenv(tc.missingKey, "")

			_, err := LoadFromEnv()
			if err == nil {
				t.Fatalf("expected error for %s", tc.missingKey)
			}
			if !strings.Contains(err.Error(), tc.missingKey) {
				t.Fatalf("expected error to mention %s, got %v", tc.missingKey, err)
			}
		})
	}
}

func TestLoadFromEnvInjectsDefaults(t *testing.T) {
	setValidEnv(t)

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.ListenAddr != defaultListenAddr {
		t.Fatalf("ListenAddr = %q, want %q", cfg.ListenAddr, defaultListenAddr)
	}
	if cfg.MaxInflightTasks != defaultMaxInflightTasks {
		t.Fatalf("MaxInflightTasks = %d, want %d", cfg.MaxInflightTasks, defaultMaxInflightTasks)
	}
	if cfg.MaxQueueSize != defaultMaxQueueSize {
		t.Fatalf("MaxQueueSize = %d, want %d", cfg.MaxQueueSize, defaultMaxQueueSize)
	}
	if cfg.TaskPollRetryAfterSec != defaultTaskPollRetryAfterSec {
		t.Fatalf("TaskPollRetryAfterSec = %d, want %d", cfg.TaskPollRetryAfterSec, defaultTaskPollRetryAfterSec)
	}
	if cfg.NewAPIRequestTimeout != defaultNewAPIRequestTimeout {
		t.Fatalf("NewAPIRequestTimeout = %s, want %s", cfg.NewAPIRequestTimeout, defaultNewAPIRequestTimeout)
	}
	if cfg.ShutdownGracePeriod != defaultShutdownGracePeriod {
		t.Fatalf("ShutdownGracePeriod = %s, want %s", cfg.ShutdownGracePeriod, defaultShutdownGracePeriod)
	}
}

func TestLoadFromEnvFallsBackToDefaultsForInvalidNumbers(t *testing.T) {
	setValidEnv(t)
	t.Setenv("MAX_INFLIGHT_TASKS", "bad")
	t.Setenv("MAX_QUEUE_SIZE", "-1")
	t.Setenv("TASK_POLL_RETRY_AFTER_SEC", "0")
	t.Setenv("NEWAPI_REQUEST_TIMEOUT_MS", "oops")
	t.Setenv("SHUTDOWN_GRACE_PERIOD_SEC", "invalid")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	assertDuration(t, "NewAPIRequestTimeout", cfg.NewAPIRequestTimeout, defaultNewAPIRequestTimeout)
	assertDuration(t, "ShutdownGracePeriod", cfg.ShutdownGracePeriod, defaultShutdownGracePeriod)

	if cfg.MaxInflightTasks != defaultMaxInflightTasks {
		t.Fatalf("MaxInflightTasks = %d, want %d", cfg.MaxInflightTasks, defaultMaxInflightTasks)
	}
	if cfg.MaxQueueSize != defaultMaxQueueSize {
		t.Fatalf("MaxQueueSize = %d, want %d", cfg.MaxQueueSize, defaultMaxQueueSize)
	}
	if cfg.TaskPollRetryAfterSec != defaultTaskPollRetryAfterSec {
		t.Fatalf("TaskPollRetryAfterSec = %d, want %d", cfg.TaskPollRetryAfterSec, defaultTaskPollRetryAfterSec)
	}
}

func setValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NEWAPI_BASE_URL", "http://newapi.internal")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost:5432/dbname?sslmode=disable")
	t.Setenv("OWNER_HASH_SECRET", "owner-secret")
	t.Setenv("TASK_PAYLOAD_ENCRYPTION_KEY", "payload-secret")
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("MAX_INFLIGHT_TASKS", "")
	t.Setenv("MAX_QUEUE_SIZE", "")
	t.Setenv("TASK_POLL_RETRY_AFTER_SEC", "")
	t.Setenv("NEWAPI_REQUEST_TIMEOUT_MS", "")
	t.Setenv("SHUTDOWN_GRACE_PERIOD_SEC", "")
}

func assertDuration(t *testing.T, field string, got, want time.Duration) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %s, want %s", field, got, want)
	}
}
