# Newapi Image Async Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 `async-gateway` 新增 `POST /v1/images/generations` 异步入口，并为 `rust-sync-proxy` 新增对应的同步改写路由，使 Newapi 图片请求可以异步受理、同步改写到真实上游、上传图片到图床或 R2，并通过既有 `/v1/tasks/*` 返回 `result` 风格结果。

**Architecture:** 保持 Gemini 链路完全不动，只做双提交协议并存。`async-gateway` 通过最小的任务协议标记与双成功态序列化复用既有任务系统；`rust-sync-proxy` 通过独立图片路由与独立改写 helper 复用现有上传和鉴权能力；Cloudflare task dashboard 仅补充双协议解析，不改代理缓存语义。

**Tech Stack:** Go 1.25、PostgreSQL migration、Rust + Axum + reqwest、React + TypeScript + Vite + Vitest

---

## File Structure

### `async-gateway`

- Create: `async-gateway/migrations/0002_request_protocol.up.sql`
  - 为 `tasks` 增加 `request_protocol` 列，默认 Gemini
- Create: `async-gateway/migrations/0002_request_protocol.down.sql`
  - 回滚 `request_protocol`
- Create: `async-gateway/internal/validation/image_generation_request.go`
  - Newapi 图片请求校验与标准化
- Create: `async-gateway/internal/validation/image_generation_request_test.go`
  - 新图片请求校验测试
- Create: `async-gateway/internal/httpapi/image_submit_handler.go`
  - 新图片提交 handler
- Create: `async-gateway/internal/httpapi/image_submit_handler_test.go`
  - 新图片提交 handler 测试
- Modify: `async-gateway/internal/domain/task.go`
  - 增加 `RequestProtocol` 与 `OpenAIImageResult`
- Modify: `async-gateway/internal/store/repository.go`
  - 持久化与读取 `request_protocol`
- Modify: `async-gateway/internal/store/repository_test.go`
  - repository SQL 与扫描测试
- Modify: `async-gateway/internal/httpapi/router.go`
  - 增加 `/v1/images/generations` 分发
- Modify: `async-gateway/internal/app/app.go`
  - 装配新 handler 与新的 draining gate
- Modify: `async-gateway/internal/app/lifecycle.go`
  - 支持同时 drain 两个 submit gate
- Modify: `async-gateway/internal/worker/summary.go`
  - 按协议解析 Gemini / Newapi 图片响应
- Modify: `async-gateway/internal/worker/summary_test.go`
  - Gemini / Newapi 结果摘要测试
- Modify: `async-gateway/internal/httpapi/query_handler.go`
  - Newapi 图片任务成功态输出 `result`
- Modify: `async-gateway/internal/httpapi/query_handler_test.go`
  - 双成功态查询测试
- Modify: `async-gateway/internal/cache/task_cache.go`
  - 深拷贝 `OpenAIImageResult`

### `rust-sync-proxy`

- Create: `rust-sync-proxy/src/openai_image.rs`
  - 新图片路由的请求标准化、usage 构造、响应改写
- Create: `rust-sync-proxy/tests/openai_image_test.rs`
  - 请求别名归一化、usage、`created` 回退、MIME 嗅探相关单测
- Modify: `rust-sync-proxy/src/lib.rs`
  - 导出新模块
- Modify: `rust-sync-proxy/src/http/router.rs`
  - 注册 `/v1/images/generations` 路由并接入 admin 日志
- Modify: `rust-sync-proxy/src/image_io.rs`
  - 增加字节级图片 MIME 嗅探 helper
- Modify: `rust-sync-proxy/tests/router_test.rs`
  - 验证新路由方法限制
- Modify: `rust-sync-proxy/tests/http_forwarding_test.rs`
  - 新图片路由上游转发与响应改写集成测试

### `cloudflare/task-dashboard`

- Modify: `cloudflare/task-dashboard/package.json`
  - 增加 `test` 脚本与 `vitest`
- Create: `cloudflare/task-dashboard/src/api/client.test.ts`
  - 双协议图片与 usage 提取测试
- Modify: `cloudflare/task-dashboard/src/api/client.ts`
  - 增加 `result` 类型与双协议提取函数
- Modify: `cloudflare/task-dashboard/src/components/TaskDetail.tsx`
  - 同时显示 `candidates` 和 `result`
- Modify: `cloudflare/task-dashboard/package-lock.json`
  - 锁定 `vitest` 依赖

## Task 1: Persist `request_protocol` In `async-gateway`

