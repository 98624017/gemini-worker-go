package domain

import "time"

type TaskStatus string

const (
	TaskStatusAccepted  TaskStatus = "accepted"
	TaskStatusQueued    TaskStatus = "queued"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusSucceeded TaskStatus = "succeeded"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusUncertain TaskStatus = "uncertain"
)

type Task struct {
	ID                  string         `json:"id"`
	Status              TaskStatus     `json:"status"`
	Model               string         `json:"model"`
	OwnerHash           string         `json:"-"`
	RequestPath         string         `json:"-"`
	RequestQuery        string         `json:"-"`
	WorkerID            string         `json:"-"`
	HeartbeatAt         *time.Time     `json:"-"`
	RequestDispatchedAt *time.Time     `json:"-"`
	ResultSummary       *ResultSummary `json:"result_summary,omitempty"`
	ErrorCode           string         `json:"error_code,omitempty"`
	ErrorMessage        string         `json:"error_message,omitempty"`
	TransportUncertain  bool           `json:"transport_uncertain,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	FinishedAt          *time.Time     `json:"finished_at,omitempty"`
}

type TaskSummary struct {
	ID            string         `json:"id"`
	Status        TaskStatus     `json:"status"`
	Model         string         `json:"model"`
	ResultSummary *ResultSummary `json:"result_summary,omitempty"`
	ErrorCode     string         `json:"error_code,omitempty"`
	ErrorMessage  string         `json:"error_message,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	FinishedAt    *time.Time     `json:"finished_at,omitempty"`
	ContentURL    string         `json:"content_url,omitempty"`
}

type TaskPayload struct {
	TaskID             string            `json:"task_id"`
	RequestBodyJSON    []byte            `json:"request_body_json"`
	ForwardHeaders     map[string]string `json:"forward_headers"`
	AuthorizationCrypt []byte            `json:"auth_ciphertext"`
	ExpiresAt          time.Time         `json:"payload_expires_at"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

type ResultSummary struct {
	ImageURLs     []string       `json:"image_urls,omitempty"`
	FinishReason  string         `json:"finish_reason,omitempty"`
	ModelVersion  string         `json:"model_version,omitempty"`
	ResponseID    string         `json:"response_id,omitempty"`
	UsageMetadata map[string]any `json:"usage_metadata,omitempty"`
	TextSummary   string         `json:"text_summary,omitempty"`
}
