package migrations

import "gorm.io/gorm"

// 014 — idempotency keys for the two writes a clinic retries after a
// network error: recording a procedure and recording a payment.
//
// The web client sends a stable Idempotency-Key per procedure and per
// visit payment. If the first request committed but its response was
// lost, the retry finds the key and gets the original record back
// instead of creating a second procedure or charging twice. The primary
// key makes concurrent retries of one key serialise on the insert.
func init() {
	Register(Migration{
		ID: "014_idempotency_keys",
		Up: func(db *gorm.DB) error {
			return db.Exec(`CREATE TABLE IF NOT EXISTS idempotency_keys (
				user_id     BIGINT NOT NULL,
				key         TEXT NOT NULL,
				route       TEXT NOT NULL,
				resource_id BIGINT NOT NULL DEFAULT 0,
				created_at  TIMESTAMP NOT NULL DEFAULT NOW(),
				PRIMARY KEY (user_id, key)
			)`).Error
		},
	})
}