**Files:**
- Create: `async-gateway/migrations/0002_request_protocol.up.sql`
- Create: `async-gateway/migrations/0002_request_protocol.down.sql`
- Modify: `async-gateway/internal/domain/task.go`
- Modify: `async-gateway/internal/store/repository.go`
- Test: `async-gateway/internal/store/repository_test.go`

- [ ] **Step 1: Write the failing repository tests**

```go
func TestCreateAcceptedTaskIncludesRequestProtocol(t *testing.T) {
	t.Parallel()

	repo, mock := newRepositoryForTest(t)
	task, payload := makeAcceptedFixture(t)
	task.RequestProtocol = domain.RequestProtocolOpenAIImageGeneration

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(insertTaskSQL)).
		WithArgs(
			task.ID,
			task.Status,
			task.Model,
			task.OwnerHash,
			task.RequestProtocol,
			task.RequestPath,
			task.RequestQuery,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(regexp.QuoteMeta(insertTaskPayloadSQL)).
		WithArgs(payload.TaskID, payload.RequestBodyJSON, payload.ForwardHeaders, payload.AuthorizationCrypt, payload.ExpiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	if err := repo.CreateAcceptedTask(context.Background(), task, payload); err != nil {
		t.Fatalf("CreateAcceptedTask() error = %v", err)
	}
}

func TestGetTaskByIDLoadsRequestProtocol(t *testing.T) {
	t.Parallel()

	repo, mock := newRepositoryForTest(t)
	rows := mock.NewRows([]string{
		"task_id", "status", "model", "owner_hash", "request_protocol", "request_path", "request_query",
		"worker_id", "heartbeat_at", "request_dispatched_at", "result_summary_json",
		"error_code", "error_message", "transport_uncertain", "created_at", "updated_at", "finished_at",
	}).AddRow(
		"task-1", "queued", "gpt-image-2", "owner", "openai_image_generation", "/v1/images/generations", "",
		"", nil, nil, []byte(`{}`), "", "", false, time.Now(), time.Now(), nil,
	)

	mock.ExpectQuery(regexp.QuoteMeta(getTaskByIDSQL)).
		WithArgs("task-1").
		WillReturnRows(rows)

	task, err := repo.GetTaskByID(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("GetTaskByID() error = %v", err)
	}
	if task.RequestProtocol != domain.RequestProtocolOpenAIImageGeneration {
		t.Fatalf("RequestProtocol = %q", task.RequestProtocol)
	}
}
```

- [ ] **Step 2: Run the targeted store tests and confirm they fail**

Run:

```bash
cd async-gateway && go test ./internal/store -run 'TestCreateAcceptedTaskIncludesRequestProtocol|TestGetTaskByIDLoadsRequestProtocol' -count=1
```

Expected:

```text
FAIL
repository_test.go: expected query arguments to include request_protocol
repository.go: scan error because request_protocol column is missing
```

- [ ] **Step 3: Implement the minimal protocol persistence**

```sql
ALTER TABLE tasks
ADD COLUMN request_protocol TEXT NOT NULL
DEFAULT 'gemini_generate_content'
CHECK (request_protocol IN ('gemini_generate_content', 'openai_image_generation'));
```

```go
type RequestProtocol string

const (
	RequestProtocolGeminiGenerateContent RequestProtocol = "gemini_generate_content"
	RequestProtocolOpenAIImageGeneration RequestProtocol = "openai_image_generation"
)

type Task struct {
	ID              string
	Status          TaskStatus
	Model           string
	RequestProtocol RequestProtocol
	OwnerHash       string
	RequestPath     string
	RequestQuery    string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
```

```go
const insertTaskSQL = `
INSERT INTO tasks (
	task_id,
	status,
	model,
	owner_hash,
	request_protocol,
	request_path,
	request_query
) VALUES ($1, $2, $3, $4, $5, $6, $7)
`
```

```go
task.RequestProtocol = domain.RequestProtocol(readString(requestProtocol))
if task.RequestProtocol == "" {
	task.RequestProtocol = domain.RequestProtocolGeminiGenerateContent
}
```

- [ ] **Step 4: Re-run store tests**

Run:

```bash
cd async-gateway && go test ./internal/store -run 'TestCreateAcceptedTask|TestCreateAcceptedTaskIncludesRequestProtocol|TestGetTaskByID|TestGetTaskByIDLoadsRequestProtocol' -count=1
```

Expected:

```text
ok  	banana-async-gateway/internal/store
```

- [ ] **Step 5: Commit**

```bash
git add async-gateway/migrations/0002_request_protocol.up.sql async-gateway/migrations/0002_request_protocol.down.sql async-gateway/internal/domain/task.go async-gateway/internal/store/repository.go async-gateway/internal/store/repository_test.go
git commit -m "feat: persist async task request protocol"
```

