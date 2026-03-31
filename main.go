package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	jsoniter "github.com/json-iterator/go"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// json is a drop-in replacement for encoding/json backed by json-iterator.
// ConfigCompatibleWithStandardLibrary ensures identical marshaling behavior.
var json = jsoniter.ConfigCompatibleWithStandardLibrary

const (
	MaxInlineDataUrls                = 5
	MaxImageBytes                    = 10 * 1024 * 1024 // 10MB
	MaxConcurrentInlineDataFetches   = 4
	MaxSSEScanTokenBytes             = (MaxImageBytes*4)/3 + (2 * 1024 * 1024)
	ImageFetchTimeout                = 20 * time.Second
	DefaultImageTLSHandshakeTimeout  = 15 * time.Second
	ProxyPrewarmTimeout              = 15 * time.Second
	UploadTimeout                    = 10 * time.Second
	DefaultUploadTLSHandshakeTimeout = 10 * time.Second
	UploadRetries                    = 1
	UploadUserAgent                  = "ComfyUI-Banana/1.0"
	DefaultUpstreamBaseURL           = "https://magic666.top"
	ExternalImageFetchProxyPrefix    = "https://gemini.xinbaoai.com/proxy/image?url="
)

var markdownImageURLRe = regexp.MustCompile(`!\[[^\]]*\]\(\s*(https?://[^)\s]+)\s*\)`)
var proxyPrewarmSem = make(chan struct{}, MaxConcurrentInlineDataFetches)

// bufPool recycles bytes.Buffer across JSON marshal calls to reduce GC pressure.
var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// sseScannerBufPool recycles the large scanner buffer used per SSE response
// (~15 MiB each: base64 expansion 4/3 of MaxImageBytes + 2 MiB line buffer).
var sseScannerBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, MaxSSEScanTokenBytes)
		return &b
	},
}

// marshalJSON marshals v to JSON using a pooled buffer. The returned slice is a
// fresh copy and is safe to use after marshalJSON returns.
// On error, the returned slice is nil.
func marshalJSON(v any) ([]byte, error) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	err := json.NewEncoder(buf).Encode(v)
	var result []byte
	if err == nil {
		b := buf.Bytes()
		// json.Encoder appends a trailing newline; trim it to match json.Marshal output.
		if len(b) > 0 && b[len(b)-1] == '\n' {
			b = b[:len(b)-1]
		}
		result = make([]byte, len(b))
		copy(result, b)
	}
	bufPool.Put(buf)
	return result, err
}

type Config struct {
	UpstreamBaseURL          string
	UpstreamAPIKey           string
	PublicBaseURL            string
	Port                     string
	AllowedDomains           []string
	SlowLogThreshold         time.Duration
	ProxyStandardOutputURLs  bool
	ProxySpecialUpstreamURLs bool
	ImageHostMode            string
	R2Endpoint               string
	R2Bucket                 string
	R2AccessKeyID            string
	R2SecretAccessKey        string
	R2PublicBaseURL          string
	R2ObjectPrefix           string

	// Admin UI (optional; disabled by default)
	AdminPassword string

	// Network / TLS knobs (safe defaults; insecure options are opt-in).
	ImageFetchTimeout            time.Duration
	UploadTimeout                time.Duration
	ImageTLSHandshakeTimeout     time.Duration
	UploadTLSHandshakeTimeout    time.Duration
	ImageFetchInsecureSkipVerify bool
	UploadInsecureSkipVerify     bool

	// Optional cross-request disk cache for request-side inlineData URL fetching.
	// When InlineDataURLCacheDir is empty, the cache is disabled.
	InlineDataURLCacheDir      string
	InlineDataURLCacheTTL      time.Duration
	InlineDataURLCacheMaxBytes int64

	// Optional L1 in-memory LRU cache in front of the disk cache.
	// Zero or negative disables the memory cache.
	InlineDataURLMemCacheMaxBytes int64

	// Optional background fetch bridge for slow inlineData URL downloads.
	// Each request waits InlineDataURLBackgroundFetchWaitTimeout; on timeout, the
	// same download keeps running in background until InlineDataURLBackgroundFetchTotalTimeout.
	InlineDataURLBackgroundFetchWaitTimeout  time.Duration
	InlineDataURLBackgroundFetchTotalTimeout time.Duration
	InlineDataURLBackgroundFetchMaxInflight  int

	// When enabled, image downloads for matching hostnames will be fetched via an external proxy
	// to mitigate flaky TLS handshakes on some public object storages (e.g. OSS/CDN).
	// Patterns:
	// - example.com   => exact match
	// - .example.com  => suffix match (matches example.com and *.example.com)
	ImageFetchExternalProxyDomains []string
}

type App struct {
	Config                         Config
	UpstreamClient                 *http.Client
	ImageFetchClient               *http.Client
	ImageFetchBackgroundClient     *http.Client
	UploadClient                   *http.Client
	InlineDataURLCache             *inlineDataURLDiskCache
	InlineDataURLBackgroundFetcher *inlineDataBackgroundFetcher
	MemoryController               *memoryReliefController
	AdminLogs                      *adminLogBuffer
	AdminStats                     *adminStats
	legacyUploadFunc               func(data []byte, mimeType string) (uploadResult, error)
	r2UploadFunc                   func(data []byte, mimeType string) (uploadResult, error)
	r2PutObjectFunc                func(ctx context.Context, key string, body []byte, mimeType string) error
	nowFunc                        func() time.Time
	randomHexFunc                  func(n int) (string, error)
}

type uploadResult struct {
	URL      string
	Provider string
}

func newBaseTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   32,
		MaxConnsPerHost:       64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: false},
	}
}

func parseDurationMsEnv(key string, defaultValue time.Duration) time.Duration {
	return parseDurationMsEnvWith(os.Getenv, key, defaultValue)
}

func parseDurationMsEnvWith(getenv func(string) string, key string, defaultValue time.Duration) time.Duration {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return defaultValue
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return defaultValue
	}
	return time.Duration(ms) * time.Millisecond
}

func parseCommaSeparatedEnv(key string) []string {
	return parseCommaSeparatedEnvWith(os.Getenv, key)
}

func parseCommaSeparatedEnvWith(getenv func(string) string, key string) []string {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func hostnameMatchesDomainPatterns(hostname string, patterns []string) bool {
	h := strings.ToLower(strings.TrimSpace(hostname))
	if h == "" {
		return false
	}
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, ".") {
			if strings.HasSuffix(h, p) || h == p[1:] {
				return true
			}
			continue
		}
		if h == p {
			return true
		}
	}
	return false
}

func applyTLSOptionsToTransport(t *http.Transport, tlsHandshakeTimeout time.Duration, insecureSkipVerify bool) {
	if t == nil {
		return
	}
	if tlsHandshakeTimeout > 0 {
		t.TLSHandshakeTimeout = tlsHandshakeTimeout
	}
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{}
	}
	t.TLSClientConfig.InsecureSkipVerify = insecureSkipVerify
}

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

func validateConfig(cfg Config) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.ImageHostMode))
	switch mode {
	case "", "legacy":
		return nil
	case "r2", "r2_then_legacy":
	default:
		return fmt.Errorf("IMAGE_HOST_MODE must be one of legacy, r2, r2_then_legacy")
	}

	// r2 与 r2_then_legacy 模式要求提供完整 R2 基础配置。
	if strings.TrimSpace(cfg.R2Endpoint) == "" {
		return errors.New("R2_ENDPOINT is required when IMAGE_HOST_MODE is r2 or r2_then_legacy")
	}
	if strings.TrimSpace(cfg.R2Bucket) == "" {
		return errors.New("R2_BUCKET is required when IMAGE_HOST_MODE is r2 or r2_then_legacy")
	}
	if strings.TrimSpace(cfg.R2AccessKeyID) == "" {
		return errors.New("R2_ACCESS_KEY_ID is required when IMAGE_HOST_MODE is r2 or r2_then_legacy")
	}
	if strings.TrimSpace(cfg.R2SecretAccessKey) == "" {
		return errors.New("R2_SECRET_ACCESS_KEY is required when IMAGE_HOST_MODE is r2 or r2_then_legacy")
	}
	if strings.TrimSpace(cfg.R2PublicBaseURL) == "" {
		return errors.New("R2_PUBLIC_BASE_URL is required when IMAGE_HOST_MODE is r2 or r2_then_legacy")
	}
	return nil
}

