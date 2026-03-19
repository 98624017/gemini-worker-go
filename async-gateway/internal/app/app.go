package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"banana-async-gateway/internal/config"
	"banana-async-gateway/internal/httpapi"
)

type App struct {
	cfg    config.Config
	logger *log.Logger
	server *http.Server
}

func New(cfg config.Config, logger *log.Logger) *App {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}

	return &App{
		cfg:    cfg,
		logger: logger,
		server: &http.Server{
			Addr:              cfg.ListenAddr,
			Handler:           httpapi.NewRouter(logger),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

func (a *App) ListenAndServe() error {
	a.logger.Printf("async gateway listening on %s", a.cfg.ListenAddr)
	return a.server.ListenAndServe()
}

func (a *App) Shutdown(ctx context.Context) error {
	return a.server.Shutdown(ctx)
}
