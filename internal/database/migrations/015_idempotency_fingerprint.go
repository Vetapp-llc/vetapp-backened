package migrations

import "gorm.io/gorm"

// 015 — remember what each idempotency key was used for, so a key reused
// with a different request body (another amount or payment method) is
// refused instead of silently returning the first request's result.
func init() {
	Register(Migration{
		ID: "015_idempotency_fingerprint",
		Up: func(db *gorm.DB) error {
			return db.Exec(`ALTER TABLE idempotency_keys ADD COLUMN IF NOT EXISTS fingerprint TEXT NOT NULL DEFAULT ''`).Error
		},
	})
}