## Task 2: Add The New `async-gateway` Submit Path

**Files:**
- Create: `async-gateway/internal/validation/image_generation_request.go`
- Create: `async-gateway/internal/validation/image_generation_request_test.go`
- Create: `async-gateway/internal/httpapi/image_submit_handler.go`
- Create: `async-gateway/internal/httpapi/image_submit_handler_test.go`
- Modify: `async-gateway/internal/httpapi/router.go`
- Modify: `async-gateway/internal/app/app.go`
- Modify: `async-gateway/internal/app/lifecycle.go`

- [ ] **Step 1: Write failing validator and handler tests**

```go
func TestValidateImageGenerationRequestNormalizesImageAliases(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{
		"model":"gpt-image-2",
		"prompt":"draw cat",
		"images":["https://img.example/a.png","https://img.example/b.png"]
	}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")

	validated, err := ValidateImageGenerationRequest(req)
	if err != nil {
		t.Fatalf("ValidateImageGenerationRequest() error = %v", err)
	}
	if validated.Model != "gpt-image-2" {
		t.Fatalf("Model = %q", validated.Model)
	}
	if !bytes.Contains(validated.RequestBodyJSON, []byte(`"reference_images"`)) {
		t.Fatalf("normalized body = %s", validated.RequestBodyJSON)
	}
	if bytes.Contains(validated.RequestBodyJSON, []byte(`"images"`)) {
		t.Fatalf("alias field leaked into normalized body = %s", validated.RequestBodyJSON)
	}
}

func TestImageSubmitAccepted(t *testing.T) {
	t.Parallel()

	repo := &submitRepositoryStub{}
	queue := &submitQueueStub{allowEnqueue: true}
	handler := newImageSubmitHandlerForTest(t, repo, queue)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{
		"model":"gpt-image-2",
		"prompt":"draw cat",
		"image":["https://img.example/a.png"]
	}`))
	req.Header.Set("Authorization", "Bearer sk-live")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}
	if repo.createdTask.RequestProtocol != domain.RequestProtocolOpenAIImageGeneration {
		t.Fatalf("RequestProtocol = %q", repo.createdTask.RequestProtocol)
	}
	if repo.createdTask.RequestPath != "/v1/images/generations" {
		t.Fatalf("RequestPath = %q", repo.createdTask.RequestPath)
	}
}
```

- [ ] **Step 2: Run the new validation and HTTP API tests**

Run:

```bash
cd async-gateway && go test ./internal/validation ./internal/httpapi -run 'TestValidateImageGenerationRequestNormalizesImageAliases|TestImageSubmitAccepted' -count=1
```

Expected:

```text
FAIL
validation/image_generation_request_test.go: undefined: ValidateImageGenerationRequest
httpapi/image_submit_handler_test.go: undefined: newImageSubmitHandlerForTest
```

- [ ] **Step 3: Implement the new validator, handler, router branch, and dual draining gates**

```go
func isImageGenerationPath(path string) bool {
	return path == "/v1/images/generations"
}
```

```go
type ValidatedImageGenerationRequest struct {
	Model               string
	AuthorizationHeader string
	RequestBodyJSON     []byte
	ReferenceImageCount int
}
```

```go
func normalizeReferenceImages(body map[string]any) ([]string, error) {
	for _, key := range []string{"reference_images", "images", "image"} {
		if raw, ok := body[key]; ok {
			list, ok := raw.([]any)
			if !ok {
				return nil, &RequestError{StatusCode: http.StatusBadRequest, Code: "invalid_reference_images", Message: key + " must be an array"}
			}
			urls := make([]string, 0, len(list))
			for _, item := range list {
				text, ok := item.(string)
				if !ok || !isAbsoluteHTTPURL(text) {
					return nil, &RequestError{StatusCode: http.StatusBadRequest, Code: "invalid_reference_image_url", Message: "reference image URL must be an absolute http/https URL"}
				}
				urls = append(urls, text)
			}
			delete(body, "image")
			delete(body, "images")
			body["reference_images"] = urls
			return urls, nil
		}
	}
	body["reference_images"] = []string{}
	return nil, nil
}
```

```go
task := &domain.Task{
	ID:              taskID,
	Status:          domain.TaskStatusAccepted,
	Model:           validated.Model,
	RequestProtocol: domain.RequestProtocolOpenAIImageGeneration,
	OwnerHash:       ownerHash,
	RequestPath:     r.URL.Path,
	RequestQuery:    r.URL.RawQuery,
}
```

```go
type App struct {
	cfg             config.Config
	logger          *log.Logger
	server          *http.Server
	geminiSubmitGate *DrainingSubmitHandler
	imageSubmitGate  *DrainingSubmitHandler
	workers         *worker.Pool
	repository      *store.Repository
}
```

```go
server: &http.Server{
	Addr: cfg.ListenAddr,
	Handler: httpapi.NewRouter(logger, httpapi.Handlers{
		SubmitTask:      geminiSubmitGate,
		ImageSubmitTask: imageSubmitGate,
		BatchGetTasks:   http.HandlerFunc(queryHandler.BatchGetTasks),
		GetTask:         http.HandlerFunc(queryHandler.GetTask),
		ListTasks:       http.HandlerFunc(queryHandler.ListTasks),
		TaskContent:     http.HandlerFunc(queryHandler.TaskContent),
	}),
}
```

- [ ] **Step 4: Re-run the focused `async-gateway` tests**

Run:

```bash
cd async-gateway && go test ./internal/validation ./internal/httpapi ./internal/app -run 'TestValidateImageGenerationRequest|TestImageSubmitAccepted|TestSubmitRejectsMissingAuthorization' -count=1
```

Expected:

```text
ok  	banana-async-gateway/internal/validation
ok  	banana-async-gateway/internal/httpapi
ok  	banana-async-gateway/internal/app
```

- [ ] **Step 5: Commit**

```bash
git add async-gateway/internal/validation/image_generation_request.go async-gateway/internal/validation/image_generation_request_test.go async-gateway/internal/httpapi/image_submit_handler.go async-gateway/internal/httpapi/image_submit_handler_test.go async-gateway/internal/httpapi/router.go async-gateway/internal/app/app.go async-gateway/internal/app/lifecycle.go
git commit -m "feat: add async newapi image submit endpoint"
```

## Task 3: Parse And Serialize Newapi Image Task Results

**Files:**
- Modify: `async-gateway/internal/domain/task.go`
- Modify: `async-gateway/internal/worker/summary.go`
- Modify: `async-gateway/internal/worker/summary_test.go`
- Modify: `async-gateway/internal/httpapi/query_handler.go`
- Modify: `async-gateway/internal/httpapi/query_handler_test.go`
- Modify: `async-gateway/internal/cache/task_cache.go`

- [ ] **Step 1: Write failing result-summary and query serialization tests**

```go
func TestExtractResultSummaryOpenAIImageSuccess(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"created": 1776663103,
		"data":[{"url":"https://img.example/final.png"}],
		"usage":{"total_tokens":2048}
	}`)

	summary, err := ExtractResultSummary(domain.RequestProtocolOpenAIImageGeneration, body)
	if err != nil {
		t.Fatalf("ExtractResultSummary() error = %v", err)
	}
	if summary.OpenAIImageResult == nil {
		t.Fatalf("OpenAIImageResult is nil")
	}
	if summary.OpenAIImageResult.Created != 1776663103 {
		t.Fatalf("Created = %d", summary.OpenAIImageResult.Created)
	}
	if len(summary.ImageURLs) != 1 || summary.ImageURLs[0] != "https://img.example/final.png" {
		t.Fatalf("ImageURLs = %#v", summary.ImageURLs)
	}
}

func TestBuildTaskResponseUsesResultForOpenAIImageTask(t *testing.T) {
	t.Parallel()

	task := &domain.Task{
		ID:              "task-openai",
		Status:          domain.TaskStatusSucceeded,
		Model:           "gpt-image-2",
		RequestProtocol: domain.RequestProtocolOpenAIImageGeneration,
		CreatedAt:       time.Unix(1776663000, 0).UTC(),
		ResultSummary: &domain.ResultSummary{
			ImageURLs: []string{"https://img.example/final.png"},
			OpenAIImageResult: &domain.OpenAIImageResult{
				Created: 1776663103,
				Data: []domain.OpenAIImageData{{URL: "https://img.example/final.png"}},
				Usage: map[string]any{"total_tokens": 2048},
			},
		},
	}

	response := buildTaskResponse(task)
	if _, ok := response["candidates"]; ok {
		t.Fatalf("unexpected candidates in openai response: %#v", response)
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result payload: %#v", response)
	}
	if result["created"] != int64(1776663103) {
		t.Fatalf("result.created = %#v", result["created"])
	}
}
```

