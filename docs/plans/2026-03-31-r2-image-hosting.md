# R2 Image Hosting Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 为现有 `output=url` 响应改写链路增加可配置的 R2 图片托管能力，支持 `legacy`、`r2`、`r2_then_legacy` 三种模式，并确保 R2 结果使用自定义公网域名直出。

**Architecture:** 保持当前 `handleNonStreamResponse`、`handleStreamResponse`、`convertInlineDataBase64ToUrlInResponse` 的主链路不变，只重构上传层。通过统一上传入口返回“最终 URL + provider 元信息”，把“上传去哪儿”和“是否包 `/proxy/image`”拆成两个独立决策。R2 上传使用 S3 兼容客户端接入 Cloudflare R2，并复用现有上传超时与 TLS 配置。

**Tech Stack:** Go 1.22、`net/http`、现有测试框架 `testing`、`httptest`、建议新增 AWS SDK v2 S3 兼容客户端（`github.com/aws/aws-sdk-go-v2/...`）用于 R2 PutObject。

---

### Task 1: 锁定配置模型与启动期校验

**Files:**
- Modify: `main.go`
- Test: `main_test.go`

**Step 1: Write the failing tests**

在 `main_test.go` 中新增配置解析与校验测试，至少覆盖以下场景：

```go
func TestLoadConfigWithEnv_DefaultImageHostModeIsLegacy(t *testing.T) {
	getenv := func(key string) string { return "" }
	cfg, err := loadConfigWithEnvValidated(getenv, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ImageHostMode != "legacy" {
		t.Fatalf("ImageHostMode=%q want legacy", cfg.ImageHostMode)
	}
}

func TestLoadConfigWithEnvValidated_R2ModeRequiresConfig(t *testing.T) {
	env := map[string]string{
		"IMAGE_HOST_MODE": "r2",
	}
	getenv := func(key string) string { return env[key] }
	_, err := loadConfigWithEnvValidated(getenv, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "R2_ENDPOINT") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigWithEnvValidated_RejectsInvalidImageHostMode(t *testing.T) {
	env := map[string]string{
		"IMAGE_HOST_MODE": "bad-mode",
	}
	getenv := func(key string) string { return env[key] }
	_, err := loadConfigWithEnvValidated(getenv, 0)
	if err == nil {
		t.Fatal("expected error")
	}
}
```

**Step 2: Run test to verify it fails**

Run:

```bash
go test -run 'TestLoadConfigWithEnv_(DefaultImageHostModeIsLegacy|R2ModeRequiresConfig|RejectsInvalidImageHostMode)' ./...
```

Expected:

```text
FAIL
undefined: loadConfigWithEnvValidated
cfg.ImageHostMode undefined
```

**Step 3: Write minimal implementation**

在 `main.go` 中完成以下最小改动：

1. 给 `Config` 新增字段：

```go
type Config struct {
	ImageHostMode       string
	R2Endpoint          string
	R2Bucket            string
	R2AccessKeyID       string
	R2SecretAccessKey   string
	R2PublicBaseURL     string
	R2ObjectPrefix      string
	// ...
}
```

2. 拆出带校验的配置加载函数：

```go
func loadConfig() Config {
	cfg, err := loadConfigWithEnvValidated(os.Getenv, detectContainerMemoryLimitBytes())
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	return cfg
}

func loadConfigWithEnvValidated(getenv func(string) string, containerLimitBytes int64) (Config, error) {
	cfg := loadConfigWithEnv(getenv, containerLimitBytes)
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
```

3. 实现最小校验逻辑：

```go
func validateConfig(cfg Config) error {
	switch cfg.ImageHostMode {
	case "", "legacy":
		return nil
	case "r2", "r2_then_legacy":
	default:
		return fmt.Errorf("IMAGE_HOST_MODE must be one of legacy, r2, r2_then_legacy")
	}
	if cfg.ImageHostMode == "" {
		cfg.ImageHostMode = "legacy"
	}
	if cfg.ImageHostMode == "legacy" {
		return nil
	}
	if strings.TrimSpace(cfg.R2Endpoint) == "" {
		return errors.New("R2_ENDPOINT is required when IMAGE_HOST_MODE is r2 or r2_then_legacy")
	}
	// 其余字段按同样风格继续校验
	return nil
}
```

