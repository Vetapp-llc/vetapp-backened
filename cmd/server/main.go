package main

import (
	"log/slog"
	"net/http"
	"os"

	"vetapp-backend/internal/config"
	"vetapp-backend/internal/database"
	"vetapp-backend/internal/database/migrations"
	"vetapp-backend/internal/router"
	"vetapp-backend/internal/services"
)

// @title VetApp API
// @version 1.0
// @description Veterinary clinic management API
// @host localhost:8080
// @BasePath /api
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
func main() {
	// Configure structured logging: JSON in production, text in dev
	var handler slog.Handler
	if os.Getenv("RAILWAY_ENVIRONMENT") != "" || os.Getenv("ENV") == "production" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	}
	slog.SetDefault(slog.New(handler))

	// Load configuration from environment / .env file
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Connect to database
	db, err := database.Connect(cfg)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	// Run database migrations
	if err := migrations.Run(db); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	// Initialize services
	authService := services.NewAuthService(cfg)
	smsService := services.NewSMSService(cfg)
	ipayService := services.NewIPayService(cfg)

	// Setup router with all routes
	r := router.Setup(db, authService, smsService, ipayService)

	// Start server
	addr := ":" + cfg.Port
	slog.Info("server starting", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}
