package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"banana-async-gateway/internal/config"
	"banana-async-gateway/internal/httpapi"
	"banana-async-gateway/internal/queue"
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
	submitHandler, err := httpapi.NewSubmitHandler(cfg, repository, taskQueue)
	if err != nil {
		pool.Close()
		return nil, err
	}
	workerPool, err := worker.NewPool(cfg, logger, repository, taskQueue)
	if err != nil {
		pool.Close()
		return nil, err
	}

	return &App{
		cfg:        cfg,
		logger:     logger,
		queue:      taskQueue,
		workers:    workerPool,
		pool:       pool,
		repository: repository,
		server: &http.Server{
			Addr: cfg.ListenAddr,
			Handler: httpapi.NewRouter(logger, httpapi.Handlers{
				SubmitTask: submitHandler,
			}),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}, nil
}

func (a *App) ListenAndServe() error {
	if a.workers != nil {
		a.workers.Start()
	}
	a.logger.Printf("async gateway listening on %s", a.cfg.ListenAddr)
	return a.server.ListenAndServe()
}

func (a *App) Shutdown(ctx context.Context) error {
	err := a.server.Shutdown(ctx)
	if a.workers != nil {
		a.workers.CloseQueue()
		if workerErr := a.workers.Shutdown(ctx); workerErr != nil && err == nil {
			err = workerErr
		}
	}
	if a.pool != nil {
		a.pool.Close()
	}
	return err
}
