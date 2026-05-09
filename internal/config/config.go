package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// Config holds all configuration for the application.
// Values are loaded from environment variables (or .env file in development).
type Config struct {
	Port string

	// Database
	DBHost    string
	DBPort    string
	DBUser    string
	DBPass    string
	DBName    string
	DBSSLMode string

	// JWT
	JWTSecret        string
	JWTRefreshSecret string

	// AES encryption salt (legacy password compat)
	AESSalt string

	// SMS (smsoffice.ge)
	SMSApiKey string
	SMSSender string
	SMSURL    string

	// Email (Resend). Empty ResendAPIKey disables email sending —
	// SendEmailVerification falls back to its no-op stub so the
	// integration is opt-in via env.
	ResendAPIKey string
	// Sender address. Format accepted by Resend: "Name <email@domain>"
	// or just "email@domain". Until your domain is verified in Resend,
	// only `onboarding@resend.dev` works (and Resend only delivers to
	// the API-key owner's email).
	ResendFrom string
	// Public base URL the email-verification link points back to. The
	// link is `<EmailVerifyBaseURL>/api/auth/email/verify/confirm?token=...`.
	// Falls back to BaseURL when unset.
	EmailVerifyBaseURL string

	// iPay.ge
	IPayClientID  string
	IPaySecretKey string
	IPayURL       string

	// Public base URL (for iPay callback etc.)
	BaseURL string
}

// Load reads configuration from environment variables.
// In development, it first loads .env file if present.
func Load() (*Config, error) {
	// Load .env file if it exists (ignored in production)
	godotenv.Load()

	cfg := &Config{
		Port:             getEnv("PORT", "8080"),
		DBHost:           getEnv("DB_HOST", ""),
		DBPort:           getEnv("DB_PORT", "5432"),
		DBUser:           getEnv("DB_USER", ""),
		DBPass:           getEnv("DB_PASS", ""),
		DBName:           getEnv("DB_NAME", "postgres"),
		DBSSLMode:        getEnv("DB_SSLMODE", "require"),
		JWTSecret:        getEnv("JWT_SECRET", ""),
		JWTRefreshSecret: getEnv("JWT_REFRESH_SECRET", ""),
		AESSalt:          getEnv("AES_SALT", ""),
		SMSApiKey:        getEnv("SMS_API_KEY", ""),
		SMSSender:        getEnv("SMS_SENDER", "V E T A P P"),
		SMSURL:           getEnv("SMS_URL", "https://smsoffice.ge/api/v2/send"),
		// Resend defaults to the sandbox sender so an unconfigured
		// install still gets coherent error messages from Resend rather
		// than 422 "missing from".
		ResendAPIKey:       getEnv("RESEND_API_KEY", ""),
		ResendFrom:         getEnv("RESEND_FROM", "VetApp <onboarding@resend.dev>"),
		EmailVerifyBaseURL: getEnv("EMAIL_VERIFY_BASE_URL", ""),
		IPayClientID:       getEnv("IPAY_CLIENT_ID", ""),
		IPaySecretKey:    getEnv("IPAY_SECRET_KEY", ""),
		IPayURL:          ipayURL(),
		BaseURL:          getEnv("BASE_URL", "http://localhost:8080"),
	}

	// Validate required fields
	if os.Getenv("DATABASE_URL") == "" && (cfg.DBHost == "" || cfg.DBUser == "" || cfg.DBPass == "") {
		return nil, fmt.Errorf("DATABASE_URL or DB_HOST/DB_USER/DB_PASS are required")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}
	if cfg.AESSalt == "" {
		return nil, fmt.Errorf("AES_SALT is required")
	}

	return cfg, nil
}

// DSN returns the PostgreSQL connection string for GORM.
func (c *Config) DSN() string {
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		return dbURL
	}
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBUser, c.DBPass, c.DBName, c.DBSSLMode,
	)
}

// ipayURL returns the iPay base URL.
// In dev/testflight (non-production) it defaults to the test gateway;
// in production it defaults to the real gateway.
// Can always be overridden by setting IPAY_URL explicitly.
func ipayURL() string {
	if v := os.Getenv("IPAY_URL"); v != "" {
		return v
	}
	env := os.Getenv("RAILWAY_ENVIRONMENT")
	if env == "production" || os.Getenv("ENV") == "production" {
		return "https://ipay.ge"
	}
	return "https://dev.ipay.ge"
}

// getEnv reads an env var or returns a default value.
func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
