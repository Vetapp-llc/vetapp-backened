package migrations

import (
	"log"

	"gorm.io/gorm"
)

// Migration represents a single database migration.
type Migration struct {
	ID   string
	Up   func(db *gorm.DB) error
	Down func(db *gorm.DB) error
}

// registry holds all registered migrations in order.
var registry []Migration

// Register adds a migration to the registry. Call this from init() in each migration file.
func Register(m Migration) {
	registry = append(registry, m)
}

// migrationAdvisoryLockKey is a process-global integer used with
// pg_advisory_lock so that multiple backend instances starting at the
// same time (e.g. Railway autoscale) serialize their migration runs
// instead of racing on identical CREATE statements. The exact number
// doesn't matter — just stays stable across deploys.
const migrationAdvisoryLockKey = 728145

// Run applies all pending migrations.
// It creates a migrations tracking table if it doesn't exist,
// then runs each migration that hasn't been applied yet.
//
// Holds a Postgres advisory lock for the duration so two booting
// instances can't apply the same migration concurrently.
func Run(db *gorm.DB) error {
	// Acquire advisory lock — blocks until any other migrating instance
	// finishes. Postgres releases it automatically when the session
	// ends; we also unlock explicitly for cleanliness.
	if err := db.Exec(`SELECT pg_advisory_lock(?)`, migrationAdvisoryLockKey).Error; err != nil {
		return err
	}
	defer db.Exec(`SELECT pg_advisory_unlock(?)`, migrationAdvisoryLockKey)

	// Create tracking table
	if err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id VARCHAR(255) PRIMARY KEY,
			applied_at TIMESTAMP DEFAULT NOW()
		)
	`).Error; err != nil {
		return err
	}

	for _, m := range registry {
		var count int64
		db.Raw("SELECT COUNT(*) FROM schema_migrations WHERE id = ?", m.ID).Scan(&count)
		if count > 0 {
			continue
		}

		log.Printf("[migration] applying: %s", m.ID)
		if err := m.Up(db); err != nil {
			return err
		}
		if err := db.Exec("INSERT INTO schema_migrations (id) VALUES (?)", m.ID).Error; err != nil {
			return err
		}
		log.Printf("[migration] applied: %s", m.ID)
	}

	return nil
}
