package migrations

import "gorm.io/gorm"

// 008 — performance indexes for hot read paths.
//
// Backs the slow-search and N+1 audit findings:
//
//   - pets ILIKE '%term%' search across first_name/phone/email/code:
//     leading-wildcard ILIKE can't use a B-tree index, so the search
//     was a full table scan per request. pg_trgm GIN handles trigram
//     ILIKE in O(log n).
//   - vaccination(uuid, tp, date DESC): Certificate / Procedures /
//     pet detail all filter by (uuid, tp) and order by date — without
//     a composite, each is a per-pet seq scan once history grows.
//   - memberlogin_members(email) and (phone): login + password reset
//     hot paths; verified missing on Supabase production.
//
// IF NOT EXISTS so re-running the migration on partially-migrated
// schemas is safe.
func init() {
	Register(Migration{
		ID: "008_search_and_perf_indexes",
		Up: func(db *gorm.DB) error {
			stmts := []string{
				`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
				`CREATE INDEX IF NOT EXISTS idx_pets_first_name_trgm
					ON pets USING gin (first_name gin_trgm_ops)`,
				`CREATE INDEX IF NOT EXISTS idx_pets_phone_trgm
					ON pets USING gin (phone gin_trgm_ops)`,
				`CREATE INDEX IF NOT EXISTS idx_pets_email_trgm
					ON pets USING gin (email gin_trgm_ops)`,
				`CREATE INDEX IF NOT EXISTS idx_pets_code_trgm
					ON pets USING gin (code gin_trgm_ops)`,
				`CREATE INDEX IF NOT EXISTS idx_vaccination_uuid_tp_date
					ON vaccination (uuid, tp, date DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_users_email
					ON memberlogin_members (email)`,
				`CREATE INDEX IF NOT EXISTS idx_users_phone
					ON memberlogin_members (phone)`,
			}
			for _, s := range stmts {
				if err := db.Exec(s).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Down: func(db *gorm.DB) error {
			stmts := []string{
				`DROP INDEX IF EXISTS idx_pets_first_name_trgm`,
				`DROP INDEX IF EXISTS idx_pets_phone_trgm`,
				`DROP INDEX IF EXISTS idx_pets_email_trgm`,
				`DROP INDEX IF EXISTS idx_pets_code_trgm`,
				`DROP INDEX IF EXISTS idx_vaccination_uuid_tp_date`,
				`DROP INDEX IF EXISTS idx_users_email`,
				`DROP INDEX IF EXISTS idx_users_phone`,
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
