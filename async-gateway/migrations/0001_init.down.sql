DROP INDEX IF EXISTS idx_task_payloads_gc_expires_at;
DROP INDEX IF EXISTS idx_tasks_gc_finished_at;
DROP INDEX IF EXISTS idx_tasks_recovery_scan;
DROP INDEX IF EXISTS idx_tasks_status_created_at;
DROP INDEX IF EXISTS idx_tasks_owner_created_at;

DROP TABLE IF EXISTS task_payloads;
DROP TABLE IF EXISTS tasks;
