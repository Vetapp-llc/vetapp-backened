package migrations

import "gorm.io/gorm"

// 023 — app-owned tables for the owner app's pet lifecycle and the
// home-procedure schedule.
//
//   - owner_hidden_pets: an owner "deletes" a pet from the app. The pets
//     row stays (clinics keep the history); the pet just disappears from
//     that owner's views.
//   - pet_transfers: audit trail of an owner handing a pet to another
//     owner (pets.uuid is rewritten, so the old owner is recorded here).
//   - home_procedure_schedules: how many times a day a home procedure is
//     given, and at what times (for the app's reminders). The legacy
//     homepro table has no column for it.
//
// None of these exist in MySQL, so the sync never touches them and they
// need no app_changes trigger. Row-level security on, like the other
// app-only tables (migration 019): the backend owns them and bypasses it.
func init() {
	Register(Migration{
		ID: "023_owner_pet_lifecycle",
		Up: func(db *gorm.DB) error {
			return db.Exec(`
				CREATE TABLE IF NOT EXISTS owner_hidden_pets (
					pet_id            BIGINT NOT NULL,
					owner_personal_id TEXT NOT NULL,
					hidden_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					PRIMARY KEY (pet_id, owner_personal_id)
				);
				CREATE TABLE IF NOT EXISTS pet_transfers (
					id               BIGSERIAL PRIMARY KEY,
					pet_id           BIGINT NOT NULL,
					from_personal_id TEXT NOT NULL,
					to_personal_id   TEXT NOT NULL,
					transferred_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
				);
				CREATE INDEX IF NOT EXISTS idx_pet_transfers_pet ON pet_transfers (pet_id);
				CREATE TABLE IF NOT EXISTS home_procedure_schedules (
					homepro_id    BIGINT PRIMARY KEY,
					times_per_day INT NOT NULL,
					times         TEXT NOT NULL
				);
				ALTER TABLE owner_hidden_pets ENABLE ROW LEVEL SECURITY;
				ALTER TABLE pet_transfers ENABLE ROW LEVEL SECURITY;
				ALTER TABLE home_procedure_schedules ENABLE ROW LEVEL SECURITY;
				DO $$
				BEGIN
					IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon') THEN
						REVOKE ALL ON owner_hidden_pets, pet_transfers, home_procedure_schedules FROM anon, authenticated;
						REVOKE ALL ON SEQUENCE pet_transfers_id_seq FROM anon, authenticated;
					END IF;
				END $$;
			`).Error
		},
	})
}