- [ ] **Step 2: Run the worker and query tests to verify failure**

Run:

```bash
cd async-gateway && go test ./internal/worker ./internal/httpapi ./internal/cache -run 'TestExtractResultSummaryOpenAIImageSuccess|TestBuildTaskResponseUsesResultForOpenAIImageTask' -count=1
```

Expected:

```text
FAIL
worker/summary_test.go: too many arguments in call to ExtractResultSummary
httpapi/query_handler_test.go: summary.OpenAIImageResult undefined
```

- [ ] **Step 3: Implement protocol-aware parsing, `result` serialization, and cache cloning**

```go
type OpenAIImageData struct {
	URL string `json:"url"`
}

type OpenAIImageResult struct {
	Created int64            `json:"created"`
	Data    []OpenAIImageData `json:"data"`
	Usage   map[string]any   `json:"usage,omitempty"`
}

type ResultSummary struct {
	ImageURLs         []string           `json:"image_urls,omitempty"`
	FinishReason      string             `json:"finish_reason,omitempty"`
	UsageMetadata     map[string]any     `json:"usage_metadata,omitempty"`
	OpenAIImageResult *OpenAIImageResult `json:"openai_image_result,omitempty"`
}
```

```go
func ExtractResultSummary(protocol domain.RequestProtocol, body []byte) (*domain.ResultSummary, *SummaryError) {
	if protocol == domain.RequestProtocolOpenAIImageGeneration {
		return extractOpenAIImageResultSummary(body)
	}
	return extractGeminiResultSummary(body)
}
```

