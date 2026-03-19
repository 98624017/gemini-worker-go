package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"banana-async-gateway/internal/config"
	"banana-async-gateway/internal/domain"
	"banana-async-gateway/internal/security"
)

type ForwardOutcome struct {
	Summary            *domain.ResultSummary
	ErrorCode          string
	ErrorMessage       string
	TransportUncertain bool
}

type Forwarder struct {
	baseURL        string
	client         *http.Client
	encryptionKey  []byte
	requestTimeout time.Duration
}

func NewForwarder(cfg config.Config) (*Forwarder, error) {
	encryptionKey, err := security.ParseEncryptionKey(cfg.TaskPayloadEncryptionKey)
	if err != nil {
		return nil, err
	}

	return &Forwarder{
		baseURL:        strings.TrimRight(cfg.NewAPIBaseURL, "/"),
		client:         &http.Client{Timeout: cfg.NewAPIRequestTimeout},
		encryptionKey:  encryptionKey,
		requestTimeout: cfg.NewAPIRequestTimeout,
	}, nil
}

func (f *Forwarder) Forward(ctx context.Context, task *domain.Task, payload *domain.TaskPayload, onDispatched func(context.Context) error) (ForwardOutcome, error) {
	if task == nil || payload == nil {
		return ForwardOutcome{}, fmt.Errorf("task and payload are required")
	}

	authHeader, err := security.DecryptAuthorization(payload.AuthorizationCrypt, f.encryptionKey)
	if err != nil {
		return ForwardOutcome{
			ErrorCode:    "unknown_error",
			ErrorMessage: fmt.Sprintf("decrypt authorization: %v", err),
		}, nil
	}

	requestURL, err := buildForwardURL(f.baseURL, task.RequestPath, task.RequestQuery)
	if err != nil {
		return ForwardOutcome{
			ErrorCode:    "unknown_error",
			ErrorMessage: fmt.Sprintf("build upstream request: %v", err),
		}, nil
	}

	attempts := 1
	for {
		outcome, retry, callErr := f.doForward(ctx, requestURL, authHeader, task, payload, onDispatched)
		if callErr != nil {
			return ForwardOutcome{}, callErr
		}
		if retry {
			attempts++
			if attempts <= 2 {
				continue
			}
			return ForwardOutcome{
				ErrorCode:    "upstream_timeout",
				ErrorMessage: "upstream request timed out",
			}, nil
		}
		return outcome, nil
	}
}

func (f *Forwarder) doForward(ctx context.Context, requestURL, authHeader string, task *domain.Task, payload *domain.TaskPayload, onDispatched func(context.Context) error) (ForwardOutcome, bool, error) {
	requestCtx := ctx
	var cancel context.CancelFunc
	if f.requestTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, f.requestTimeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, bytes.NewReader(payload.RequestBodyJSON))
	if err != nil {
		return ForwardOutcome{
			ErrorCode:    "unknown_error",
			ErrorMessage: fmt.Sprintf("create upstream request: %v", err),
		}, false, nil
	}
	copyForwardHeaders(req.Header, payload.ForwardHeaders)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("X-Banana-Task-ID", task.ID)
	req.Header.Set("X-Banana-Async-Source", "local-gateway")
	req.Header.Del("Content-Encoding")

	var (
		wroteRequest bool
		traceOnce    sync.Once
	)
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			traceOnce.Do(func() {
				wroteRequest = true
				if onDispatched != nil {
					_ = onDispatched(requestCtx)
				}
			})
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	resp, err := f.httpClient().Do(req)
	if err != nil {
		if wroteRequest {
			return ForwardOutcome{
				ErrorCode:          "upstream_transport_uncertain",
				ErrorMessage:       "connection to newapi broke after request dispatch; task result may be uncertain",
				TransportUncertain: true,
			}, false, nil
		}
		if isTimeoutError(err) {
			return ForwardOutcome{}, true, nil
		}
		return ForwardOutcome{
			ErrorCode:    "upstream_error",
			ErrorMessage: err.Error(),
		}, false, nil
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		if wroteRequest {
			return ForwardOutcome{
				ErrorCode:          "upstream_transport_uncertain",
				ErrorMessage:       "connection to newapi broke after request dispatch; task result may be uncertain",
				TransportUncertain: true,
			}, false, nil
		}
		return ForwardOutcome{
			ErrorCode:    "upstream_error",
			ErrorMessage: err.Error(),
		}, false, nil
	}

	if resp.StatusCode == http.StatusRequestTimeout {
		return ForwardOutcome{}, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return classifyHTTPFailure(resp.StatusCode, bodyBytes), false, nil
	}

	summary, summaryErr := ExtractResultSummary(bodyBytes)
	if summaryErr != nil {
		if wroteRequest && strings.Contains(summaryErr.Message, "unexpected end of JSON input") {
			return ForwardOutcome{
				ErrorCode:          "upstream_transport_uncertain",
				ErrorMessage:       "connection to newapi broke after request dispatch; task result may be uncertain",
				TransportUncertain: true,
			}, false, nil
		}
		return ForwardOutcome{
			ErrorCode:    summaryErr.Code,
			ErrorMessage: summaryErr.Message,
		}, false, nil
	}

	return ForwardOutcome{Summary: summary}, false, nil
}

func buildForwardURL(baseURL, requestPath, rawQuery string) (string, error) {
	parsedBase, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}

	resolved := parsedBase.ResolveReference(&url.URL{
		Path:     requestPath,
		RawQuery: rawQuery,
	})
	return resolved.String(), nil
}

func copyForwardHeaders(header http.Header, source map[string]string) {
	for key, value := range source {
		if value == "" {
			continue
		}
		switch http.CanonicalHeaderKey(key) {
		case "Authorization", "Content-Encoding", "Content-Length", "Host":
			continue
		default:
			header.Set(key, value)
		}
	}
}

func classifyHTTPFailure(statusCode int, body []byte) ForwardOutcome {
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = fmt.Sprintf("upstream returned status %d", statusCode)
	}

	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ForwardOutcome{ErrorCode: "auth_failed", ErrorMessage: message}
	case http.StatusPaymentRequired:
		return ForwardOutcome{ErrorCode: "insufficient_balance", ErrorMessage: message}
	case http.StatusTooManyRequests:
		return ForwardOutcome{ErrorCode: "upstream_rate_limited", ErrorMessage: message}
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return ForwardOutcome{ErrorCode: "invalid_request", ErrorMessage: message}
	default:
		if statusCode >= http.StatusInternalServerError {
			return ForwardOutcome{ErrorCode: "upstream_error", ErrorMessage: message}
		}
		return ForwardOutcome{ErrorCode: "upstream_error", ErrorMessage: message}
	}
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (f *Forwarder) httpClient() *http.Client {
	if f.client != nil {
		return f.client
	}
	return http.DefaultClient
}
