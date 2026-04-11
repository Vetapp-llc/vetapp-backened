package migrations

import "gorm.io/gorm"

func init() {
	Register(Migration{
		ID: "001_add_address_to_users",
		Up: func(db *gorm.DB) error {
			return db.Exec(`ALTER TABLE memberlogin_members ADD COLUMN IF NOT EXISTS address TEXT DEFAULT ''`).Error
		},
		Down: func(db *gorm.DB) error {
			return db.Exec(`ALTER TABLE memberlogin_members DROP COLUMN IF EXISTS address`).Error
		},
	})
}
