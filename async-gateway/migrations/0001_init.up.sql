CREATE TABLE tasks (
  task_id               TEXT PRIMARY KEY,
  status                TEXT NOT NULL CHECK (status IN ('accepted', 'queued', 'running', 'succeeded', 'failed', 'uncertain')),
  model                 TEXT NOT NULL,
  owner_hash            TEXT NOT NULL,
  request_path          TEXT NOT NULL,
  request_query         TEXT NOT NULL DEFAULT '',
  worker_id             TEXT NOT NULL DEFAULT '',
  heartbeat_at          TIMESTAMPTZ,
  request_dispatched_at TIMESTAMPTZ,
  result_summary_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
  error_code            TEXT NOT NULL DEFAULT '',
  error_message         TEXT NOT NULL DEFAULT '',
  transport_uncertain   BOOLEAN NOT NULL DEFAULT FALSE,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  finished_at           TIMESTAMPTZ
);

CREATE TABLE task_payloads (
  task_id              TEXT PRIMARY KEY REFERENCES tasks(task_id) ON DELETE CASCADE,
  request_body_json    JSONB NOT NULL,
  forward_headers_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  auth_ciphertext      BYTEA NOT NULL,
  payload_expires_at   TIMESTAMPTZ NOT NULL,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_tasks_owner_created_at ON tasks(owner_hash, created_at DESC, task_id DESC);
CREATE INDEX idx_tasks_status_created_at ON tasks(status, created_at DESC);
CREATE INDEX idx_tasks_recovery_scan ON tasks(status, request_dispatched_at, heartbeat_at)
WHERE status IN ('accepted', 'queued', 'running');
CREATE INDEX idx_tasks_gc_finished_at ON tasks(finished_at)
WHERE status IN ('succeeded', 'failed', 'uncertain');
CREATE INDEX idx_task_payloads_gc_expires_at ON task_payloads(payload_expires_at);
