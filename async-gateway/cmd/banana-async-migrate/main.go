package main

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	logger := log.New(os.Stdout, "banana-async-migrate ", log.LstdFlags|log.LUTC)

	if len(os.Args) < 2 {
		logger.Fatalf("usage: banana-async-migrate [up|down|version]")
	}

	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		logger.Fatalf("POSTGRES_DSN is required")
	}

	migrationsURL := "file://migrations"
	m, err := migrate.New(migrationsURL, dsn)
	if err != nil {
		logger.Fatalf("create migrator: %v", err)
	}
	defer m.Close()

	switch os.Args[1] {
	case "up":
		err = m.Up()
		if errors.Is(err, migrate.ErrNoChange) {
			logger.Print("no change")
			return
		}
		if err != nil {
			logger.Fatalf("migrate up: %v", err)
		}
		logger.Print("migrate up complete")
	case "down":
		err = m.Down()
		if errors.Is(err, migrate.ErrNoChange) {
			logger.Print("no change")
			return
		}
		if err != nil {
			logger.Fatalf("migrate down: %v", err)
		}
		logger.Print("migrate down complete")
	case "version":
		version, dirty, err := m.Version()
		if errors.Is(err, migrate.ErrNilVersion) {
			fmt.Println("version=none dirty=false")
			return
		}
		if err != nil {
			logger.Fatalf("read version: %v", err)
		}
		fmt.Printf("version=%d dirty=%t\n", version, dirty)
	default:
		logger.Fatalf("unsupported command %q", os.Args[1])
	}
}
