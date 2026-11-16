package migrations

import "gorm.io/gorm"

func init() {
	Register(Migration{
		ID: "021_account_deletion_files",
		Up: func(db *gorm.DB) error {
			// Outside the exposed public schema; contains only pending object
			// keys, never account credentials or medical-record contents.
			return db.Exec(`
				CREATE SCHEMA IF NOT EXISTS vetapp_private;
				REVOKE ALL ON SCHEMA vetapp_private FROM PUBLIC;
				CREATE TABLE IF NOT EXISTS vetapp_private.file_deletions (
					object_key TEXT PRIMARY KEY,
					created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
				);
				ALTER TABLE vetapp_private.file_deletions ENABLE ROW LEVEL SECURITY;
				REVOKE ALL ON vetapp_private.file_deletions FROM PUBLIC;
			`).Error
		},
	})
}