```go
func extractOpenAIImageResultSummary(body []byte) (*domain.ResultSummary, *SummaryError) {
	var envelope struct {
		Created int64 `json:"created"`
		Data    []struct {
			URL string `json:"url"`
		} `json:"data"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, &SummaryError{Code: "upstream_error", Message: err.Error()}
	}
	if len(envelope.Data) == 0 || strings.TrimSpace(envelope.Data[0].URL) == "" {
		return nil, &SummaryError{Code: "upstream_error", Message: noImageMessage}
	}
	data := make([]domain.OpenAIImageData, 0, len(envelope.Data))
	imageURLs := make([]string, 0, len(envelope.Data))
	for _, item := range envelope.Data {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		data = append(data, domain.OpenAIImageData{URL: item.URL})
		imageURLs = append(imageURLs, item.URL)
	}
	return &domain.ResultSummary{
		ImageURLs: imageURLs,
		OpenAIImageResult: &domain.OpenAIImageResult{
			Created: envelope.Created,
			Data:    data,
			Usage:   envelope.Usage,
		},
	}, nil
}
```

```go
if task.RequestProtocol == domain.RequestProtocolOpenAIImageGeneration && task.ResultSummary != nil && task.ResultSummary.OpenAIImageResult != nil {
	response["result"] = map[string]any{
		"created": task.ResultSummary.OpenAIImageResult.Created,
		"data":    task.ResultSummary.OpenAIImageResult.Data,
		"usage":   task.ResultSummary.OpenAIImageResult.Usage,
	}
	return response
}
```

- [ ] **Step 4: Re-run the focused result and query tests**

Run:

```bash
cd async-gateway && go test ./internal/worker ./internal/httpapi ./internal/cache -run 'TestExtractResultSummary|TestBuildTaskResponseUsesResultForOpenAIImageTask|TestBatchGetTasksSuccessPreservesOrderAndHidesForeignTasks' -count=1
```

Expected:

```text
ok  	banana-async-gateway/internal/worker
ok  	banana-async-gateway/internal/httpapi
ok  	banana-async-gateway/internal/cache
```

- [ ] **Step 5: Commit**

```bash
git add async-gateway/internal/domain/task.go async-gateway/internal/worker/summary.go async-gateway/internal/worker/summary_test.go async-gateway/internal/httpapi/query_handler.go async-gateway/internal/httpapi/query_handler_test.go async-gateway/internal/cache/task_cache.go
git commit -m "feat: expose openai image task results"
```

## Task 4: Add Request Normalization And Response Rewrite Helpers In `rust-sync-proxy`

**Files:**
- Create: `rust-sync-proxy/src/openai_image.rs`
- Create: `rust-sync-proxy/tests/openai_image_test.rs`
- Modify: `rust-sync-proxy/src/lib.rs`
- Modify: `rust-sync-proxy/src/image_io.rs`

- [ ] **Step 1: Write failing unit tests for request aliases, fixed usage, `created` fallback, and MIME sniffing**

```rust
#[test]
fn normalize_openai_image_request_uses_reference_images_alias() {
    let body = json!({
        "model": "gpt-image-2",
        "prompt": "draw cat",
        "images": ["https://img.example/a.png"],
    });

    let normalized = rust_sync_proxy::openai_image::normalize_request_body(body).unwrap();
    assert_eq!(normalized["reference_images"], json!(["https://img.example/a.png"]));
    assert_eq!(normalized["response_format"], "b64_json");
    assert!(normalized.get("images").is_none());
}

