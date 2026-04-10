package main

import (
	"log"
	"net/http"

	"vetapp-backend/internal/config"
	"vetapp-backend/internal/database"
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
	// Load configuration from environment / .env file
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Connect to database
	db, err := database.Connect(cfg)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// Initialize services
	authService := services.NewAuthService(cfg)
	smsService := services.NewSMSService(cfg)
	ipayService := services.NewIPayService(cfg)

	// Setup router with all routes
	r := router.Setup(db, authService, smsService, ipayService)

	// Start server
	addr := ":" + cfg.Port
	log.Printf("Server starting on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
