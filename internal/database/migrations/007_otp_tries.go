package migrations

import "gorm.io/gorm"

// 007 — add tries counter to otp_codes.
//
// Backs the OTP brute-force guard added in handlers/auth.go::OTPVerify.
// Without this counter an attacker can sweep the 10⁶ code space in well
// under the 60s TTL. We lock the OTP after 5 wrong attempts.
func init() {
	Register(Migration{
		ID: "007_add_tries_to_otp",
		Up: func(db *gorm.DB) error {
			// `otp_codes` historically came from the PHP app and was
			// created out-of-band — fresh deployments (and any DB that
			// never ran the legacy importer) don't have it. Create it
			// if missing so the rest of the OTP code path is usable.
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS otp_codes (
					id          BIGSERIAL PRIMARY KEY,
					phone       VARCHAR(32) NOT NULL,
					code        VARCHAR(12) NOT NULL,
					type        VARCHAR(32) NOT NULL,
					used        BOOLEAN NOT NULL DEFAULT FALSE,
					expires_at  TIMESTAMP NOT NULL,
					created_at  TIMESTAMP NOT NULL DEFAULT NOW()
				)`,
				`CREATE INDEX IF NOT EXISTS idx_otp_codes_phone ON otp_codes (phone)`,
				`ALTER TABLE otp_codes ADD COLUMN IF NOT EXISTS tries INT NOT NULL DEFAULT 0`,
			}
			for _, s := range stmts {
				if err := db.Exec(s).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Down: func(db *gorm.DB) error {
			return db.Exec(`ALTER TABLE otp_codes DROP COLUMN IF EXISTS tries;`).Error
		},
	})
}