#[test]
fn build_openai_image_response_falls_back_created_timestamp() {
    let body = json!({
        "data": [{"b64_json": "iVBORw0KGgo="}]
    });

    let response = rust_sync_proxy::openai_image::build_response_payload(
        body,
        &[rust_sync_proxy::openai_image::UploadedImage {
            url: "https://img.example/final.png".to_string(),
        }],
        1_776_663_103,
    ).unwrap();

    assert_eq!(response["created"], 1_776_663_103);
    assert_eq!(response["usage"]["total_tokens"], 2048);
}

#[test]
fn sniff_image_mime_type_detects_png() {
    let mime = rust_sync_proxy::image_io::sniff_image_mime_type(&[137, 80, 78, 71, 13, 10, 26, 10]);
    assert_eq!(mime.as_deref(), Some("image/png"));
}
```

- [ ] **Step 2: Run the Rust helper tests and confirm failure**

Run:

```bash
cd rust-sync-proxy && cargo test --test openai_image_test
```

Expected:

```text
error[E0433]: could not find `openai_image` in `rust_sync_proxy`
error[E0425]: cannot find function `sniff_image_mime_type`
```

- [ ] **Step 3: Implement the helper module and byte sniffing**

```rust
pub fn normalize_request_body(mut body: Value) -> Result<Value> {
    let reference_images = take_reference_images(&mut body)?;
    body.as_object_mut()
        .ok_or_else(|| anyhow!("request body must be a json object"))?
        .insert("reference_images".to_string(), Value::Array(
            reference_images.into_iter().map(Value::String).collect()
        ));
    body.as_object_mut().unwrap().insert(
        "response_format".to_string(),
        Value::String("b64_json".to_string()),
    );
    Ok(body)
}
```

```rust
pub fn build_fixed_usage() -> Value {
    json!({
        "input_tokens": 1024,
        "input_tokens_details": {
            "image_tokens": 1000,
            "text_tokens": 24
        },
        "output_tokens": 1024,
        "total_tokens": 2048,
        "output_tokens_details": {
            "image_tokens": 1024,
            "text_tokens": 0
        }
    })
}
```

```rust
pub fn sniff_image_mime_type(bytes: &[u8]) -> Option<&'static str> {
    if bytes.starts_with(&[0x89, b'P', b'N', b'G', 0x0D, 0x0A, 0x1A, 0x0A]) {
        return Some("image/png");
    }
    if bytes.starts_with(&[0xFF, 0xD8, 0xFF]) {
        return Some("image/jpeg");
    }
    if bytes.len() >= 12 && &bytes[0..4] == b"RIFF" && &bytes[8..12] == b"WEBP" {
        return Some("image/webp");
    }
    if bytes.starts_with(b"GIF87a") || bytes.starts_with(b"GIF89a") {
        return Some("image/gif");
    }
    None
}
```

```rust
pub fn build_response_payload(
    upstream_body: Value,
    uploaded: &[UploadedImage],
    fallback_created: i64,
) -> Result<Value> {
    let created = upstream_body.get("created").and_then(Value::as_i64).unwrap_or(fallback_created);
    Ok(json!({
        "created": created,
        "data": uploaded,
        "usage": build_fixed_usage(),
    }))
}
```

- [ ] **Step 4: Re-run the focused Rust unit tests**

Run:

```bash
cd rust-sync-proxy && cargo test --test openai_image_test
```

Expected:

```text
test result: ok
```

- [ ] **Step 5: Commit**

```bash
git add rust-sync-proxy/src/openai_image.rs rust-sync-proxy/src/lib.rs rust-sync-proxy/src/image_io.rs rust-sync-proxy/tests/openai_image_test.rs
git commit -m "feat: add rust openai image rewrite helpers"
```

## Task 5: Wire The New Rust Route End-To-End

**Files:**
- Modify: `rust-sync-proxy/src/http/router.rs`
- Modify: `rust-sync-proxy/tests/router_test.rs`
- Modify: `rust-sync-proxy/tests/http_forwarding_test.rs`

- [ ] **Step 1: Write failing router and forwarding tests**

```rust
#[tokio::test]
async fn image_generations_route_accepts_post_only() {
    let app = rust_sync_proxy::build_router(rust_sync_proxy::test_config());
    let response = app
        .oneshot(
            Request::builder()
                .method(Method::GET)
                .uri("/v1/images/generations")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::METHOD_NOT_ALLOWED);
}

