package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"banana-async-gateway/internal/config"
)

const defaultPostgresConnMaxLifetime = 30 * time.Minute

func NewPostgresPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}

	poolConfig.MaxConns = int32(cfg.PostgresMaxOpenConns)
	// pgxpool 没有 MaxIdleConns 的直接语义，这里将其映射为预热连接下限，保持一部分空闲连接常驻。
	poolConfig.MinConns = int32(minInt(cfg.PostgresMaxIdleConns, cfg.PostgresMaxOpenConns))
	poolConfig.MaxConnLifetime = defaultPostgresConnMaxLifetime

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return pool, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