4. 解析默认值：

```go
cfg.ImageHostMode = strings.TrimSpace(getenv("IMAGE_HOST_MODE"))
if cfg.ImageHostMode == "" {
	cfg.ImageHostMode = "legacy"
}
cfg.R2ObjectPrefix = strings.TrimSpace(getenv("R2_OBJECT_PREFIX"))
if cfg.R2ObjectPrefix == "" {
	cfg.R2ObjectPrefix = "images"
}
```

**Step 4: Run test to verify it passes**

Run:

```bash
go test -run 'TestLoadConfigWithEnv_(DefaultImageHostModeIsLegacy|R2ModeRequiresConfig|RejectsInvalidImageHostMode)' ./...
```

Expected:

```text
ok
```

**Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: validate r2 image host config"
```

### Task 2: 引入上传结果元信息与策略分发入口

**Files:**
- Modify: `main.go`
- Test: `main_test.go`

**Step 1: Write the failing tests**

在 `main_test.go` 中新增分发测试，避免一开始就耦合真实 R2 SDK：

```go
func TestUploadImageBytesToURL_LegacyModeUsesLegacyUploader(t *testing.T) {
	app := &App{
		Config: Config{ImageHostMode: "legacy"},
		legacyUploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			return uploadResult{URL: "https://legacy.example/a.png", Provider: "legacy"}, nil
		},
		r2UploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			t.Fatal("r2 uploader should not be called")
			return uploadResult{}, nil
		},
	}
	got, err := app.uploadImageBytesToURL([]byte("img"), "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Provider != "legacy" {
		t.Fatalf("Provider=%q want legacy", got.Provider)
	}
}

func TestUploadImageBytesToURL_R2ThenLegacyFallsBack(t *testing.T) {
	app := &App{
		Config: Config{ImageHostMode: "r2_then_legacy"},
		r2UploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			return uploadResult{}, errors.New("r2 down")
		},
		legacyUploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			return uploadResult{URL: "https://legacy.example/a.png", Provider: "legacy"}, nil
		},
	}
	got, err := app.uploadImageBytesToURL([]byte("img"), "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Provider != "legacy" {
		t.Fatalf("Provider=%q want legacy", got.Provider)
	}
}
```

**Step 2: Run test to verify it fails**

Run:

```bash
go test -run 'TestUploadImageBytesToURL_(LegacyModeUsesLegacyUploader|R2ThenLegacyFallsBack)' ./...
```

Expected:

```text
FAIL
undefined: uploadResult
app.uploadImageBytesToURL undefined
```

**Step 3: Write minimal implementation**

在 `main.go` 中引入上传结果对象和可替换的上传函数：

```go
type uploadResult struct {
	URL      string
	Provider string
}

type App struct {
	// ...
	legacyUploadFunc func(data []byte, mimeType string) (uploadResult, error)
	r2UploadFunc     func(data []byte, mimeType string) (uploadResult, error)
}
```

新增统一分发入口：

```go
func (app *App) uploadImageBytesToURL(data []byte, mimeType string) (uploadResult, error) {
	mode := strings.TrimSpace(app.Config.ImageHostMode)
	if mode == "" {
		mode = "legacy"
	}
	switch mode {
	case "legacy":
		return app.callLegacyUploader(data, mimeType)
	case "r2":
		return app.callR2Uploader(data, mimeType)
	case "r2_then_legacy":
		res, err := app.callR2Uploader(data, mimeType)
		if err == nil {
			return res, nil
		}
		log.Printf("[Image Upload Fallback] mode=%s provider=r2 err=%v", mode, err)
		return app.callLegacyUploader(data, mimeType)
	default:
		return uploadResult{}, fmt.Errorf("unsupported IMAGE_HOST_MODE %q", mode)
	}
}
```

保留现有 legacy 链路，但改名或包一层：

```go
func (app *App) callLegacyUploader(data []byte, mimeType string) (uploadResult, error) {
	if app.legacyUploadFunc != nil {
		return app.legacyUploadFunc(data, mimeType)
	}
	urlStr, err := app.uploadImageBytesToLegacyURL(data, mimeType)
	if err != nil {
		return uploadResult{}, err
	}
	return uploadResult{URL: urlStr, Provider: "legacy"}, nil
}
```

把当前 `uploadImageBytesToUrl` 重命名为 `uploadImageBytesToLegacyURL`。

**Step 4: Run test to verify it passes**

Run:

```bash
go test -run 'TestUploadImageBytesToURL_(LegacyModeUsesLegacyUploader|R2ThenLegacyFallsBack)' ./...
```

Expected:

```text
ok
```

**Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "refactor: add image host mode dispatcher"
```

