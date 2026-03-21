package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	taskcache "banana-async-gateway/internal/cache"
	"banana-async-gateway/internal/cleanup"
	"banana-async-gateway/internal/config"
	"banana-async-gateway/internal/httpapi"
	"banana-async-gateway/internal/metrics"
	"banana-async-gateway/internal/queue"
	taskratelimit "banana-async-gateway/internal/ratelimit"
	"banana-async-gateway/internal/recovery"
	"banana-async-gateway/internal/store"
	"banana-async-gateway/internal/worker"

	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	cfg        config.Config
	logger     *log.Logger
	server     *http.Server
	queue      *queue.MemoryQueue
	workers    *worker.Pool
	submitGate *DrainingSubmitHandler
	recovery   *recovery.Scanner
	cleaner    *cleanup.Cleaner
	metrics    *metrics.Registry
	bgCtx      context.Context
	bgCancel   context.CancelFunc
	pool       *pgxpool.Pool
	repository *store.Repository
}

func New(cfg config.Config, logger *log.Logger) (*App, error) {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}

	pool, err := store.NewPostgresPool(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	repository := store.NewRepository(pool)
	taskQueue := queue.NewMemoryQueue(cfg.MaxQueueSize)
	queryCache := taskcache.NewTaskCache(taskcache.Config{})
	queryLimiter := taskratelimit.NewLimiter(taskratelimit.Config{
		RefillInterval: time.Duration(cfg.TaskPollRetryAfterSec) * time.Second,
		Burst:          1,
	})
	submitHandler, err := httpapi.NewSubmitHandler(cfg, repository, taskQueue)
	if err != nil {
		pool.Close()
		return nil, err
	}
	submitGate := NewDrainingSubmitHandler(submitHandler, cfg.TaskPollRetryAfterSec)
	queryHandler := httpapi.NewQueryHandler(cfg, repository, queryCache, queryLimiter)
	workerPool, err := worker.NewPool(cfg, logger, repository, taskQueue)
	if err != nil {
		pool.Close()
		return nil, err
	}
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	recoveryScanner := recovery.NewScanner(repository, taskQueue, recovery.Config{Logger: logger})
	cleaner := cleanup.NewCleaner(repository, cleanup.Config{})
	metricsRegistry := metrics.Default()

	return &App{
		cfg:        cfg,
		logger:     logger,
		queue:      taskQueue,
		workers:    workerPool,
		submitGate: submitGate,
		recovery:   recoveryScanner,
		cleaner:    cleaner,
		metrics:    metricsRegistry,
		bgCtx:      backgroundCtx,
		bgCancel:   backgroundCancel,
		pool:       pool,
		repository: repository,
		server: &http.Server{
			Addr: cfg.ListenAddr,
			Handler: httpapi.NewRouter(logger, httpapi.Handlers{
				SubmitTask:    submitGate,
				BatchGetTasks: http.HandlerFunc(queryHandler.BatchGetTasks),
				GetTask:       http.HandlerFunc(queryHandler.GetTask),
				ListTasks:     http.HandlerFunc(queryHandler.ListTasks),
				TaskContent:   http.HandlerFunc(queryHandler.TaskContent),
			}),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}, nil
}

func (a *App) ListenAndServe() error {
	if a.recovery != nil {
		if err := a.recovery.Run(context.Background()); err != nil {
			return err
		}
	}
	if a.workers != nil {
		a.workers.Start()
	}
	if a.cleaner != nil && a.bgCtx != nil {
		go a.cleaner.Run(a.bgCtx)
	}
	a.logger.Printf("async gateway listening on %s", a.cfg.ListenAddr)
	return a.server.ListenAndServe()
}

func (a *App) Shutdown(ctx context.Context) error {
	err := runShutdown(ctx, a.logger, a.submitGate, a.workers, a.repository, a.server.Shutdown)
	if a.bgCancel != nil {
		a.bgCancel()
	}
	if a.pool != nil {
		a.pool.Close()
	}
	return err
}
