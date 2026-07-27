package migrations

import "gorm.io/gorm"

// 004 — add password_hash column to memberlogin_members.
//
// Backs the AES-128-ECB → bcrypt migration. Stays NULL until the user
// next logs in successfully (Login backfills via dual-read), or until
// they register / change / reset their password (those flows now write
// bcrypt directly). The legacy `password` (bytea AES) column stays
// populated for one release as a safety net before being dropped.
func init() {
	Register(Migration{
		ID: "004_add_password_hash",
		Up: func(db *gorm.DB) error {
			return db.Exec(`
				ALTER TABLE memberlogin_members
				ADD COLUMN IF NOT EXISTS password_hash TEXT NULL;
			`).Error
		},
		Down: func(db *gorm.DB) error {
			return db.Exec(`ALTER TABLE memberlogin_members DROP COLUMN IF EXISTS password_hash;`).Error
		},
	})
}