### Task 3: 接入 Cloudflare R2 上传器

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `main.go`
- Test: `upload_streaming_test.go`
- Test: `main_test.go`

**Step 1: Write the failing tests**

先锁定 R2 结果格式和对象 key 规则，不在第一步测试真实 SDK 细节：

```go
func TestBuildR2ObjectKey_UsesDatePrefixAndExtension(t *testing.T) {
	now := time.Date(2026, 3, 31, 10, 20, 30, 0, time.UTC)
	key := buildR2ObjectKey("images", "image/png", now, "abcd1234")
	if key != "images/2026/03/31/1743416430000-abcd1234.png" {
		t.Fatalf("unexpected key: %s", key)
	}
}

func TestUploadToR2_ReturnsPublicURL(t *testing.T) {
	app := newTestAppForR2Upload()
	got, err := app.uploadToR2([]byte("png-bytes"), "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.URL != "https://img.example.com/images/2026/03/31/test.png" {
		t.Fatalf("unexpected url: %s", got.URL)
	}
	if got.Provider != "r2" {
		t.Fatalf("unexpected provider: %s", got.Provider)
	}
}
```

如果你想先避免时间依赖，把 key 生成函数做成可注入：

```go
type App struct {
	nowFunc        func() time.Time
	randomHexFunc  func(int) (string, error)
	r2PutObjectFunc func(ctx context.Context, key string, body []byte, mimeType string) error
}
```

**Step 2: Run test to verify it fails**

Run:

```bash
go test -run 'Test(BuildR2ObjectKey_UsesDatePrefixAndExtension|UploadToR2_ReturnsPublicURL)' ./...
```

Expected:

```text
FAIL
undefined: buildR2ObjectKey
app.uploadToR2 undefined
```

**Step 3: Write minimal implementation**

1. 在 `go.mod` 增加 AWS SDK v2 依赖：

```go
require (
	github.com/aws/aws-sdk-go-v2 v1.x.x
	github.com/aws/aws-sdk-go-v2/config v1.x.x
	github.com/aws/aws-sdk-go-v2/credentials v1.x.x
	github.com/aws/aws-sdk-go-v2/service/s3 v1.x.x
)
```

2. 新增 R2 对象 key 生成函数：

```go
func buildR2ObjectKey(prefix, mimeType string, now time.Time, randHex string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		prefix = "images"
	}
	ext := strings.TrimPrefix(extensionFromMime(mimeType), ".")
	if ext == "" {
		ext = "bin"
	}
	return fmt.Sprintf("%s/%04d/%02d/%02d/%d-%s.%s",
		prefix, now.Year(), now.Month(), now.Day(), now.UnixMilli(), randHex, ext)
}
```

3. 在 `newApp` 或 `main.go` 的初始化路径中创建 R2 客户端：

```go
cfg, err := awsconfig.LoadDefaultConfig(
	context.Background(),
	awsconfig.WithRegion("auto"),
	awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
		app.Config.R2AccessKeyID,
		app.Config.R2SecretAccessKey,
		"",
	)),
)
```

