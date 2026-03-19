package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	appsvc "banana-async-gateway/internal/app"
	"banana-async-gateway/internal/config"
)

func main() {
	logger := log.New(os.Stdout, "banana-async-gateway ", log.LstdFlags|log.LUTC)

	cfg, err := config.LoadFromEnv()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	application, err := appsvc.New(cfg, logger)
	if err != nil {
		logger.Fatalf("create app: %v", err)
	}
	serverErrCh := make(chan error, 1)

	go func() {
		serverErrCh <- application.ListenAndServe()
	}()

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErrCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("server exited: %v", err)
		}
	case <-signalCtx.Done():
		logger.Printf("shutdown signal received")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGracePeriod)
		defer cancel()

		if err := application.Shutdown(shutdownCtx); err != nil {
			logger.Printf("shutdown error: %v", err)
			os.Exit(1)
		}

		if err := <-serverErrCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("server exited with error: %v", err)
			os.Exit(1)
		}
	}
}
