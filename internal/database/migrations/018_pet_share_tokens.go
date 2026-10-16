package migrations

import "gorm.io/gorm"

// 018 — share tokens for the public pet pages.
//
// The public profile and history were served by bare pet id, so anyone
// could walk the ids and read every pet's diagnoses. An owner now shares
// a pet through a token: a random link token (QR code, 7 days) or a
// 6-digit PIN (1 hour). pets.code, the legacy 4-digit field, is left
// alone: PHP writes 1313 into it for thousands of pets.
func init() {
	Register(Migration{
		ID: "018_pet_share_tokens",
		Up: func(db *gorm.DB) error {
			return db.Exec(`
				CREATE TABLE IF NOT EXISTS pet_share_tokens (
					token      TEXT PRIMARY KEY,
					pet_id     BIGINT NOT NULL,
					kind       TEXT NOT NULL CHECK (kind IN ('pin', 'link')),
					expires_at TIMESTAMPTZ NOT NULL,
					created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
				);
				CREATE INDEX IF NOT EXISTS idx_pet_share_tokens_pet ON pet_share_tokens (pet_id, kind);
			`).Error
		},
	})
}