func loadConfigWithEnv(getenv func(string) string, containerLimitBytes int64) Config {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Config{
		UpstreamBaseURL: getenv("UPSTREAM_BASE_URL"),
		UpstreamAPIKey:  getenv("UPSTREAM_API_KEY"),
		PublicBaseURL:   getenv("PUBLIC_BASE_URL"),
		Port:            getenv("PORT"),
		ImageHostMode:   strings.ToLower(strings.TrimSpace(getenv("IMAGE_HOST_MODE"))),
		R2Endpoint:      strings.TrimSpace(getenv("R2_ENDPOINT")),
		R2Bucket:        strings.TrimSpace(getenv("R2_BUCKET")),
		R2AccessKeyID:   strings.TrimSpace(getenv("R2_ACCESS_KEY_ID")),
		R2SecretAccessKey: strings.TrimSpace(
			getenv("R2_SECRET_ACCESS_KEY"),
		),
		R2PublicBaseURL: strings.TrimSpace(getenv("R2_PUBLIC_BASE_URL")),
		R2ObjectPrefix:  strings.Trim(strings.TrimSpace(getenv("R2_OBJECT_PREFIX")), "/"),
	}
	if cfg.ImageHostMode == "" {
		cfg.ImageHostMode = "legacy"
	}
	if cfg.R2ObjectPrefix == "" {
		cfg.R2ObjectPrefix = "images"
	}
	if cfg.UpstreamBaseURL == "" {
		cfg.UpstreamBaseURL = DefaultUpstreamBaseURL
	}
	// Validate Port
	if cfg.Port == "" || !isNumeric(cfg.Port) {
		log.Printf("Warning: Invalid or missing PORT environment variable (%q). Defaulting to 8787.", cfg.Port)
		cfg.Port = "8787"
	}

	// Allowed Proxy Domains
	if allowed := parseCommaSeparatedEnvWith(getenv, "ALLOWED_PROXY_DOMAINS"); len(allowed) > 0 {
		cfg.AllowedDomains = allowed
	} else {
		// Default Allowed Domains
		cfg.AllowedDomains = []string{
			"ai.kefan.cn",
			"uguu.se",
			".uguu.se",
			".aitohumanize.com",
		}
	}

	// External image fetch proxy domains (optional; empty => disabled)
	cfg.ImageFetchExternalProxyDomains = parseCommaSeparatedEnvWith(getenv, "IMAGE_FETCH_EXTERNAL_PROXY_DOMAINS")

	// Slow log threshold (ms). Default: 100s
	// Set SLOW_LOG_THRESHOLD_MS=0 or negative to disable slow logs.
	cfg.SlowLogThreshold = 100 * time.Second
	if raw := strings.TrimSpace(getenv("SLOW_LOG_THRESHOLD_MS")); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil {
			if ms <= 0 {
				cfg.SlowLogThreshold = 0
			} else {
				cfg.SlowLogThreshold = time.Duration(ms) * time.Millisecond
			}
		}
	}

	// Proxy wrapper toggles (default enabled to keep current behavior)
	cfg.ProxyStandardOutputURLs = parseBoolEnvWith(getenv, "PROXY_STANDARD_OUTPUT_URLS", true)
	cfg.ProxySpecialUpstreamURLs = parseBoolEnvWith(getenv, "PROXY_SPECIAL_UPSTREAM_URLS", true)

	cfg.AdminPassword = strings.TrimSpace(getenv("ADMIN_PASSWORD"))
	if isDisabledValue(cfg.AdminPassword) {
		cfg.AdminPassword = ""
	}

	// Image fetch / upload networking knobs
	cfg.ImageFetchTimeout = parseDurationMsEnvWith(getenv, "IMAGE_FETCH_TIMEOUT_MS", ImageFetchTimeout)
	cfg.UploadTimeout = parseDurationMsEnvWith(getenv, "UPLOAD_TIMEOUT_MS", UploadTimeout)
	cfg.ImageTLSHandshakeTimeout = parseDurationMsEnvWith(getenv, "IMAGE_TLS_HANDSHAKE_TIMEOUT_MS", DefaultImageTLSHandshakeTimeout)
	cfg.UploadTLSHandshakeTimeout = parseDurationMsEnvWith(getenv, "UPLOAD_TLS_HANDSHAKE_TIMEOUT_MS", DefaultUploadTLSHandshakeTimeout)
	cfg.ImageFetchInsecureSkipVerify = parseBoolEnvWith(getenv, "IMAGE_FETCH_INSECURE_SKIP_VERIFY", false)
	cfg.UploadInsecureSkipVerify = parseBoolEnvWith(getenv, "UPLOAD_INSECURE_SKIP_VERIFY", false)

	// Request-side inlineData URL disk cache (disabled by default)
	cfg.InlineDataURLCacheDir = strings.TrimSpace(getenv("INLINE_DATA_URL_CACHE_DIR"))
	if isDisabledValue(cfg.InlineDataURLCacheDir) {
		cfg.InlineDataURLCacheDir = ""
	}

	cfg.InlineDataURLCacheTTL = 1 * time.Hour
	if raw := strings.TrimSpace(getenv("INLINE_DATA_URL_CACHE_TTL_MS")); raw != "" {
		if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if ms <= 0 {
				cfg.InlineDataURLCacheTTL = 0
			} else {
				cfg.InlineDataURLCacheTTL = time.Duration(ms) * time.Millisecond
			}
		}
	}

	cfg.InlineDataURLCacheMaxBytes = 1 << 30 // 1GiB
	if raw := strings.TrimSpace(getenv("INLINE_DATA_URL_CACHE_MAX_BYTES")); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if v <= 0 {
				cfg.InlineDataURLCacheMaxBytes = 0
			} else {
				cfg.InlineDataURLCacheMaxBytes = v
			}
		}
	}

	// L1 memory cache for inlineData URL fetches.
	cfg.InlineDataURLMemCacheMaxBytes = autoInlineDataMemCacheMaxBytes(containerLimitBytes)
	if raw := strings.TrimSpace(getenv("INLINE_DATA_URL_MEMORY_CACHE_MAX_BYTES")); raw != "" {
		if isDisabledValue(raw) {
			cfg.InlineDataURLMemCacheMaxBytes = 0
		} else if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if v <= 0 {
				cfg.InlineDataURLMemCacheMaxBytes = 0
			} else {
				cfg.InlineDataURLMemCacheMaxBytes = v
			}
		}
	}

	// Background fetch bridge for slow inlineData URL downloads.
	cfg.InlineDataURLBackgroundFetchWaitTimeout = cfg.ImageFetchTimeout
	if raw := strings.TrimSpace(getenv("INLINE_DATA_URL_BACKGROUND_FETCH_WAIT_TIMEOUT_MS")); raw != "" {
		if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if ms <= 0 {
				cfg.InlineDataURLBackgroundFetchWaitTimeout = 0
			} else {
				cfg.InlineDataURLBackgroundFetchWaitTimeout = time.Duration(ms) * time.Millisecond
			}
		}
	}

	cfg.InlineDataURLBackgroundFetchTotalTimeout = 90 * time.Second
	if raw := strings.TrimSpace(getenv("INLINE_DATA_URL_BACKGROUND_FETCH_TOTAL_TIMEOUT_MS")); raw != "" {
		if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if ms <= 0 {
				cfg.InlineDataURLBackgroundFetchTotalTimeout = 0
			} else {
				cfg.InlineDataURLBackgroundFetchTotalTimeout = time.Duration(ms) * time.Millisecond
			}
		}
	}

	cfg.InlineDataURLBackgroundFetchMaxInflight = 128
	if raw := strings.TrimSpace(getenv("INLINE_DATA_URL_BACKGROUND_FETCH_MAX_INFLIGHT")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			if n <= 0 {
				cfg.InlineDataURLBackgroundFetchMaxInflight = 0
			} else {
				cfg.InlineDataURLBackgroundFetchMaxInflight = n
			}
		}
	}

	return cfg
}

func isDisabledValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "n", "off", "disable", "disabled", "none":
		return true
	default:
		return false
	}
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func parseBoolEnv(key string, defaultValue bool) bool {
	return parseBoolEnvWith(os.Getenv, key, defaultValue)
}

func parseBoolEnvWith(getenv func(string) string, key string, defaultValue bool) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return defaultValue
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "y", "on", "enable", "enabled":
		return true
	case "0", "false", "no", "n", "off", "disable", "disabled", "none":
		return false
	default:
		return defaultValue
	}
}