#[tokio::test]
async fn image_generations_forwards_reference_images_and_returns_uploaded_urls() {
    let state = TestState::default();
    let server = Router::new()
        .route("/v1/images/generations", post(mock_openai_image_generation))
        .route("/uguu", post(mock_legacy_upload))
        .with_state(state.clone());
    let server_addr = spawn_server(server).await;

    let mut config = rust_sync_proxy::test_config();
    config.upstream_base_url = format!("http://{server_addr}");
    config.upstream_api_key = "env-key".to_string();
    config.legacy_uguu_upload_url = format!("http://{server_addr}/uguu");
    let app = rust_sync_proxy::build_router(config);

    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/v1/images/generations")
                .header(CONTENT_TYPE, "application/json")
                .header(AUTHORIZATION, "Bearer http://127.0.0.1|real-upstream-key")
                .body(Body::from(json!({
                    "model": "gpt-image-2",
                    "prompt": "draw cat",
                    "image": ["https://img.example/a.png"],
                    "response_format": "url"
                }).to_string()))
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::OK);
    let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let json_body: Value = serde_json::from_slice(&body).unwrap();
    assert_eq!(json_body["data"][0]["url"], "https://img.example.com/final.png");
    assert_eq!(json_body["usage"]["total_tokens"], 2048);
}
```

- [ ] **Step 2: Run the route-level Rust tests**

Run:

```bash
cd rust-sync-proxy && cargo test --test router_test image_generations_route_accepts_post_only && cargo test --test http_forwarding_test image_generations_forwards_reference_images_and_returns_uploaded_urls
```

Expected:

```text
FAILED
assertion failed: left == right because GET /v1/images/generations is not registered
assertion failed: upstream request body still contains image instead of reference_images
```

- [ ] **Step 3: Implement the route and end-to-end rewrite flow**

```rust
Router::new()
    .route("/v1/images/generations", post(handle_openai_image_generation))
    .route("/v1beta/models/:model", post(proxy_generate_content))
```

```rust
async fn handle_openai_image_generation(
    State(state): State<AppState>,
    request: Request,
) -> Response {
    match crate::openai_image::proxy_openai_image_generation(state, request).await {
        Ok(response) => response.into_response(),
        Err(err) => build_structured_proxy_error_response(&err),
    }
}
```

```rust
let normalized_body = crate::openai_image::normalize_request_body(request_json)?;
let resolved_upstream = resolve_upstream_from_header_map(headers, &state.config.upstream_base_url, &state.config.upstream_api_key)?;
let upstream_url = format!("{}/v1/images/generations", resolved_upstream.base_url.trim_end_matches('/'));
let upstream_response = state.upstream_client
    .post(upstream_url)
    .header(AUTHORIZATION, format!("Bearer {}", resolved_upstream.api_key))
    .json(&normalized_body)
    .send()
    .await?;
```

```rust
let mime_type = crate::image_io::sniff_image_mime_type(&decoded_bytes).unwrap_or("image/png");
let uploaded = uploader.upload_image(&decoded_bytes, mime_type).await?;
```

- [ ] **Step 4: Re-run the route and forwarding tests**

Run:

```bash
cd rust-sync-proxy && cargo test --test router_test image_generations_route_accepts_post_only && cargo test --test http_forwarding_test image_generations_forwards_reference_images_and_returns_uploaded_urls
```

Expected:

```text
test result: ok
```

- [ ] **Step 5: Commit**

```bash
git add rust-sync-proxy/src/http/router.rs rust-sync-proxy/tests/router_test.rs rust-sync-proxy/tests/http_forwarding_test.rs
git commit -m "feat: wire rust openai image route"
```

## Task 6: Teach The Task Dashboard To Read `result`

**Files:**
- Modify: `cloudflare/task-dashboard/package.json`
- Modify: `cloudflare/task-dashboard/package-lock.json`
- Create: `cloudflare/task-dashboard/src/api/client.test.ts`
- Modify: `cloudflare/task-dashboard/src/api/client.ts`
- Modify: `cloudflare/task-dashboard/src/components/TaskDetail.tsx`

- [ ] **Step 1: Write failing task-dashboard extraction tests**

```ts
import { describe, expect, it } from "vitest";
import {
  extractImageURLs,
  extractUsageMetadata,
  type TaskDetailResponse,
} from "./client";

