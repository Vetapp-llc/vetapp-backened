package migrations

import (
	"log"

	"gorm.io/gorm"
)

// 010 — backfill vaccination.date3 for owner-created reminders.
//
// `date3` holds the reminder date in the legacy comma format
// ("2027,03,18") that mirrors `date2` ("2027-03-18"). The clinic's PHP
// tooling writes and reads both. The mobile owner-portal endpoint never
// populated it, so every procedure an owner recorded themselves landed
// with an empty date3 — and the owner calendar, which filtered on
// date3, silently dropped exactly those rows.
//
// The read path and the write path are both fixed in code (see
// OwnerPortalHandler.Calendar and CreateProcedure). This migration
// repairs the rows already in the table so the two columns agree for
// anything reading date3 directly.
//
// Scope is deliberately narrow: only rows whose date2 is a well-formed
// ISO date AND whose date3 is empty. Legacy sentinels (",-1,", "--")
// are left untouched — they are meaningful to the PHP side, and
// "normalising" them would be a data-loss bug dressed up as a cleanup.
func init() {
	Register(Migration{
		ID: "010_backfill_procedure_date3",
		Up: func(db *gorm.DB) error {
			res := db.Exec(`
				UPDATE vaccination
				SET date3 = SUBSTRING(date2 FROM 1 FOR 4) || ',' ||
				            SUBSTRING(date2 FROM 6 FOR 2) || ',' ||
				            SUBSTRING(date2 FROM 9 FOR 2)
				WHERE date2 ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'
				  AND (date3 IS NULL OR date3 = '')
			`)
			if res.Error != nil {
				return res.Error
			}
			log.Printf("[migration] 010: backfilled date3 on %d row(s)", res.RowsAffected)
			return nil
		},
		// Not reversible in any useful sense: we cannot distinguish rows
		// this migration filled from rows the clinic tooling filled
		// legitimately, and blanking the latter would reintroduce the
		// disappearing-reminder bug.
		Down: func(db *gorm.DB) error { return nil },
	})
}