func main() {
	containerLimitBytes := detectContainerMemoryLimitBytes()
	runtimeTuning := configureRuntimeMemory(os.Getenv, containerLimitBytes, debug.SetGCPercent, debug.SetMemoryLimit)
	cfg, err := loadConfigWithEnvValidated(os.Getenv, containerLimitBytes)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	baseTransport := newBaseTransport()
	upstreamTransport := baseTransport.Clone()
	imageTransport := baseTransport.Clone()
	imageBackgroundTransport := baseTransport.Clone()
	uploadTransport := baseTransport.Clone()

	// Image fetch/upload often hits public object storage/CDN; default TLS handshake timeout is a bit more tolerant.
	applyTLSOptionsToTransport(imageTransport, cfg.ImageTLSHandshakeTimeout, cfg.ImageFetchInsecureSkipVerify)
	applyTLSOptionsToTransport(imageBackgroundTransport, cfg.ImageTLSHandshakeTimeout, cfg.ImageFetchInsecureSkipVerify)
	applyTLSOptionsToTransport(uploadTransport, cfg.UploadTLSHandshakeTimeout, cfg.UploadInsecureSkipVerify)

	backgroundFetchClientTimeout := cfg.ImageFetchTimeout
	if cfg.InlineDataURLBackgroundFetchTotalTimeout > backgroundFetchClientTimeout {
		backgroundFetchClientTimeout = cfg.InlineDataURLBackgroundFetchTotalTimeout
	}
	if backgroundFetchClientTimeout <= 0 {
		backgroundFetchClientTimeout = ImageFetchTimeout
	}

	app := &App{
		Config: cfg,
		UpstreamClient: &http.Client{
			Timeout:   600 * time.Second, // Global timeout for upstream calls
			Transport: upstreamTransport,
		},
		ImageFetchClient: &http.Client{
			Timeout:   cfg.ImageFetchTimeout,
			Transport: imageTransport,
		},
		ImageFetchBackgroundClient: &http.Client{
			Timeout:   backgroundFetchClientTimeout,
			Transport: imageBackgroundTransport,
		},
		UploadClient: &http.Client{
			Timeout:   cfg.UploadTimeout,
			Transport: uploadTransport,
		},
		MemoryController: newMemoryReliefController(),
	}

	if cfg.ImageHostMode != "legacy" {
		putObjectFunc, err := newR2PutObjectFunc(cfg, app.UploadClient)
		if err != nil {
			log.Fatalf("init r2 uploader: %v", err)
		}
		app.r2PutObjectFunc = putObjectFunc
	}

	if cfg.InlineDataURLCacheDir != "" && cfg.InlineDataURLCacheTTL > 0 && cfg.InlineDataURLCacheMaxBytes > 0 {
		cache, err := newInlineDataURLDiskCache(cfg.InlineDataURLCacheDir, cfg.InlineDataURLCacheTTL, cfg.InlineDataURLCacheMaxBytes)
		if err != nil {
			log.Printf("WARNING: InlineData URL cache disabled: %v", err)
		} else {
			if cfg.InlineDataURLMemCacheMaxBytes > 0 {
				cache.memCache = newInlineDataURLMemCache(cfg.InlineDataURLMemCacheMaxBytes)
			}
			app.InlineDataURLCache = cache
		}
	}
	if cfg.InlineDataURLBackgroundFetchTotalTimeout > 0 && cfg.InlineDataURLBackgroundFetchMaxInflight > 0 {
		backgroundFetcher, err := newInlineDataBackgroundFetcher(
			cfg.InlineDataURLBackgroundFetchTotalTimeout,
			cfg.InlineDataURLBackgroundFetchMaxInflight,
		)
		if err != nil {
			log.Printf("WARNING: InlineData URL background fetch disabled: %v", err)
		} else {
			app.InlineDataURLBackgroundFetcher = backgroundFetcher
		}
	}
	if cfg.AdminPassword != "" {
		app.AdminLogs = newAdminLogBuffer(100)
		app.AdminStats = &adminStats{}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.Handler)

	log.Printf("Starting Gemini Worker (Go) on port %s...", cfg.Port)
	log.Printf("Upstream Base URL: %s", cfg.UpstreamBaseURL)
	if cfg.PublicBaseURL != "" {
		log.Printf("Public Base URL: %s", cfg.PublicBaseURL)
	}
	if cfg.SlowLogThreshold > 0 {
		log.Printf("Slow Log Threshold: %v", cfg.SlowLogThreshold)
	} else {
		log.Printf("Slow Log Threshold: disabled")
	}
	if cfg.ProxyStandardOutputURLs {
		log.Printf("Proxy Standard Output URLs: enabled")
	} else {
		log.Printf("Proxy Standard Output URLs: disabled")
	}
	if cfg.ProxySpecialUpstreamURLs {
		log.Printf("Proxy Special Upstream URLs: enabled")
	} else {
		log.Printf("Proxy Special Upstream URLs: disabled")
	}
	log.Printf("Image Fetch Timeout: %v", cfg.ImageFetchTimeout)
	log.Printf("Image TLS Handshake Timeout: %v", cfg.ImageTLSHandshakeTimeout)
	if cfg.ImageFetchInsecureSkipVerify {
		log.Printf("WARNING: IMAGE_FETCH_INSECURE_SKIP_VERIFY is enabled. Image download TLS certificate verification is disabled.")
	}
	if len(cfg.ImageFetchExternalProxyDomains) > 0 {
		log.Printf("Image Fetch External Proxy Domains: %s (via %s<escaped-url>)", strings.Join(cfg.ImageFetchExternalProxyDomains, ","), ExternalImageFetchProxyPrefix)
	} else {
		log.Printf("Image Fetch External Proxy Domains: disabled")
	}
	log.Printf("Upload Timeout: %v", cfg.UploadTimeout)
	log.Printf("Upload TLS Handshake Timeout: %v", cfg.UploadTLSHandshakeTimeout)
	if cfg.UploadInsecureSkipVerify {
		log.Printf("WARNING: UPLOAD_INSECURE_SKIP_VERIFY is enabled. Upload TLS certificate verification is disabled.")
	}
	if cfg.InlineDataURLCacheDir != "" && cfg.InlineDataURLCacheTTL > 0 && cfg.InlineDataURLCacheMaxBytes > 0 {
		log.Printf("InlineData URL Cache: disk L2 enabled dir=%s ttl=%v maxBytes=%d", cfg.InlineDataURLCacheDir, cfg.InlineDataURLCacheTTL, cfg.InlineDataURLCacheMaxBytes)
		if cfg.InlineDataURLMemCacheMaxBytes > 0 {
			log.Printf("InlineData URL Cache: memory L1 enabled maxBytes=%d", cfg.InlineDataURLMemCacheMaxBytes)
		}
	} else {
		log.Printf("InlineData URL Cache: disabled")
	}
	if runtimeTuning.AutoMemoryLimit {
		log.Printf("Go Runtime Memory Limit: auto=%d bytes (container=%d bytes)", runtimeTuning.MemoryLimitBytes, runtimeTuning.ContainerLimitBytes)
	}
	if runtimeTuning.AutoGCPercent {
		log.Printf("Go Runtime GC Percent: auto=%d", runtimeTuning.GCPercent)
	}
	if app.InlineDataURLBackgroundFetcher != nil {
		log.Printf(
			"InlineData URL Background Fetch: enabled waitTimeout=%v totalTimeout=%v maxInflight=%d",
			cfg.InlineDataURLBackgroundFetchWaitTimeout,
			cfg.InlineDataURLBackgroundFetchTotalTimeout,
			cfg.InlineDataURLBackgroundFetchMaxInflight,
		)
	} else {
		log.Printf("InlineData URL Background Fetch: disabled")
	}
	if cfg.AdminPassword != "" {
		log.Printf("Admin UI: enabled (/admin)")
	} else {
		log.Printf("Admin UI: disabled")
	}

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: mux,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func (app *App) Handler(w http.ResponseWriter, r *http.Request) {
	// Simple Logging
	start := time.Now()
	defer func() {
		log.Printf("%s %s %d %v", r.Method, r.URL.Path, 200, time.Since(start)) // Status code logic simplified for log
	}()

	path := r.URL.Path
	if strings.HasPrefix(path, "/admin") {
		app.handleAdmin(w, r)
		return
	}
	if path == "/proxy/image" && r.Method == "GET" {
		app.handleProxyImage(w, r)
		return
	}

	if strings.HasPrefix(path, "/v1beta/models/") && strings.HasSuffix(path, ":generateContent") {
		app.handleGeminiRequest(w, r, false)
		return
	}
	if strings.HasPrefix(path, "/v1beta/models/") && strings.HasSuffix(path, ":streamGenerateContent") {
		app.handleGeminiRequest(w, r, true)
		return
	}

	http.Error(w, "Not Found", http.StatusNotFound)
}

// --- Proxy Logic ---

func (app *App) handleProxyImage(w http.ResponseWriter, r *http.Request) {
	targetUrl := strings.TrimSpace(r.URL.Query().Get("url"))
	if targetUrl == "" {
		encoded := strings.TrimSpace(r.URL.Query().Get("u"))
		if encoded == "" {
			http.Error(w, "Missing url param", http.StatusBadRequest)
			return
		}

		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(raw) == 0 {
			http.Error(w, "Invalid u param", http.StatusBadRequest)
			return
		}
		targetUrl = string(raw)
	}

	u, err := url.Parse(targetUrl)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		http.Error(w, "Forbidden proxy target", http.StatusForbidden)
		return
	}
	if isForbiddenFetchTarget(u) || !app.isAllowedProxyTarget(u) {
		http.Error(w, "Forbidden proxy target", http.StatusForbidden)
		return
	}

	fetchURL := targetUrl
	if hostnameMatchesDomainPatterns(u.Hostname(), app.Config.ImageFetchExternalProxyDomains) {
		fetchURL = ExternalImageFetchProxyPrefix + url.QueryEscape(targetUrl)
	}

	req, err := http.NewRequest("GET", fetchURL, nil)
	if err != nil {
		http.Error(w, "Proxy request build failed", http.StatusInternalServerError)
		return
	}
	setImageFetchHeaders(req)

	resp, err := app.ImageFetchClient.Do(req)
	if err != nil {
		http.Error(w, "Proxy fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (app *App) isAllowedProxyTarget(u *url.URL) bool {
	return hostnameMatchesDomainPatterns(u.Hostname(), app.Config.AllowedDomains)
}

// --- Gemini Handler ---

func (app *App) handleGeminiRequest(w http.ResponseWriter, r *http.Request, isStream bool) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	start := time.Now()

	var adminEntry *adminLogEntry
	if app != nil && app.AdminLogs != nil {
		adminEntry = &adminLogEntry{
			CreatedAt:  start,
			Method:     r.Method,
			Path:       r.URL.Path,
			Query:      r.URL.RawQuery,
			RemoteAddr: r.RemoteAddr,
			IsStream:   isStream,
		}
		defer func() {
			adminEntry.DurationMs = time.Since(start).Milliseconds()
			app.AdminLogs.Add(*adminEntry)
		}()
	}
	if app != nil && app.AdminStats != nil {
		defer func() {
			durationMs := time.Since(start).Milliseconds()
			app.AdminStats.totalRequests.Add(1)
			if adminEntry != nil && adminEntry.StatusCode >= 400 {
				app.AdminStats.errorRequests.Add(1)
			}
			app.AdminStats.totalDurationMs.Add(durationMs)
		}()
	}

	// 1. Parse Upstream Config
	upstreamBase := app.Config.UpstreamBaseURL
	upstreamKey := app.Config.UpstreamAPIKey

	// Override from Environment or Custom Headers if needed (simplified for Go version to stick to env or basic auth)
	// For full compatibility with Node.js version's complex auth parsing:
	akHeader := r.Header.Get("x-goog-api-key")
	authHeader := r.Header.Get("Authorization")

	// Simplified Auth Logic: Prefer Header > Env
	var token string
	if akHeader != "" {
		token = akHeader
	} else if strings.HasPrefix(authHeader, "Bearer ") {
		token = strings.TrimPrefix(authHeader, "Bearer ")
	}

	if token != "" {
		// Check for "baseUrl|apiKey" format
		if idx := strings.Index(token, "|"); idx != -1 {
			customBase := strings.TrimSpace(token[:idx])
			realKey := strings.TrimSpace(token[idx+1:])

			if customBase != "" {
				upstreamBase = customBase
			}
			if realKey != "" {
				upstreamKey = realKey
			}
		} else {
			upstreamKey = token
		}
	}

	if upstreamKey == "" {
		body := geminiError(w, 401, "Missing upstream apiKey")
		if adminEntry != nil {
			adminEntry.StatusCode = 401
			adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(body)
		}
		return
	}

	// 2. Parse Body
	bodyReadStart := time.Now()
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		body := geminiError(w, 400, "Failed to read body")
		if adminEntry != nil {
			adminEntry.StatusCode = 400
			adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(body)
		}
		return
	}
	r.Body.Close()
	bodyReadDur := time.Since(bodyReadStart)

	maybeHasInlineData := bytes.Contains(bodyBytes, []byte("\"inlineData\""))
	if adminEntry != nil {
		adminEntry.RequestRaw, adminEntry.RequestRawImages = sanitizeJSONForAdminLog(bodyBytes)
	}

	var bodyMap map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &bodyMap); err != nil {
		body := geminiError(w, 400, "Invalid JSON body")
		if adminEntry != nil {
			adminEntry.StatusCode = 400
			adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(body)
		}
		return
	}

	// 3. Output Preference
	queryOutput := r.URL.Query().Get("output")
	outputMode := getOutputMode(queryOutput, bodyMap)
	if adminEntry != nil {
		adminEntry.OutputMode = outputMode
	}

	// Remove 'output' from body to prevent upstream error
	stripOutputFromMap(bodyMap)

	// 4. Process Inline Data (Fetch URLs -> Base64)
	inlineDataStart := time.Now()
	if maybeHasInlineData {
		var cacheHitMu sync.Mutex
		cacheHits := make(map[string]struct{})
		var observer func(rawURL string, fromCache bool)
		if adminEntry != nil || (app != nil && app.AdminStats != nil) {
			observer = func(rawURL string, fromCache bool) {
				if !fromCache {
					return
				}
				if adminEntry != nil {
					cacheHitMu.Lock()
					cacheHits[rawURL] = struct{}{}
					cacheHitMu.Unlock()
				}
				if app != nil && app.AdminStats != nil {
					app.AdminStats.cacheHits.Add(1)
				}
			}
		}

		var err error
		if observer != nil {
			err = app.convertRequestInlineDataUrlsToBase64WithObserver(bodyMap, observer)
		} else {
			err = app.convertRequestInlineDataUrlsToBase64(bodyMap)
		}
		if err != nil {
			log.Printf("inlineData URL 处理失败: %v", err)
			msg := inlineDataUrlUserFacingErrorMessage(err)
			var waitErr *inlineDataBackgroundWaitTimeoutError
			if errors.As(err, &waitErr) {
				msg = "inlineData image URL is still downloading in background, please retry shortly"
			}
			body := geminiError(w, 502, msg)
			if adminEntry != nil {
				adminEntry.StatusCode = 502
				adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(body)
			}
			return
		}
		if adminEntry != nil {
			cacheHitMu.Lock()
			adminEntry.RequestRawImageCacheHits = sortedKeys(cacheHits)
			cacheHitMu.Unlock()
		}
	}
	inlineDataDur := time.Since(inlineDataStart)

	// 5. Build Upstream Request
	newBodyBytes, marshalErr := marshalJSON(bodyMap)
	if marshalErr != nil {
		log.Printf("[marshalJSON] failed to marshal upstream body: %v", marshalErr)
		newBodyBytes = bodyBytes // fallback: forward original body unchanged
	}
	if adminEntry != nil {
		adminEntry.RequestUpstream, adminEntry.RequestUpstreamImgs = sanitizeJSONForAdminLog(newBodyBytes)
	}
	bodyBytes = nil
	bodyMap = nil

	// Construct Upstream URL
	targetPath := r.URL.Path
	targetQuery := r.URL.Query()
	targetQuery.Del("output") // Strip output param

	upstreamTarget := fmt.Sprintf("%s%s", strings.TrimRight(upstreamBase, "/"), targetPath)
	if len(targetQuery) > 0 {
		upstreamTarget += "?" + targetQuery.Encode()
	}

	req, err := http.NewRequest("POST", upstreamTarget, bytes.NewReader(newBodyBytes))
	if err != nil {
		body := geminiError(w, 500, "Failed to create upstream request")
		if adminEntry != nil {
			adminEntry.StatusCode = 500
			adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(body)
		}
		return
	}

	// Copy Headers
	copyUpstreamHeaders(req.Header, r.Header, upstreamKey)

	// 6. Send to Upstream
	upstreamStart := time.Now()
	resp, err := app.UpstreamClient.Do(req)
	if err != nil {
		body := geminiError(w, 502, "Failed to reach upstream: "+err.Error())
		if adminEntry != nil {
			adminEntry.StatusCode = 502
			adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(body)
		}
		return
	}
	defer resp.Body.Close()
	upstreamHeaderDur := time.Since(upstreamStart)
	newBodyBytes = nil
	req = nil

	// 7. Handle Response
	respHandleStart := time.Now()
	if !isStream {
		app.handleNonStreamResponse(w, resp, outputMode, r, adminEntry)
	} else {
		app.handleStreamResponse(w, resp, outputMode, r, adminEntry)
	}
	respHandleDur := time.Since(respHandleStart)

	total := time.Since(start)
	if app.Config.SlowLogThreshold > 0 && total > app.Config.SlowLogThreshold {
		log.Printf("[Slow Request] total=%v upstreamHeaders=%v respHandle=%v bodyRead=%v inlineData=%v output=%s stream=%v path=%s",
			total, upstreamHeaderDur, respHandleDur, bodyReadDur, inlineDataDur, outputMode, isStream, r.URL.Path)
	}
}

