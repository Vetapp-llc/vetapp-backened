package migrations

import "gorm.io/gorm"

// 012 — indexes for the filters every request actually uses.
//
// Migration 008 added trigram indexes for the clinic's *search* box and
// an index for per-pet procedure history, but the plain equality
// filters that back the busiest screens were left unindexed. Measured
// against production data (2026-08-02) before this migration:
//
//	owner pet list      pets.uuid = ?            1,180 ms  seq scan
//	daily revenue       paymethod.zip + date     1,308 ms  seq scan
//	shop sales          shop.zip                   131 ms  seq scan
//	appointments        operationdate.owner        118 ms  seq scan
//	payment history     paymethod.zip               37 ms  seq scan
//
// After: every one plans as an index scan at 0.05–0.16 ms server time.
// The two worst are the mobile home screen and the clinic dashboard —
// the first thing each type of user sees on opening the app.
//
// Why this matters beyond latency: a sequential scan reads the whole
// table into shared buffers. At 146k/88k/32k rows and 1,000 concurrent
// users those scans evict each other's cache and saturate I/O, so the
// failure mode under load is not "a bit slower" but a database that
// falls over. Indexes turn each of these into a handful of page reads.
//
// Column choices follow the legacy schema, which is inconsistent by
// table (see models): `pets.uuid` is the OWNER's personal ID, `pets.vet`
// and `paymethod.zip` / `shop.zip` / `prices.zip` are the clinic code,
// while `vaccination.sk` / `operationdate.sk` are the same clinic code
// under a different name.
//
// Composite column order is (filter, sort) so the index can satisfy the
// ORDER BY as well and skip the sort step entirely.
func init() {
	Register(Migration{
		ID: "012_hot_path_indexes",
		Up: func(db *gorm.DB) error {
			stmts := []string{
				// Mobile home screen: every pet belonging to one owner.
				// Also used by the owner-portal detail and calendar paths.
				`CREATE INDEX IF NOT EXISTS idx_pets_uuid ON pets (uuid)`,

				// Clinic pet register, filtered by clinic.
				`CREATE INDEX IF NOT EXISTS idx_pets_vet ON pets (vet)`,

				// Clinic dashboard daily revenue + payment history.
				`CREATE INDEX IF NOT EXISTS idx_paymethod_zip_date ON paymethod (zip, date DESC)`,
				// Per-pet payment lookups (receipts, visit builder).
				`CREATE INDEX IF NOT EXISTS idx_paymethod_uuid ON paymethod (uuid)`,

				// Retail sales list, newest first.
				`CREATE INDEX IF NOT EXISTS idx_shop_zip_date ON shop (zip, date DESC)`,

				// Owner's booked visits, and the clinic's day view.
				`CREATE INDEX IF NOT EXISTS idx_operationdate_owner ON operationdate (owner)`,
				`CREATE INDEX IF NOT EXISTS idx_operationdate_sk_date ON operationdate (sk, date)`,

				// Clinic-scoped procedure register. Complements 008's
				// per-pet index, which does not help when the filter is
				// the clinic rather than the pet.
				`CREATE INDEX IF NOT EXISTS idx_vaccination_sk_date ON vaccination (sk, date DESC)`,

				// Allergies / diseases are looked up per pet on the
				// detail screen and the public QR profile.
				`CREATE INDEX IF NOT EXISTS idx_eals_uuid ON eals (uuid)`,

				// Clinic price list.
				`CREATE INDEX IF NOT EXISTS idx_prices_zip ON prices (zip)`,

				// Public QR lookup resolves a pet by its short code.
				`CREATE INDEX IF NOT EXISTS idx_pets_code ON pets (code)`,
			}
			for _, s := range stmts {
				if err := db.Exec(s).Error; err != nil {
					return err
				}
			}
			// Fresh statistics so the planner uses the new indexes
			// immediately rather than waiting for autovacuum.
			for _, t := range []string{"pets", "paymethod", "shop", "operationdate", "vaccination", "eals", "prices"} {
				if err := db.Exec("ANALYZE " + t).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Down: func(db *gorm.DB) error {
			stmts := []string{
				`DROP INDEX IF EXISTS idx_pets_uuid`,
				`DROP INDEX IF EXISTS idx_pets_vet`,
				`DROP INDEX IF EXISTS idx_paymethod_zip_date`,
				`DROP INDEX IF EXISTS idx_paymethod_uuid`,
				`DROP INDEX IF EXISTS idx_shop_zip_date`,
				`DROP INDEX IF EXISTS idx_operationdate_owner`,
				`DROP INDEX IF EXISTS idx_operationdate_sk_date`,
				`DROP INDEX IF EXISTS idx_vaccination_sk_date`,
				`DROP INDEX IF EXISTS idx_eals_uuid`,
				`DROP INDEX IF EXISTS idx_prices_zip`,
				`DROP INDEX IF EXISTS idx_pets_code`,
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
