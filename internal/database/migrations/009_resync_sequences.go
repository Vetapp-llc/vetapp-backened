package migrations

import (
	"fmt"
	"log"

	"gorm.io/gorm"
)

// 009 — repair identity sequences left behind by the MySQL import.
//
// The MySQL → Supabase sync (cmd/sync) inserts rows with their original
// primary keys so IDs stay stable across the two systems. Postgres
// sequences are only advanced by nextval(), never by an explicit-ID
// INSERT, so after every import each sequence still points at whatever
// value it held before. The next natural INSERT then reuses an ID that
// already exists and dies with:
//
//	duplicate key value violates unique constraint "pets_pkey" (SQLSTATE 23505)
//
// Observed on production 2026-07-26: 11 tables were behind, including
// pets (1454), vaccination (10633), paymethod (35381) and shop (3568).
// The practical effect was total write failure on the core flows — no
// owner could add a pet, and no procedure could be recorded by anyone.
//
// This migration walks every sequence-backed column in the public
// schema and fast-forwards any sequence that trails MAX(id). It is
// data-dependent rather than schema-dependent, so it is written to be
// safely re-runnable; cmd/sync also calls ResyncSequences after each
// import so the drift cannot silently return.
//
// setval(..., false) is deliberate: it makes the NEXT nextval() return
// exactly the value passed, so we pass MAX(id)+1. Empty tables are
// reset to 1.
func init() {
	Register(Migration{
		ID: "009_resync_sequences",
		Up: func(db *gorm.DB) error {
			fixed, err := ResyncSequences(db)
			if err != nil {
				return err
			}
			log.Printf("[migration] 009: resynced %d sequence(s)", fixed)
			return nil
		},
		// Deliberately a no-op. "Undoing" a sequence repair would mean
		// rewinding sequences into a range where IDs already exist —
		// i.e. deliberately reintroducing the duplicate-key failure.
		Down: func(db *gorm.DB) error { return nil },
	})
}

// SequenceFix describes one repaired sequence.
type SequenceFix struct {
	Table    string
	Column   string
	Sequence string
	LastVal  int64
	MaxID    int64
}

// ResyncSequences fast-forwards every sequence in the public schema
// that has fallen behind the max value actually present in its column.
// It returns the number of sequences it had to correct.
//
// Exported so cmd/sync can call it directly after an import rather than
// relying on a server restart to run migrations.
func ResyncSequences(db *gorm.DB) (int, error) {
	var cols []struct {
		TableName  string
		ColumnName string
	}
	if err := db.Raw(`
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND column_default LIKE 'nextval%'
	`).Scan(&cols).Error; err != nil {
		return 0, fmt.Errorf("list sequence columns: %w", err)
	}

	fixed := 0
	for _, c := range cols {
		var seqName string
		if err := db.Raw(`SELECT pg_get_serial_sequence(?, ?)`,
			"public."+c.TableName, c.ColumnName).Scan(&seqName).Error; err != nil {
			return fixed, fmt.Errorf("resolve sequence for %s.%s: %w", c.TableName, c.ColumnName, err)
		}
		if seqName == "" {
			continue
		}

		var lastVal int64
		if err := db.Raw(fmt.Sprintf(`SELECT last_value FROM %s`, seqName)).Scan(&lastVal).Error; err != nil {
			return fixed, fmt.Errorf("read %s: %w", seqName, err)
		}

		// Identifiers are quoted rather than parameterised because
		// table/column names cannot be bound as query parameters. The
		// values come from information_schema, not from user input.
		var maxID int64
		if err := db.Raw(fmt.Sprintf(`SELECT COALESCE(MAX(%q), 0) FROM %q`,
			c.ColumnName, c.TableName)).Scan(&maxID).Error; err != nil {
			return fixed, fmt.Errorf("read max(%s.%s): %w", c.TableName, c.ColumnName, err)
		}

		if lastVal >= maxID {
			continue
		}

		next := maxID + 1
		if next < 1 {
			next = 1
		}
		// is_called=false → the next nextval() returns `next` itself.
		if err := db.Exec(fmt.Sprintf(`SELECT setval('%s', %d, false)`, seqName, next)).Error; err != nil {
			return fixed, fmt.Errorf("setval %s: %w", seqName, err)
		}
		log.Printf("[sequences] %s.%s: %d → %d", c.TableName, c.ColumnName, lastVal, next)
		fixed++
	}

	return fixed, nil
}
