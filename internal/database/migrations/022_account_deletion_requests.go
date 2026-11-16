package migrations

import "gorm.io/gorm"

func init() {
	Register(Migration{
		ID: "022_account_deletion_requests",
		Up: func(db *gorm.DB) error {
			return db.Exec(`
				CREATE SCHEMA IF NOT EXISTS vetapp_private;
				REVOKE ALL ON SCHEMA vetapp_private FROM PUBLIC;
				CREATE TABLE IF NOT EXISTS vetapp_private.account_deletion_requests (
					id BIGSERIAL PRIMARY KEY,
					user_id BIGINT UNIQUE,
					name TEXT NOT NULL,
					email TEXT NOT NULL,
					personal_id TEXT NOT NULL,
					requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					due_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '7 days',
					completed_at TIMESTAMPTZ,
					completed_by BIGINT
				);
				ALTER TABLE vetapp_private.account_deletion_requests ENABLE ROW LEVEL SECURITY;
				REVOKE ALL ON vetapp_private.account_deletion_requests FROM PUBLIC;
			`).Error
		},
	})
}