func (app *App) handleNonStreamResponse(w http.ResponseWriter, resp *http.Response, outputMode string, req *http.Request, adminEntry *adminLogEntry) {
	start := time.Now()
	defer app.maybeRelieveMemory()

	readStart := time.Now()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		body := geminiError(w, 502, "Failed to read upstream response")
		if adminEntry != nil {
			adminEntry.StatusCode = 502
			adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(body)
		}
		return
	}
	readDur := time.Since(readStart)

	if adminEntry != nil {
		adminEntry.StatusCode = resp.StatusCode
	}

	if resp.StatusCode != 200 {
		// Pass through error
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		if adminEntry != nil {
			adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(respBody)
		}
		return
	}

	unmarshalStart := time.Now()
	var jsonBody map[string]interface{}
	if err := json.Unmarshal(respBody, &jsonBody); err != nil {
		// Not JSON? just pass through
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		if adminEntry != nil {
			adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(respBody)
		}
		return
	}
	respBody = nil
	unmarshalDur := time.Since(unmarshalStart)

	// Remove thought signature
	removeThoughtStart := time.Now()
	removeThoughtSignature(jsonBody)
	removeThoughtDur := time.Since(removeThoughtStart)

	// SPECIAL UPSTREAM: Markdown image link in text -> normalize to inlineData
	normalizeStart := time.Now()
	if err := app.normalizeSpecialMarkdownImageResponse(jsonBody, outputMode, req); err != nil {
		body := geminiError(w, 502, "Failed to normalize markdown image response: "+err.Error())
		if adminEntry != nil {
			adminEntry.StatusCode = 502
			adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(body)
		}
		return
	}
	keepLargestInlineImagePartPerCandidate(jsonBody)
	normalizeDur := time.Since(normalizeStart)

	// PROXY LOGIC: Convert Base64 -> URL
	convertStart := time.Now()
	if outputMode == "url" {
		err := app.convertInlineDataBase64ToUrlInResponse(jsonBody, req)
		if err != nil {
			log.Printf("[Fallback Triggered] Upload failed in normal response: %v", err)
			// Silent fallback: do nothing, jsonBody still has Base64
		}
	}
	convertDur := time.Since(convertStart)

	marshalStart := time.Now()
	finalBytes, marshalErr := marshalJSON(jsonBody)
	if marshalErr != nil {
		log.Printf("[marshalJSON] failed to marshal response body: %v", marshalErr)
	}
	jsonBody = nil
	marshalDur := time.Since(marshalStart)

	writeStart := time.Now()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(finalBytes)
	writeDur := time.Since(writeStart)
	if adminEntry != nil {
		adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(finalBytes)
		adminEntry.FinishReason = extractFinishReasonFromMap(jsonBody)
	}

	total := time.Since(start)
	if app.Config.SlowLogThreshold > 0 && total > app.Config.SlowLogThreshold {
		log.Printf("[Slow NonStream] total=%v read=%v unmarshal=%v stripThought=%v normalize=%v convert=%v marshal=%v write=%v output=%s",
			total, readDur, unmarshalDur, removeThoughtDur, normalizeDur, convertDur, marshalDur, writeDur, outputMode)
	}
}

