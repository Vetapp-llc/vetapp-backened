package migrations

import "gorm.io/gorm"

// 003 — create email_verification_tokens.
//
// Backs the email-verification flow added in `services/email.go` +
// `auth.go::SendEmailVerification` / `ConfirmEmailVerification`.
// Schema kept deliberately minimal: a token is single-use, scoped to a
// user, with an expiry timestamp.
func init() {
	Register(Migration{
		ID: "003_create_email_verification_tokens",
		Up: func(db *gorm.DB) error {
			return db.Exec(`
				CREATE TABLE IF NOT EXISTS email_verification_tokens (
					id          BIGSERIAL PRIMARY KEY,
					user_id     BIGINT NOT NULL,
					token       VARCHAR(64) NOT NULL UNIQUE,
					expires_at  TIMESTAMP NOT NULL,
					used_at     TIMESTAMP NULL,
					created_at  TIMESTAMP NOT NULL DEFAULT NOW()
				);
				CREATE INDEX IF NOT EXISTS idx_evt_user_id ON email_verification_tokens (user_id);
				CREATE INDEX IF NOT EXISTS idx_evt_expires_at ON email_verification_tokens (expires_at);
			`).Error
		},
		Down: func(db *gorm.DB) error {
			return db.Exec(`DROP TABLE IF EXISTS email_verification_tokens`).Error
		},
	})
}
