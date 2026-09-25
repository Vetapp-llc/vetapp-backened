package migrations

import "gorm.io/gorm"

// 013 — indexes for the PHP-parity lookups, and the SMS send log.
//
// Indexes, each backing a filter added with the clinic-portal parity work:
//
//	TRIM(pets.chip)                   microchip lookup (vet/search3.php); trimmed
//	                                  because 151 legacy chips carry stray spaces
//	memberlogin_members.last_name     owner by personal ID; certificate owner block
//	operationdate (sk, date2)         appointments by appointment day — date2 is
//	                                  the appointment day, `date` the booking day,
//	                                  so 012's (sk, date) index no longer applies
//	vaccination (date2)               the daily SMS reminder pass
//	(TRIM(pets.uuid), id DESC)        a clinic's owner lookup, newest first; trimmed
//	                                  because 43 legacy owner IDs carry spaces, and
//	                                  composite because with (uuid) alone the
//	                                  planner walked the primary key (13k rows)
//
// CONCURRENTLY so building them on the live tables does not block writes.
// That cannot run inside a transaction, which is fine: the migration
// runner executes each statement on its own.
//
// sms_runs records each day's send per kind. Its primary key makes a
// second send of the same kind on the same day fail, so a double click
// or two backend instances cannot text customers twice.
func init() {
	Register(Migration{
		ID: "013_parity_indexes_and_sms_runs",
		Up: func(db *gorm.DB) error {
			stmts := []string{
				`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_pets_chip_trim ON pets (TRIM(chip))`,
				`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_members_last_name ON memberlogin_members (last_name)`,
				`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_operationdate_sk_date2 ON operationdate (sk, date2)`,
				`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_vaccination_date2 ON vaccination (date2)`,
				`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_pets_uuid_trim_id ON pets (TRIM(uuid), id DESC)`,
				`CREATE TABLE IF NOT EXISTS sms_runs (
					run_date   TEXT NOT NULL,
					kind       TEXT NOT NULL,
					recipients INT NOT NULL DEFAULT 0,
					sent       INT NOT NULL DEFAULT 0,
					errors     INT NOT NULL DEFAULT 0,
					created_at TIMESTAMP NOT NULL DEFAULT NOW(),
					PRIMARY KEY (run_date, kind)
				)`,
			}
			for _, s := range stmts {
				if err := db.Exec(s).Error; err != nil {
					return err
				}
			}
			return nil
		},
	})
}