func (app *App) handleStreamResponse(w http.ResponseWriter, resp *http.Response, outputMode string, req *http.Request, adminEntry *adminLogEntry) {
	defer app.maybeRelieveMemory()
	// SSE Header
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Start Sending...

	if adminEntry != nil {
		adminEntry.StatusCode = resp.StatusCode
	}

	if resp.StatusCode != 200 {
		// If upstream failed immediately via stream endpoint
		cap := &limitedCaptureWriter{limit: adminMaxBodyBytesPerEntry}
		_, _ = io.Copy(io.MultiWriter(w, cap), resp.Body)
		if adminEntry != nil {
			adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(cap.Bytes())
		}
		return
	}

	var lastDataJSON []byte
	sseBufPtr := sseScannerBufPool.Get().(*[]byte)
	defer sseScannerBufPool.Put(sseBufPtr)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(*sseBufPtr, MaxSSEScanTokenBytes)
	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data:") {
			fmt.Fprintf(w, "%s\n", line)
			w.(http.Flusher).Flush()
			continue
		}

		// It's a data line
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "" || raw == "[DONE]" {
			fmt.Fprintf(w, "%s\n", line)
			w.(http.Flusher).Flush()
			continue
		}

		var jsonBody map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &jsonBody); err != nil {
			fmt.Fprintf(w, "%s\n", line) // Failed to parse, pass raw
			w.(http.Flusher).Flush()
			continue
		}

		removeThoughtSignature(jsonBody)

		if err := app.normalizeSpecialMarkdownImageResponse(jsonBody, outputMode, req); err != nil {
			// SSE 中无法可靠返回非 2xx，尽量发出一个 error 事件并中止流
			log.Printf("[Stream Abort] normalize markdown image failed: %v", err)
			errObj := map[string]interface{}{
				"error": map[string]interface{}{
					"code":    502,
					"message": "Failed to normalize markdown image response: " + err.Error(),
					"status":  "ERROR",
				},
			}
			newBytes, marshalErr := marshalJSON(errObj)
			if marshalErr != nil {
				log.Printf("[marshalJSON] failed to marshal SSE error: %v", marshalErr)
			}
			fmt.Fprintf(w, "data: %s\n", string(newBytes))
			w.(http.Flusher).Flush()
			if adminEntry != nil {
				lastDataJSON = newBytes
			}
			return
		}
		keepLargestInlineImagePartPerCandidate(jsonBody)

		if outputMode == "url" {
			err := app.convertInlineDataBase64ToUrlInResponse(jsonBody, req)
			if err != nil {
				log.Printf("[Fallback Triggered] Upload failed in stream: %v", err)
			}
		}

		newBytes, marshalErr := marshalJSON(jsonBody)
		if marshalErr != nil {
			log.Printf("[marshalJSON] failed to marshal SSE chunk: %v", marshalErr)
		}
		if adminEntry != nil {
			lastDataJSON = newBytes
		}
		fmt.Fprintf(w, "data: %s\n", string(newBytes))
		w.(http.Flusher).Flush()
	}
	if err := scanner.Err(); err != nil {
		log.Printf("stream scan error: %v", err)
	}
	if adminEntry != nil {
		adminEntry.ResponseDownstream, adminEntry.ResponseImages = sanitizeJSONForAdminLog(lastDataJSON)
		if len(lastDataJSON) > 0 {
			var lastBody map[string]interface{}
			if err := json.Unmarshal(lastDataJSON, &lastBody); err == nil {
				adminEntry.FinishReason = extractFinishReasonFromMap(lastBody)
			}
		}
	}
	lastDataJSON = nil
}

// extractFinishReasonFromMap returns the finishReason of the first candidate
// in a parsed Gemini API response body, or "" if absent.
func extractFinishReasonFromMap(body map[string]interface{}) string {
	candidates, ok := body["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return ""
	}
	cand, ok := candidates[0].(map[string]interface{})
	if !ok {
		return ""
	}
	reason, _ := cand["finishReason"].(string)
	return reason
}

// --- Image Processing Logic ---

func (app *App) convertRequestInlineDataUrlsToBase64(root interface{}) error {
	return app.convertRequestInlineDataUrlsToBase64WithObserver(root, nil)
}

func (app *App) convertRequestInlineDataUrlsToBase64WithObserver(root interface{}, observer func(rawURL string, fromCache bool)) error {
	// 两阶段处理：
	// 1) 遍历收集所有 inlineData.data=http(s) 的节点引用（CPU）
	// 2) 对 URL 做去重后并发抓取并转 base64（I/O），再回填（避免遍历时阻塞网络）

	tasksByURL := make(map[string][]map[string]interface{})
	convertCount := 0

	var walk func(v interface{}) error
	walk = func(v interface{}) error {
		switch node := v.(type) {
		case map[string]interface{}:
			if inlineData, ok := node["inlineData"].(map[string]interface{}); ok {
				if dataStr, ok := inlineData["data"].(string); ok && isHttpUrlString(dataStr) {
					convertCount++
					if convertCount > MaxInlineDataUrls {
						return errors.New("too many inlineData URLs")
					}
					tasksByURL[dataStr] = append(tasksByURL[dataStr], inlineData)
				}
			}
			for _, child := range node {
				if err := walk(child); err != nil {
					return err
				}
			}
		case []interface{}:
			for _, child := range node {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := walk(root); err != nil {
		return err
	}
	if convertCount == 0 {
		return nil
	}

	maxConcurrent := MaxConcurrentInlineDataFetches
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	sem := make(chan struct{}, maxConcurrent)

	var wg sync.WaitGroup
	errCh := make(chan error, len(tasksByURL))

	for rawURL, targets := range tasksByURL {
		wg.Add(1)
		go func(rawURL string, targets []map[string]interface{}) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			mimeType, b64, fromCache, err := app.fetchImageUrlAsInlineData(rawURL)
			if err != nil {
				errCh <- err
				return
			}
			if observer != nil {
				observer(rawURL, fromCache)
			}
			for _, inlineData := range targets {
				inlineData["data"] = b64
				inlineData["mimeType"] = mimeType
			}
		}(rawURL, targets)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

type safeInlineDataFetchError struct {
	safeURL string
	err     error
}

func (e *safeInlineDataFetchError) Error() string {
	cause := stripURLFromClientError(e.err)
	if cause == "" {
		cause = "unknown error"
	}
	return fmt.Sprintf("inlineData 图片抓取失败 (%s): %s", e.safeURL, cause)
}

func (e *safeInlineDataFetchError) Unwrap() error {
	return e.err
}

func stripURLFromClientError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Common net/http format: `Get "https://host/path?query": <cause>`
	if i := strings.Index(msg, "\": "); i != -1 {
		return msg[i+3:]
	}
	return msg
}

func sanitizeURLForLog(u *url.URL) string {
	if u == nil {
		return ""
	}
	// Drop query/fragment to avoid leaking signed URL tokens into logs.
	return (&url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   u.Path,
	}).String()
}

func isRetryableImageFetchError(err error) bool {
	if err == nil {
		return false
	}
	// If caller canceled (e.g. client disconnected), do not retry.
	if errors.Is(err, context.Canceled) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout() || ne.Temporary()
	}
	// Some transient errors don't implement net.Error.
	return errors.Is(err, io.EOF)
}

// setImageFetchHeaders adds browser-like headers to an image fetch request to avoid
// being blocked by CDN/WAF/hotlink-protection on public object storages (e.g. Alibaba OSS).
func setImageFetchHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Set Referer to the target origin — helps pass hotlink protection (防盗链) checks
	// that whitelist the storage domain itself.
	if req.URL != nil && req.URL.Host != "" {
		req.Header.Set("Referer", req.URL.Scheme+"://"+req.URL.Host+"/")
	}
}

func (app *App) buildInlineDataFetchURL(rawURL string, uObj *url.URL) string {
	fetchURL := rawURL
	if uObj != nil && hostnameMatchesDomainPatterns(uObj.Hostname(), app.Config.ImageFetchExternalProxyDomains) {
		fetchURL = ExternalImageFetchProxyPrefix + url.QueryEscape(rawURL)
	}
	return fetchURL
}

func (app *App) downloadInlineDataImage(ctx context.Context, client *http.Client, rawURL string, uObj *url.URL, safeURL string) (string, []byte, error) {
	if client == nil {
		return "", nil, errors.New("image fetch client is nil")
	}

	fetchURL := app.buildInlineDataFetchURL(rawURL, uObj)
	var resp *http.Response
	var lastErr error

	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
		if err != nil {
			return "", nil, &safeInlineDataFetchError{safeURL: safeURL, err: err}
		}
		setImageFetchHeaders(req)

		resp, err = client.Do(req)
		if err == nil {
			break
		}
		lastErr = err
		if attempt == 0 && isRetryableImageFetchError(err) {
			select {
			case <-ctx.Done():
				return "", nil, &safeInlineDataFetchError{safeURL: safeURL, err: ctx.Err()}
			case <-time.After(150 * time.Millisecond):
				continue
			}
		}
		return "", nil, &safeInlineDataFetchError{safeURL: safeURL, err: err}
	}
	if resp == nil {
		return "", nil, &safeInlineDataFetchError{safeURL: safeURL, err: lastErr}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("inlineData 图片抓取失败 (%s): http status %d", safeURL, resp.StatusCode)
	}

	bytesData, err := readWithLimit(resp.Body, MaxImageBytes)
	if err != nil {
		return "", nil, fmt.Errorf("inlineData 图片抓取失败 (%s): %w", safeURL, err)
	}

	mime := normalizeImageMimeType(resp.Header.Get("Content-Type"))
	if mime == "image/png" || mime == "application/octet-stream" {
		guess := guessImageMimeTypeFromUrl(rawURL)
		if strings.HasPrefix(guess, "image/") {
			mime = guess
		}
	}
	return mime, bytesData, nil
}

