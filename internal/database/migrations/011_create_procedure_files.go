package migrations

import "gorm.io/gorm"

// 011 — attachments for procedure records (lab results, scans, PDFs).
//
// Partner request: laboratory records need files attached. The legacy
// `analysefile` table cannot be reused — it stores only a display
// string and a case id, with no reference to an actual stored object:
//
//	id | location ("2024-09-09  - სისხლის ბიოქიმია") | caseid
//
// so there is no way to fetch the file it names. This table stores a
// real object key instead, plus the metadata needed to render a list
// without touching storage.
//
// Files live in Supabase Storage under `procedure-files/<pet>/<uuid>`;
// only the key is kept here. Downloads go through short-lived signed
// URLs minted per request, so the bucket stays private and a leaked
// URL expires on its own — see services/storage.go.
//
// `procedure_id` is the `vaccination.id` the file belongs to and is
// deliberately NOT a foreign key: `vaccination` is a legacy table
// re-synced from MySQL, and a FK would make the sync fail on any row
// ordering it doesn't control. The owning pet is denormalised onto
// `pet_id` so authorisation can be checked without a join.
func init() {
	Register(Migration{
		ID: "011_create_procedure_files",
		Up: func(db *gorm.DB) error {
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS procedure_files (
					id           SERIAL PRIMARY KEY,
					procedure_id BIGINT       NOT NULL,
					pet_id       TEXT         NOT NULL,
					object_key   TEXT         NOT NULL UNIQUE,
					file_name    TEXT         NOT NULL,
					content_type TEXT         NOT NULL DEFAULT '',
					size_bytes   BIGINT       NOT NULL DEFAULT 0,
					uploaded_by  BIGINT       NOT NULL DEFAULT 0,
					created_at   TIMESTAMP    NOT NULL DEFAULT NOW()
				)`,
				// Listing files for one procedure is the hot path.
				`CREATE INDEX IF NOT EXISTS idx_procedure_files_procedure
					ON procedure_files (procedure_id)`,
				// Authorisation filters by pet before returning anything.
				`CREATE INDEX IF NOT EXISTS idx_procedure_files_pet
					ON procedure_files (pet_id)`,
			}
			for _, s := range stmts {
				if err := db.Exec(s).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Down: func(db *gorm.DB) error {
			return db.Exec(`DROP TABLE IF EXISTS procedure_files`).Error
		},
	})
}
