package app

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

const forcedUncertainMessage = "gateway shutdown grace period elapsed before task completion"

type shutdownWorkers interface {
	CloseQueue()
	Shutdown(ctx context.Context) error
}

type shutdownRepository interface {
	MarkDispatchedRunningUncertain(ctx context.Context, errorCode, errorMessage string) (int64, error)
}

type DrainingSubmitHandler struct {
	next              http.Handler
	retryAfterSeconds int
	draining          atomic.Bool
}

func NewDrainingSubmitHandler(next http.Handler, retryAfterSeconds int) *DrainingSubmitHandler {
	return &DrainingSubmitHandler{
		next:              next,
		retryAfterSeconds: retryAfterSeconds,
	}
}

func (h *DrainingSubmitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", strconv.Itoa(h.retryAfterSeconds))
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"code":    "service_draining",
				"message": "service is draining",
			},
		})
		return
	}
	h.next.ServeHTTP(w, r)
}

func (h *DrainingSubmitHandler) StartDraining() {
	h.draining.Store(true)
}

func runShutdown(
	ctx context.Context,
	logger *log.Logger,
	submitGate *DrainingSubmitHandler,
	workers shutdownWorkers,
	repo shutdownRepository,
	shutdownServer func(context.Context) error,
	extraSubmitGates ...*DrainingSubmitHandler,
) error {
	if submitGate != nil {
		submitGate.StartDraining()
	}
	for _, gate := range extraSubmitGates {
		if gate != nil {
			gate.StartDraining()
		}
	}
	if logger != nil {
		logger.Printf("event=shutdown_drain_started")
	}

	if workers != nil {
		workers.CloseQueue()
		if err := workers.Shutdown(ctx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				if repo != nil {
					forceCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if logger != nil {
						logger.Printf("event=shutdown_force_uncertain")
					}
					if _, forceErr := repo.MarkDispatchedRunningUncertain(forceCtx, "gateway_shutdown_uncertain", forcedUncertainMessage); forceErr != nil {
						return forceErr
					}
				}
			} else {
				return err
			}
		}
	}

	if shutdownServer == nil {
		return nil
	}

	serverCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		serverCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	if err := shutdownServer(serverCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