func (app *App) fetchInlineDataBytesWithBridge(rawURL string, uObj *url.URL, safeURL string) (string, []byte, error) {
	fetchDirect := func() (string, []byte, error) {
		timeout := app.Config.ImageFetchTimeout
		if timeout <= 0 && app.ImageFetchClient != nil {
			timeout = app.ImageFetchClient.Timeout
		}
		if timeout <= 0 {
			timeout = ImageFetchTimeout
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return app.downloadInlineDataImage(ctx, app.ImageFetchClient, rawURL, uObj, safeURL)
	}

	if app.InlineDataURLBackgroundFetcher == nil {
		return fetchDirect()
	}

	waitTimeout := app.Config.InlineDataURLBackgroundFetchWaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = app.Config.ImageFetchTimeout
	}
	if waitTimeout <= 0 && app.ImageFetchClient != nil {
		waitTimeout = app.ImageFetchClient.Timeout
	}
	if waitTimeout <= 0 {
		waitTimeout = app.Config.InlineDataURLBackgroundFetchTotalTimeout
	}

	onSuccess := func(mime string, bytesData []byte) {
		if app.InlineDataURLCache != nil {
			_ = app.InlineDataURLCache.Set(rawURL, mime, bytesData)
		}
	}
	fetchInBackground := func(ctx context.Context) (string, []byte, error) {
		client := app.ImageFetchBackgroundClient
		if client == nil {
			client = app.ImageFetchClient
		}
		return app.downloadInlineDataImage(ctx, client, rawURL, uObj, safeURL)
	}

	mime, bytesData, err := app.InlineDataURLBackgroundFetcher.Fetch(
		rawURL,
		safeURL,
		waitTimeout,
		fetchInBackground,
		onSuccess,
	)
	if err != nil {
		var bridgeErr *inlineDataBackgroundFetchError
		if errors.As(err, &bridgeErr) {
			// Bridge unavailable/full: fallback to original behavior.
			return fetchDirect()
		}
		return "", nil, err
	}

	return mime, bytesData, nil
}

func (app *App) fetchImageUrlAsInlineData(rawUrl string) (string, string, bool, error) {
	// 1) Parse + SSRF guard
	uObj, err := url.Parse(rawUrl)
	if err != nil {
		return "", "", false, fmt.Errorf("invalid url: %v", err)
	}
	if isForbiddenFetchTarget(uObj) {
		return "", "", false, fmt.Errorf("forbidden target: %s", uObj.Hostname())
	}

	safeURL := sanitizeURLForLog(uObj)

	fetchBytes := func() (string, []byte, error) {
		fetchURL := rawUrl
		if hostnameMatchesDomainPatterns(uObj.Hostname(), app.Config.ImageFetchExternalProxyDomains) {
			fetchURL = ExternalImageFetchProxyPrefix + url.QueryEscape(rawUrl)
		}

		var resp *http.Response
		var lastErr error
		for attempt := 0; attempt < 2; attempt++ {
			req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet, fetchURL, nil)
			if reqErr != nil {
				return "", nil, &safeInlineDataFetchError{safeURL: safeURL, err: reqErr}
			}
			setImageFetchHeaders(req)
			resp, err = app.ImageFetchClient.Do(req)
			if err == nil {
				break
			}
			lastErr = err
			if attempt == 0 && isRetryableImageFetchError(err) {
				time.Sleep(150 * time.Millisecond)
				continue
			}
			return "", nil, &safeInlineDataFetchError{safeURL: safeURL, err: err}
		}
		if resp == nil {
			return "", nil, &safeInlineDataFetchError{safeURL: safeURL, err: lastErr}
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return "", nil, fmt.Errorf("inlineData 图片抓取失败 (%s): http status %d", safeURL, resp.StatusCode)
		}

		// Limit read (inlineData has a hard cap)
		bytesData, err := readWithLimit(resp.Body, MaxImageBytes)
		if err != nil {
			return "", nil, fmt.Errorf("inlineData 图片抓取失败 (%s): %w", safeURL, err)
		}

		mime := normalizeImageMimeType(resp.Header.Get("Content-Type"))
		if mime == "image/png" || mime == "application/octet-stream" {
			guess := guessImageMimeTypeFromUrl(rawUrl)
			if strings.HasPrefix(guess, "image/") {
				mime = guess
			}
		}

		return mime, bytesData, nil
	}

	// Use the background bridge to avoid restarting the same slow download on retries.
	fetchBytes = func() (string, []byte, error) {
		return app.fetchInlineDataBytesWithBridge(rawUrl, uObj, safeURL)
	}

	var mime string
	var bytesData []byte
	var fromCache bool
	if app.InlineDataURLCache != nil {
		mime, bytesData, fromCache, err = app.InlineDataURLCache.GetOrFetch(rawUrl, fetchBytes)
	} else {
		mime, bytesData, err = fetchBytes()
	}
	if err != nil {
		return "", "", false, err
	}

	b64 := base64.StdEncoding.EncodeToString(bytesData)
	return mime, b64, fromCache, nil
}