并配置 S3 BaseEndpoint 指向 `R2_ENDPOINT`。

4. 实现 `uploadToR2`：

```go
func (app *App) uploadToR2(data []byte, mimeType string) (uploadResult, error) {
	now := app.now()
	randHex, err := app.randomHex(4)
	if err != nil {
		return uploadResult{}, err
	}
	key := buildR2ObjectKey(app.Config.R2ObjectPrefix, mimeType, now, randHex)
	if err := app.putObjectToR2(context.Background(), key, data, mimeType); err != nil {
		return uploadResult{}, err
	}
	publicBase := strings.TrimRight(app.Config.R2PublicBaseURL, "/")
	return uploadResult{
		URL:      publicBase + "/" + key,
		Provider: "r2",
	}, nil
}
```

5. 复用现有上传超时与 TLS 配置，不新增一套 R2 专用配置。

**Step 4: Run test to verify it passes**

Run:

```bash
go test -run 'Test(BuildR2ObjectKey_UsesDatePrefixAndExtension|UploadToR2_ReturnsPublicURL)' ./...
```

Expected:

```text
ok
```

**Step 5: Commit**

```bash
git add go.mod go.sum main.go main_test.go upload_streaming_test.go
git commit -m "feat: add r2 image uploader"
```

### Task 4: 将 provider 感知接入响应改写与 URL 包装

**Files:**
- Modify: `main.go`
- Test: `main_test.go`

**Step 1: Write the failing tests**

锁定“仅 legacy 允许继续包代理”的语义：

