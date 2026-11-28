package migrations

import "gorm.io/gorm"

func init() {
	Register(Migration{
		ID: "025_pet_transfer_requests",
		Up: func(db *gorm.DB) error {
			return db.Exec(`
				CREATE TABLE IF NOT EXISTS pet_transfer_requests (
					id BIGSERIAL PRIMARY KEY,
					pet_id BIGINT NOT NULL,
					sender_user_id BIGINT NOT NULL,
					recipient_user_id BIGINT NOT NULL,
					from_personal_id TEXT NOT NULL,
					to_personal_id TEXT NOT NULL,
					status TEXT NOT NULL DEFAULT 'pending'
						CHECK (status IN ('pending', 'accepted', 'declined', 'cancelled')),
					created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					resolved_at TIMESTAMPTZ
				);
				CREATE UNIQUE INDEX IF NOT EXISTS pet_transfer_requests_pending_pet
					ON pet_transfer_requests (pet_id) WHERE status = 'pending';
				CREATE INDEX IF NOT EXISTS pet_transfer_requests_sender
					ON pet_transfer_requests (sender_user_id, id DESC);
				CREATE INDEX IF NOT EXISTS pet_transfer_requests_recipient
					ON pet_transfer_requests (recipient_user_id, id DESC);
				CREATE TABLE IF NOT EXISTS owner_push_devices (
					token TEXT PRIMARY KEY,
					user_id BIGINT NOT NULL,
					updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
				);
				CREATE INDEX IF NOT EXISTS owner_push_devices_user ON owner_push_devices (user_id, updated_at DESC);
				ALTER TABLE pet_transfer_requests ENABLE ROW LEVEL SECURITY;
				ALTER TABLE owner_push_devices ENABLE ROW LEVEL SECURITY;
				DO $$
				BEGIN
					IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon') THEN
						REVOKE ALL ON pet_transfer_requests, owner_push_devices FROM anon, authenticated;
						REVOKE ALL ON SEQUENCE pet_transfer_requests_id_seq FROM anon, authenticated;
					END IF;
				END $$;
			`).Error
		},
	})
}
