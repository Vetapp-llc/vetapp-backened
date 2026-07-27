package database

import (
	"fmt"
	"log"
	"os"
	"time"

	"vetapp-backend/internal/config"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Connect establishes a connection to PostgreSQL via GORM.
// It pings the database to verify connectivity.
func Connect(cfg *config.Config) (*gorm.DB, error) {
	// In production we want Warn-level — Info logs every query and is
	// expensive on a busy instance. Local dev keeps the verbose logger
	// because seeing the SQL is invaluable when debugging.
	logLevel := logger.Info
	if os.Getenv("RAILWAY_ENVIRONMENT") != "" || os.Getenv("ENV") == "production" {
		logLevel = logger.Warn
	}

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  cfg.DSN(),
		PreferSimpleProtocol: true, // disables prepared statement cache (required for PgBouncer/Supabase)
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Verify connection with a ping
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying DB: %w", err)
	}
	// Bound the pool — Supabase / PgBouncer enforces connection caps,
	// and an unbounded local pool can saturate the proxy.
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	log.Println("Connected to database successfully")
	return db, nil
}