```go
func TestConvertInlineDataBase64ToURLInResponse_R2ResultSkipsProxyWrap(t *testing.T) {
	app := &App{
		Config: Config{
			ImageHostMode:           "r2",
			PublicBaseURL:           "https://proxy.example.com",
			ProxyStandardOutputURLs: true,
		},
	}
	app.r2UploadFunc = func(data []byte, mimeType string) (uploadResult, error) {
		return uploadResult{
			URL:      "https://img.example.com/images/2026/03/31/a.png",
			Provider: "r2",
		}, nil
	}

	root := mustParseJSONMap(t, `{
		"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "http://localhost/v1beta/models/test:generateContent?output=url", nil)
	if err := app.convertInlineDataBase64ToUrlInResponse(root, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := extractFirstInlineDataData(t, root)
	if got != "https://img.example.com/images/2026/03/31/a.png" {
		t.Fatalf("unexpected data: %s", got)
	}
}

func TestConvertInlineDataBase64ToURLInResponse_LegacyResultStillWrapsProxy(t *testing.T) {
	// 与上例相同，但 legacy uploader 返回 legacy provider
}
```

**Step 2: Run test to verify it fails**

Run:

```bash
go test -run 'TestConvertInlineDataBase64ToURLInResponse_(R2ResultSkipsProxyWrap|LegacyResultStillWrapsProxy)' ./...
```

Expected:

```text
FAIL
got proxy wrapped url for r2 result
```

**Step 3: Write minimal implementation**

把 `convertInlineDataBase64ToUrlInResponse` 中的上传调用替换为新的统一入口：

```go
res, err := app.uploadImageBytesToURL(imageBytes, key.mimeType)
if err != nil {
	errCh <- err
	return
}

finalURL := res.URL
if res.Provider == "legacy" && app.Config.ProxyStandardOutputURLs {
	finalURL = app.maybeWrapProxyUrl(r, res.URL)
}
```

注意：

- 不能再直接调用旧的 `uploadImageBytesToLegacyURL`
- 不要让 `r2` 结果经过 `maybeWrapProxyUrl`
- `r2_then_legacy` 场景下应根据最终 `Provider` 动态决定

**Step 4: Run test to verify it passes**

Run:

```bash
go test -run 'TestConvertInlineDataBase64ToURLInResponse_(R2ResultSkipsProxyWrap|LegacyResultStillWrapsProxy)' ./...
```

Expected:

```text
ok
```

**Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: skip proxy wrapping for r2 image urls"
```

### Task 5: 补齐回归测试与文档

**Files:**
- Modify: `README.md`
- Modify: `main_test.go`
- Modify: `upload_streaming_test.go`

**Step 1: Write the failing tests**

补上最容易回归的端到端语义：

```go
func TestHandleNonStreamResponse_OutputURL_R2ThenLegacyFallbackUsesLegacyURLRules(t *testing.T) {
	// 构造 base64 图片响应
	// r2 uploader 返回错误
	// legacy uploader 返回 legacy URL
	// 断言最终响应使用 legacy URL，并按 ProxyStandardOutputURLs 规则包装
}

func TestHandleNonStreamResponse_OutputURL_R2FailureStillFailOpen(t *testing.T) {
	// r2 模式下上传失败
	// 断言响应保持 base64，不返回 5xx
}
```

**Step 2: Run test to verify it fails**

Run:

```bash
go test -run 'TestHandleNonStreamResponse_OutputURL_(R2ThenLegacyFallbackUsesLegacyURLRules|R2FailureStillFailOpen)' ./...
```

Expected:

```text
FAIL
unexpected wrapped url behavior
unexpected status or body
```

**Step 3: Write minimal implementation and docs**

1. 只做让测试通过所需的最小修正
2. 更新 `README.md`：
   - 新增 `IMAGE_HOST_MODE`
   - 新增全部 R2 相关环境变量说明
   - 明确写出：
     - `legacy` 保持现状
     - `r2` 结果直出
     - `r2_then_legacy` 先 R2 后 legacy

建议文档片段：

```md
#### `IMAGE_HOST_MODE`（默认 `legacy`）

- `legacy`：继续使用现有图床上传链路
- `r2`：仅上传到 R2
- `r2_then_legacy`：优先上传到 R2，失败后回退 legacy 图床

当最终 URL 来自 R2 时，将始终直出 `R2_PUBLIC_BASE_URL/<objectKey>`，
不会再套 `/proxy/image`。
```

**Step 4: Run full verification**

Run:

```bash
go test ./...
```

Expected:

```text
ok
```

再运行格式化：

```bash
gofmt -w main.go main_test.go upload_streaming_test.go
go test ./...
```

Expected:

```text
all tests pass within 60s
```

**Step 5: Commit**

```bash
git add README.md main.go main_test.go upload_streaming_test.go go.mod go.sum
git commit -m "feat: support r2 image hosting for output url"
```

### Task 6: 收尾检查

**Files:**
- Review: `docs/plans/2026-03-31-r2-image-hosting-design.md`
- Review: `README.md`
- Review: `main.go`
- Review: `main_test.go`

**Step 1: Review diff for scope creep**

Run:

```bash
git diff --stat HEAD~4..HEAD
git diff -- main.go README.md main_test.go upload_streaming_test.go go.mod go.sum
```

Expected:

```text
only config parsing, uploader dispatch, r2 upload, tests, and docs changes
```

**Step 2: Re-run focused regression tests**

Run:

```bash
go test -run 'TestHandleNonStreamResponse_OutputURL|TestUploadImageBytesToURL|TestLoadConfigWithEnv' ./...
```

Expected:

```text
ok
```

**Step 3: Sanity-check logs and defaults**

人工确认：

- 默认未配置 `IMAGE_HOST_MODE` 时为 `legacy`
- `legacy` 模式未要求 R2 配置
- `r2` / `r2_then_legacy` 缺配置时启动立即失败
- R2 上传日志不会泄露密钥

**Step 4: Final doc sync**

确认以下文档一致：

- `docs/plans/2026-03-31-r2-image-hosting-design.md`
- `docs/plans/2026-03-31-r2-image-hosting.md`
- `README.md`

**Step 5: Final commit if needed**

如果收尾阶段还有 README 或测试命名微调：

```bash
git add README.md main.go main_test.go upload_streaming_test.go docs/plans/2026-03-31-r2-image-hosting.md
git commit -m "docs: finalize r2 image hosting rollout notes"
```