func (app *App) convertInlineDataBase64ToUrlInResponse(root interface{}, r *http.Request) error {
	type inlineDataKey struct {
		mimeType string
		data     string
	}

	tasksByData := make(map[inlineDataKey][]map[string]interface{})
	totalRefs := 0

	var walk func(v interface{})
	walk = func(v interface{}) {
		switch node := v.(type) {
		case map[string]interface{}:
			if inlineData, ok := node["inlineData"].(map[string]interface{}); ok {
				dataStr, _ := inlineData["data"].(string)
				if dataStr != "" && !strings.HasPrefix(dataStr, "http") {
					mimeType, _ := inlineData["mimeType"].(string)
					key := inlineDataKey{mimeType: mimeType, data: dataStr}
					tasksByData[key] = append(tasksByData[key], inlineData)
					totalRefs++
				}
			}

			for _, child := range node {
				walk(child)
			}
		case []interface{}:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(root)

	if len(tasksByData) == 0 {
		return nil
	}

	start := time.Now()
	uniqueRefs := len(tasksByData)

	maxConcurrent := MaxConcurrentInlineDataFetches
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	sem := make(chan struct{}, maxConcurrent)

	var wg sync.WaitGroup
	errCh := make(chan error, len(tasksByData))

	for key, targets := range tasksByData {
		wg.Add(1)
		go func(key inlineDataKey, targets []map[string]interface{}) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			imageBytes, err := base64.StdEncoding.DecodeString(key.data)
			if err != nil {
				// 保持与旧逻辑一致：无法解析为 base64 就跳过，不报错
				return
			}

			uploadRes, err := app.uploadImageBytesToURL(imageBytes, key.mimeType)
			if err != nil {
				errCh <- err
				return
			}
			imageBytes = nil

			finalURL := uploadRes.URL
			if uploadRes.Provider == "legacy" && app.Config.ProxyStandardOutputURLs {
				finalURL = app.maybeWrapProxyUrl(r, uploadRes.URL)
			}
			for _, inlineData := range targets {
				inlineData["data"] = finalURL
			}
		}(key, targets)
	}

	wg.Wait()
	close(errCh)

	var firstErr error
	for err := range errCh {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	tasksByData = nil
	dur := time.Since(start)
	app.maybeRelieveMemory()
	if dur > 5*time.Second {
		log.Printf("[InlineData->URL] refs=%d unique=%d dur=%v", totalRefs, uniqueRefs, dur)
	}

	return firstErr
}

func containsInlineData(root interface{}) bool {
	found := false
	var walk func(v interface{})
	walk = func(v interface{}) {
		if found {
			return
		}
		switch node := v.(type) {
		case map[string]interface{}:
			if _, ok := node["inlineData"]; ok {
				found = true
				return
			}
			for _, child := range node {
				walk(child)
			}
		case []interface{}:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(root)
	return found
}

func keepLargestInlineImagePartPerCandidate(root map[string]interface{}) {
	candidates, ok := root["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return
	}

	for _, candidateNode := range candidates {
		candidate, ok := candidateNode.(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := candidate["content"].(map[string]interface{})
		if !ok {
			continue
		}
		parts, ok := content["parts"].([]interface{})
		if !ok || len(parts) < 2 {
			continue
		}

		largestIndex := -1
		largestSize := -1
		imageCount := 0
		for idx, part := range parts {
			size, ok := inlineImagePartPayloadSize(part)
			if !ok {
				continue
			}
			imageCount++
			if size > largestSize {
				largestSize = size
				largestIndex = idx
			}
		}
		if imageCount <= 1 || largestIndex < 0 {
			continue
		}

		filteredParts := make([]interface{}, 0, len(parts)-imageCount+1)
		for idx, part := range parts {
			if _, ok := inlineImagePartPayloadSize(part); ok && idx != largestIndex {
				continue
			}
			filteredParts = append(filteredParts, part)
		}
		// 某些上游会错误返回多张 base64 图片；
		// 这里直接保留体积最大的那张，其余图片 part 丢弃。
		content["parts"] = filteredParts
	}
}

func inlineImagePartPayloadSize(part interface{}) (int, bool) {
	partMap, ok := part.(map[string]interface{})
	if !ok {
		return 0, false
	}
	inlineData, ok := partMap["inlineData"].(map[string]interface{})
	if !ok {
		return 0, false
	}

	mimeType, _ := inlineData["mimeType"].(string)
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/") {
		return 0, false
	}

	dataStr, _ := inlineData["data"].(string)
	dataStr = strings.TrimSpace(dataStr)
	if dataStr == "" {
		return 0, false
	}
	if strings.HasPrefix(dataStr, "http://") || strings.HasPrefix(dataStr, "https://") || strings.HasPrefix(dataStr, "/proxy/image") {
		return 0, false
	}

	return base64.StdEncoding.DecodedLen(len(dataStr)), true
}

func extractFirstMarkdownImageURLFromParts(parts []interface{}) (string, bool) {
	for _, p := range parts {
		partMap, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		text, ok := partMap["text"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}

		matches := markdownImageURLRe.FindStringSubmatch(text)
		if len(matches) == 2 && strings.TrimSpace(matches[1]) != "" {
			return strings.TrimSpace(matches[1]), true
		}
	}
	return "", false
}

func (app *App) wrapProxyUrlEncoded(targetUrl string) (string, error) {
	base, ok := app.proxyBaseURL()
	if !ok {
		return "", errors.New("proxy is disabled (PUBLIC_BASE_URL is empty or invalid)")
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(targetUrl))
	return fmt.Sprintf("%s/proxy/image?u=%s", base, encoded), nil
}

func (app *App) normalizeSpecialMarkdownImageResponse(jsonBody map[string]interface{}, outputMode string, req *http.Request) error {
	if containsInlineData(jsonBody) {
		return nil
	}

	candidates, ok := jsonBody["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return nil
	}
	cand0, ok := candidates[0].(map[string]interface{})
	if !ok {
		return nil
	}
	content, ok := cand0["content"].(map[string]interface{})
	if !ok {
		return nil
	}
	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return nil
	}

	imageURL, ok := extractFirstMarkdownImageURLFromParts(parts)
	if !ok {
		return nil
	}

	inline := map[string]interface{}{
		"inlineData": map[string]interface{}{},
	}
	inlineData := inline["inlineData"].(map[string]interface{})

	if outputMode == "url" {
		mimeType := guessImageMimeTypeFromUrl(imageURL)
		inlineData["mimeType"] = mimeType

		if app.Config.ProxySpecialUpstreamURLs {
			proxyURL, err := app.wrapProxyUrlEncoded(imageURL)
			if err == nil {
				inlineData["data"] = proxyURL
				app.maybePrewarmProxyImage(proxyURL)
			} else {
				// fail-open：当 PUBLIC_BASE_URL 不可用时不返回 5xx，改为直出原始 URL（速度优先）
				inlineData["data"] = imageURL
			}
		} else {
			// 速度优先：允许明文暴露上游图片域名，不走 /proxy/image
			inlineData["data"] = imageURL
		}
	} else {
		mimeType, b64, _, err := app.fetchImageUrlAsInlineData(imageURL)
		if err != nil {
			return err
		}
		inlineData["data"] = b64
		inlineData["mimeType"] = mimeType
	}

	content["parts"] = []interface{}{inline}
	return nil
}

func (app *App) maybePrewarmProxyImage(proxyURL string) {
	if strings.TrimSpace(proxyURL) == "" {
		return
	}

	select {
	case proxyPrewarmSem <- struct{}{}:
		go func() {
			defer func() { <-proxyPrewarmSem }()
			app.prewarmProxyImage(proxyURL)
		}()
	default:
		log.Printf("[Prewarm Skipped] Too many concurrent prewarms")
	}
}

func (app *App) prewarmProxyImage(proxyURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), ProxyPrewarmTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", proxyURL, nil)
	if err != nil {
		log.Printf("[Prewarm Failed] build request: %v", err)
		return
	}

	resp, err := app.ImageFetchClient.Do(req)
	if err != nil {
		log.Printf("[Prewarm Failed] fetch proxy: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("[Prewarm Failed] non-200: %d", resp.StatusCode)
		return
	}

	// 预热缓存需要尽量完整读完 body，避免上游连接提前中断导致缓存不落盘。
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		log.Printf("[Prewarm Failed] read body: %v", err)
		return
	}
}

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

func (app *App) callLegacyUploader(data []byte, mimeType string) (uploadResult, error) {
	if app.legacyUploadFunc != nil {
		return app.legacyUploadFunc(data, mimeType)
	}
	urlStr, err := app.uploadImageBytesToUrl(data, mimeType)
	if err != nil {
		return uploadResult{}, err
	}
	return uploadResult{URL: urlStr, Provider: "legacy"}, nil
}

func (app *App) callR2Uploader(data []byte, mimeType string) (uploadResult, error) {
	if app.r2UploadFunc != nil {
		return app.r2UploadFunc(data, mimeType)
	}
	return app.uploadToR2(data, mimeType)
}

func (app *App) now() time.Time {
	if app != nil && app.nowFunc != nil {
		return app.nowFunc().UTC()
	}
	return time.Now().UTC()
}

func (app *App) randomHex(n int) (string, error) {
	if app != nil && app.randomHexFunc != nil {
		return app.randomHexFunc(n)
	}
	if n <= 0 {
		return "", errors.New("random hex size must be positive")
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func buildR2ObjectKey(prefix, mimeType string, now time.Time, randHex string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		prefix = "images"
	}
	ext := strings.TrimPrefix(extensionFromMime(mimeType), ".")
	if ext == "" {
		ext = "bin"
	}
	now = now.UTC()
	return fmt.Sprintf("%s/%04d/%02d/%02d/%d-%s.%s",
		prefix, now.Year(), now.Month(), now.Day(), now.UnixMilli(), randHex, ext)
}

func (app *App) uploadToR2(data []byte, mimeType string) (uploadResult, error) {
	if app == nil || app.r2PutObjectFunc == nil {
		return uploadResult{}, errors.New("r2 uploader is not configured")
	}
	randHex, err := app.randomHex(4)
	if err != nil {
		return uploadResult{}, err
	}
	key := buildR2ObjectKey(app.Config.R2ObjectPrefix, mimeType, app.now(), randHex)

	timeout := app.Config.UploadTimeout
	if timeout <= 0 {
		timeout = UploadTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := app.r2PutObjectFunc(ctx, key, data, mimeType); err != nil {
		return uploadResult{}, err
	}
	publicBaseURL := strings.TrimRight(strings.TrimSpace(app.Config.R2PublicBaseURL), "/")
	return uploadResult{
		URL:      publicBaseURL + "/" + key,
		Provider: "r2",
	}, nil
}

func newR2PutObjectFunc(cfg Config, httpClient *http.Client) (func(ctx context.Context, key string, body []byte, mimeType string) error, error) {
	endpoint, err := parseR2Endpoint(cfg.R2Endpoint)
	if err != nil {
		return nil, err
	}
	client := httpClient
	if client == nil {
		client = &http.Client{Timeout: UploadTimeout}
	}
	return func(ctx context.Context, key string, body []byte, mimeType string) error {
		return putObjectToR2(ctx, client, endpoint, cfg.R2Bucket, cfg.R2AccessKeyID, cfg.R2SecretAccessKey, key, body, mimeType)
	}, nil
}

func parseR2Endpoint(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("R2_ENDPOINT is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse R2_ENDPOINT: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("R2_ENDPOINT must use http or https")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return nil, errors.New("R2_ENDPOINT host is empty")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	return parsed, nil
}

func putObjectToR2(ctx context.Context, client *http.Client, endpoint *url.URL, bucket, accessKeyID, secretAccessKey, key string, body []byte, mimeType string) error {
	if client == nil {
		return errors.New("r2 http client is nil")
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := sha256Hex(body)
	objectURL, canonicalURI := buildR2ObjectURL(endpoint, bucket, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, objectURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)

	canonicalHeaders := strings.Join([]string{
		"content-type:" + mimeType,
		"host:" + req.URL.Host,
		"x-amz-content-sha256:" + payloadHash,
		"x-amz-date:" + amzDate,
		"",
	}, "\n")
	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		http.MethodPut,
		canonicalURI,
		"",
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := dateStamp + "/auto/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signingKey := deriveAWSV4SigningKey(secretAccessKey, dateStamp, "auto", "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKeyID,
		credentialScope,
		signedHeaders,
		signature,
	))

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("r2 put object failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}

func buildR2ObjectURL(endpoint *url.URL, bucket, key string) (objectURL string, canonicalURI string) {
	bucket = strings.TrimSpace(bucket)
	key = strings.TrimLeft(strings.TrimSpace(key), "/")
	plainPath := "/" + bucket + "/" + key
	canonicalURI = "/" + pathEscapePreservingSlashes(bucket) + "/" + pathEscapePreservingSlashes(key)
	urlCopy := *endpoint
	urlCopy.Path = strings.TrimRight(endpoint.Path, "/") + plainPath
	urlCopy.RawPath = strings.TrimRight(endpoint.RawPath, "/") + canonicalURI
	return urlCopy.String(), canonicalURI
}

func pathEscapePreservingSlashes(v string) string {
	if v == "" {
		return ""
	}
	parts := strings.Split(v, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}

func deriveAWSV4SigningKey(secretAccessKey, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretAccessKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func (app *App) uploadImageBytesToUrl(data []byte, mimeType string) (string, error) {
	// Retry Logic: uguu.se -> kefan
	// Attempt Uguu
	for i := 0; i <= UploadRetries; i++ {
		urlStr, err := app.uploadToUguu(data, mimeType)
		if err == nil {
			return urlStr, nil
		}
		// log error?
	}

	// Attempt Kefan
	for i := 0; i <= UploadRetries; i++ {
		urlStr, err := app.uploadToKefan(data, mimeType)
		if err == nil {
			return urlStr, nil
		}
	}

	return "", errors.New("all upload providers failed")
}

func (app *App) uploadToUguu(data []byte, mimeType string) (string, error) {
	ext := extensionFromMime(mimeType)
	req, contentType, err := newStreamingMultipartUploadRequest("https://uguu.se/upload", "files[]", "image"+ext, data)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", UploadUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := app.UploadClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("uguu.se status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	respBytes, _ := io.ReadAll(resp.Body)
	var resObj struct {
		Success bool `json:"success"`
		Files   []struct {
			URL string `json:"url"`
		} `json:"files"`
	}
	if err := json.Unmarshal(respBytes, &resObj); err != nil {
		return "", fmt.Errorf("uguu.se invalid json: %w: %s", err, strings.TrimSpace(string(respBytes)))
	}
	if !resObj.Success || len(resObj.Files) == 0 || resObj.Files[0].URL == "" {
		return "", fmt.Errorf("uguu.se upload failed: %s", strings.TrimSpace(string(respBytes)))
	}
	return resObj.Files[0].URL, nil
}

func (app *App) uploadToKefan(data []byte, mimeType string) (string, error) {
	ext := extensionFromMime(mimeType)
	req, contentType, err := newStreamingMultipartUploadRequest("https://ai.kefan.cn/api/upload/local", "file", "image"+ext, data)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", UploadUserAgent)

	resp, err := app.UploadClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	// Parse JSON {success: true, data: "url"}
	var resObj struct {
		Success bool   `json:"success"`
		Data    string `json:"data"`
	}
	json.Unmarshal(respBytes, &resObj)

	if !resObj.Success || resObj.Data == "" {
		return "", fmt.Errorf("kefan failed: %s", string(respBytes))
	}
	return resObj.Data, nil
}

func newStreamingMultipartUploadRequest(targetURL, fieldName, fileName string, data []byte) (*http.Request, string, error) {
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()

	req, err := http.NewRequest(http.MethodPost, targetURL, pr)
	if err != nil {
		_ = pw.Close()
		return nil, "", err
	}

	go func() {
		var pipeErr error
		defer func() {
			closeErr := writer.Close()
			if pipeErr == nil {
				pipeErr = closeErr
			}
			_ = pw.CloseWithError(pipeErr)
		}()

		part, err := writer.CreateFormFile(fieldName, fileName)
		if err != nil {
			pipeErr = err
			return
		}
		if _, err := part.Write(data); err != nil {
			pipeErr = err
			return
		}
	}()

	return req, contentType, nil
}

// --- Helpers ---

func isProxyDisabledPublicBaseURLValue(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	switch strings.ToLower(v) {
	case "off", "disable", "disabled", "false", "0", "no", "none":
		return true
	default:
		return false
	}
}

func (app *App) proxyBaseURL() (string, bool) {
	raw := strings.TrimSpace(app.Config.PublicBaseURL)
	if isProxyDisabledPublicBaseURLValue(raw) {
		return "", false
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	return strings.TrimRight(raw, "/"), true
}

func (app *App) isProxyEnabled() bool {
	_, ok := app.proxyBaseURL()
	return ok
}

func (app *App) maybeWrapProxyUrl(r *http.Request, targetUrl string) string {
	if !app.isProxyEnabled() {
		return targetUrl
	}

	uParsed, uErr := url.Parse(targetUrl)
	if uErr == nil && strings.EqualFold(uParsed.Hostname(), "ai.kefan.cn") {
		return targetUrl
	}

	return app.wrapProxyUrl(r, targetUrl)
}

func (app *App) wrapProxyUrl(r *http.Request, targetUrl string) string {
	base, ok := app.proxyBaseURL()
	if !ok {
		return targetUrl
	}
	return fmt.Sprintf("%s/proxy/image?url=%s", base, url.QueryEscape(targetUrl))
}

func getOutputMode(query string, body map[string]interface{}) string {
	if strings.EqualFold(strings.TrimSpace(query), "url") {
		return "url"
	}
	if v, ok := body["output"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "url") {
		return "url"
	}

	// Check generationConfig.imageConfig.output
	if genConfig, ok := body["generationConfig"].(map[string]interface{}); ok {
		if imgConfig, ok := genConfig["imageConfig"].(map[string]interface{}); ok {
			if v, ok := imgConfig["output"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "url") {
				return "url"
			}
		}
	}
	// Check generation_config.image_config.output (snake_case)
	if genConfig, ok := body["generation_config"].(map[string]interface{}); ok {
		if imgConfig, ok := genConfig["image_config"].(map[string]interface{}); ok {
			if v, ok := imgConfig["output"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "url") {
				return "url"
			}
		}
	}

	return "base64"
}

func stripOutputFromMap(m map[string]interface{}) {
	delete(m, "output")

	// Handle nested generationConfig -> imageConfig
	if genConfig, ok := m["generationConfig"].(map[string]interface{}); ok {
		if imgConfig, ok := genConfig["imageConfig"].(map[string]interface{}); ok {
			delete(imgConfig, "output")
		}
		// Also check mixedCase/snake_case just in case?
		// Node.js version checked generationConfig.imageConfig specifically
		// We'll stick to exact key match for now as JSON unmarshal is case-sensitive usually unless specific decoder
	}

	// Also check snake_case which Gemini sometimes accepts?
	// The error message from user "generation_config.image_config" implies snake_case might be used in the request
	if genConfig, ok := m["generation_config"].(map[string]interface{}); ok {
		if imgConfig, ok := genConfig["image_config"].(map[string]interface{}); ok {
			delete(imgConfig, "output")
		}
	}
}

func removeThoughtSignature(root interface{}) {
	// Recursive delete "thoughtSignature"
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch node := v.(type) {
		case map[string]interface{}:
			delete(node, "thoughtSignature")
			for _, child := range node {
				walk(child)
			}
		case []interface{}:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(root)
}

func readWithLimit(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit)) // Simplified. Real limit reader returns EOF?
}

func isHttpUrlString(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func normalizeImageMimeType(t string) string {
	t = strings.Split(t, ";")[0]
	t = strings.TrimSpace(t)
	if t == "" {
		return "image/png"
	}
	return t
}

func extensionFromMime(t string) string {
	if t == "image/jpeg" {
		return ".jpg"
	}
	if t == "image/gif" {
		return ".gif"
	}
	if t == "image/webp" {
		return ".webp"
	}
	return ".png"
}

func guessImageMimeTypeFromUrl(rawUrl string) string {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return "image/png"
	}
	p := strings.ToLower(u.Path)
	if strings.HasSuffix(p, ".png") {
		return "image/png"
	}
	if strings.HasSuffix(p, ".jpg") || strings.HasSuffix(p, ".jpeg") {
		return "image/jpeg"
	}
	if strings.HasSuffix(p, ".webp") {
		return "image/webp"
	}
	if strings.HasSuffix(p, ".gif") {
		return "image/gif"
	}
	return "image/png"
}

func isForbiddenFetchTarget(u *url.URL) bool {
	hostname := u.Hostname()
	// explicit localhost check
	if strings.EqualFold(hostname, "localhost") {
		return true
	}

	// IP check (simplified private range check)
	// In production Go, use net.ParseIP and checking ranges
	// For this snippet, we'll do basic checks or allow public DNS
	// Note: Proper SSRF protection in Go usually requires a custom dialer
	// to prevent TOCTOU DNS rebinding, but basic string check for now:
	if strings.HasPrefix(hostname, "127.") || strings.HasPrefix(hostname, "10.") || strings.HasPrefix(hostname, "192.168.") {
		return true
	}
	if strings.HasPrefix(hostname, "172.") {
		// 172.16.x.x - 172.31.x.x
		parts := strings.Split(hostname, ".")
		if len(parts) >= 2 {
			second, err := strconv.Atoi(parts[1])
			if err == nil && second >= 16 && second <= 31 {
				return true
			}
		}
		// If parsing fails or not in range, we assume it's public 172.x (e.g. 172.100.x.x)
		return false
	}
	return false
}

func copyUpstreamHeaders(dst http.Header, src http.Header, apiKey string) {
	// Preserve specific headers
	if ct := src.Get("Content-Type"); ct != "" {
		dst.Set("Content-Type", ct)
	}
	if accept := src.Get("Accept"); accept != "" {
		dst.Set("Accept", accept)
	}
	// Always set Gemini keys
	dst.Set("x-goog-api-key", apiKey)
	dst.Set("Authorization", "Bearer "+apiKey)
}

func geminiError(w http.ResponseWriter, code int, msg string) []byte {
	payload := map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": msg,
			"status":  "ERROR",
		},
	}
	b, marshalErr := marshalJSON(payload)
	if marshalErr != nil {
		log.Printf("[marshalJSON] failed to marshal error payload: %v", marshalErr)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if len(b) > 0 {
		w.Write(b)
	}
	return b
}

func inlineDataUrlUserFacingErrorMessage(err error) string {
	// 对外返回更可读的中文提示（细节写入日志避免泄露 URL / Token 等敏感信息）。
	//
	// 常见场景：图片过大、图床/链接不可达、网络抖动导致下载/读 body 超时。
	tips := "请检查输入图片大小（建议单图 ≤ 5MB），并建议开启图片缩放（长边建议 2048 或 3072）或提高压缩率后重试。"
	if isTimeoutOrCanceled(err) {
		return "处理 inlineData 图片 URL 失败：下载/读取图片内容超时或被取消。" + tips
	}
	return "处理 inlineData 图片 URL 失败：无法抓取或读取图片内容（可能是图片过大、链接不可访问或网络超时）。" + tips
}

func isTimeoutOrCanceled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