describe("task detail extractors", () => {
  it("reads Gemini image URLs from candidates", () => {
    const detail = {
      id: "task-gemini",
      object: "image.task",
      model: "gemini-3-pro-image-preview",
      status: "succeeded",
      created_at: 1776663000,
      candidates: [
        {
          content: {
            parts: [{ inlineData: { mimeType: "image/png", data: "https://img.example/gemini.png" } }],
          },
          finishReason: "STOP",
        },
      ],
    } satisfies TaskDetailResponse;

    expect(extractImageURLs(detail)).toEqual(["https://img.example/gemini.png"]);
  });

  it("reads OpenAI image URLs and usage from result", () => {
    const detail = {
      id: "task-openai",
      object: "image.task",
      model: "gpt-image-2",
      status: "succeeded",
      created_at: 1776663000,
      result: {
        created: 1776663103,
        data: [{ url: "https://img.example/openai.png" }],
        usage: { total_tokens: 2048 },
      },
    } satisfies TaskDetailResponse;

    expect(extractImageURLs(detail)).toEqual(["https://img.example/openai.png"]);
    expect(extractUsageMetadata(detail)).toEqual({ total_tokens: 2048 });
  });
});
```

- [ ] **Step 2: Run the dashboard tests and confirm failure**

Run:

```bash
cd cloudflare/task-dashboard && npm test -- src/api/client.test.ts
```

Expected:

```text
npm ERR! Missing script: "test"
src/api/client.test.ts: Property 'result' does not exist on type 'TaskDetailResponse'
```

- [ ] **Step 3: Add minimal Vitest support and dual-protocol extraction**

```json
{
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "test": "vitest run",
    "preview": "vite build && wrangler dev --config wrangler.toml",
    "deploy": "vite build && wrangler deploy --config wrangler.toml"
  },
  "devDependencies": {
    "vitest": "^2.1.8"
  }
}
```

```ts
export interface OpenAIImageResult {
  created: number;
  data: Array<{ url: string }>;
  usage?: Record<string, unknown>;
}

export interface TaskDetailResponse {
  id: string;
  object: string;
  model: string;
  status: string;
  created_at: number;
  finished_at?: number;
  response_id?: string;
  model_version?: string;
  usage_metadata?: Record<string, unknown>;
  candidates?: TaskDetailCandidate[];
  result?: OpenAIImageResult;
  error?: { code: string; message: string };
  transport_uncertain?: boolean;
}

export function extractImageURLs(detail: TaskDetailResponse): string[] {
  if (detail.result?.data?.length) {
    return detail.result.data.map((item) => item.url).filter(Boolean);
  }
  if (!detail.candidates) return [];
  const urls: string[] = [];
  for (const candidate of detail.candidates) {
    for (const part of candidate.content?.parts ?? []) {
      if (part.inlineData?.data) {
        urls.push(part.inlineData.data);
      }
    }
  }
  return urls;
}

export function extractUsageMetadata(detail: TaskDetailResponse): Record<string, unknown> | null {
  if (detail.result?.usage) return detail.result.usage;
  return detail.usage_metadata ?? null;
}
```

```tsx
const usageMetadata = extractUsageMetadata(detail);

{usageMetadata && (
  <MetadataPanel metadata={usageMetadata} />
)}
```

- [ ] **Step 4: Install the new test dependency and refresh the lockfile**

Run:

```bash
cd cloudflare/task-dashboard && npm install
```

Expected:

```text
added vitest and updated package-lock.json
```

- [ ] **Step 5: Re-run dashboard test and build verification**

Run:

```bash
cd cloudflare/task-dashboard && npm test -- src/api/client.test.ts && npm run build
```

Expected:

```text
✓ src/api/client.test.ts
✓ built in
```

- [ ] **Step 6: Commit**

```bash
git add cloudflare/task-dashboard/package.json cloudflare/task-dashboard/package-lock.json cloudflare/task-dashboard/src/api/client.ts cloudflare/task-dashboard/src/api/client.test.ts cloudflare/task-dashboard/src/components/TaskDetail.tsx
git commit -m "feat: support openai image task results in dashboard"
```

## Final Verification Batch

### Cross-project regression commands

- [ ] Run the full focused Go suite:

```bash
cd async-gateway && go test ./internal/store ./internal/validation ./internal/httpapi ./internal/worker ./internal/cache ./internal/app -count=1
```

- [ ] Run the full focused Rust suite:

```bash
cd rust-sync-proxy && cargo test
```

- [ ] Run the task dashboard checks:

```bash
cd cloudflare/task-dashboard && npm test && npm run build
```

- [ ] Run the final repo smoke checks before merge:

```bash
cd async-gateway && go test ./... -count=1
cd ../rust-sync-proxy && cargo test
```

Expected:

```text
All targeted test groups pass with no Gemini regressions and the new image route covered end-to-end.
```
