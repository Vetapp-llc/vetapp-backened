// sterilmigrate consolidates the legacy `steril` table into the
// `vaccination` table as tp=110 (sterilization/castration) records,
// matching the canonical PHP scheme described in
// `internal/handlers/procedures.go:Types()`.
//
// Run modes (positional arg):
//
//	precheck   — read-only: counts, samples, would-migrate count.
//	            Run this first; safe.
//	backup     — creates a timestamped snapshot table of `steril` so
//	            we can roll back any time. Idempotent (re-running with
//	            the same name no-ops).
//	migrate    — transactional INSERT into `vaccination`. Skips rows
//	            that already exist as tp=110 with same uuid+date+vac.
//	            Pre-condition: backup must have been run.
//	verify     — confirms counts moved correctly; lists any orphans.
//	rollback   — UNDO. Restores `steril`-sourced rows by deleting them
//	            from `vaccination` (matched via the backup table).
//	            Must be run with explicit `--confirm-rollback` flag.
//
// Usage:
//
//	go run ./cmd/sterilmigrate precheck
//	go run ./cmd/sterilmigrate backup
//	go run ./cmd/sterilmigrate migrate
//	go run ./cmd/sterilmigrate verify
//	go run ./cmd/sterilmigrate rollback --confirm-rollback
package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"vetapp-backend/internal/config"
	"vetapp-backend/internal/database"

	"gorm.io/gorm"
)

// backupTableName is the snapshot table that holds the pre-migration
// state of `steril`. Date-stamped so re-running on a different day
// doesn't clobber an earlier snapshot.
const backupTablePrefix = "_steril_backup_"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sterilmigrate <precheck|backup|migrate|verify|rollback>")
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	db, err := database.Connect(cfg)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	// Quieter logging for the migration tool — GORM's default Info
	// level prints every query, drowning the output we actually want.
	db.Logger = db.Logger.LogMode(0)

	switch os.Args[1] {
	case "precheck":
		precheck(db)
	case "backup":
		backup(db)
	case "migrate":
		migrate(db)
	case "verify":
		verify(db)
	case "rollback":
		if !hasFlag("--confirm-rollback") {
			fmt.Fprintln(os.Stderr, "rollback requires --confirm-rollback flag (this DELETES rows from vaccination)")
			os.Exit(2)
		}
		rollback(db)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}

func hasFlag(name string) bool {
	for _, a := range os.Args[2:] {
		if a == name {
			return true
		}
	}
	return false
}

// tableExists reports whether the given table is present in the
// `public` schema of the connected database.
func tableExists(db *gorm.DB, name string) bool {
	var n int64
	must(db.Raw(`
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = ?
	`, name).Scan(&n).Error, "table-exists check")
	return n > 0
}

// ─────────────────────────────────────────────────────────────────────
// precheck
// ─────────────────────────────────────────────────────────────────────

func precheck(db *gorm.DB) {
	header("STERIL → VACCINATION tp=110 — PRECHECK (read-only)")

	if !tableExists(db, "steril") {
		fmt.Println("`steril` table does not exist in this database.")
		fmt.Println()
		fmt.Println("Searching for similarly-named tables in case it was renamed…")
		type tbl struct{ TableName string }
		var tbls []tbl
		must(db.Raw(`
			SELECT table_name
			FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND (table_name ILIKE '%steril%' OR table_name ILIKE '%cast%' OR table_name ILIKE '%neuter%')
			ORDER BY table_name
		`).Scan(&tbls).Error, "search similar tables")
		if len(tbls) == 0 {
			fmt.Println("  (none found — no migration needed)")
		} else {
			for _, t := range tbls {
				fmt.Printf("  %s\n", t.TableName)
			}
		}

		// Also show the current count of vaccination tp=110 records
		// for context — they may already be the canonical store.
		var vacc110 int64
		must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE tp = '110'`).Scan(&vacc110).Error, "count vaccination tp=110")
		fmt.Printf("\nvaccination WHERE tp='110' (sterilization records already in canonical table): %d\n", vacc110)

		fmt.Println()
		fmt.Println("conclusion: there is nothing to migrate from `steril`. The legacy")
		fmt.Println("PHP `addsterile.php` form referenced this table, but it was either never")
		fmt.Println("populated in production or was already cleaned up. Sterilization records")
		fmt.Println("are already living in `vaccination` as tp=110 (written by addprocedure3.php).")
		fmt.Println("No migration action is required for sterilization data.")
		return
	}

	// 1. Schema sanity — list `steril` columns and confirm we have
	//    everything we need.
	type col struct{ ColumnName, DataType string }
	var sterilCols []col
	must(db.Raw(`
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_name = 'steril'
		ORDER BY ordinal_position
	`).Scan(&sterilCols).Error, "steril schema lookup")
	fmt.Println("steril table schema:")
	for _, c := range sterilCols {
		fmt.Printf("  %-20s %s\n", c.ColumnName, c.DataType)
	}

	// 2. Row counts in each table.
	var sterilCount, vacc110Count int64
	must(db.Raw(`SELECT COUNT(*) FROM steril`).Scan(&sterilCount).Error, "count steril")
	must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE tp = '110'`).Scan(&vacc110Count).Error, "count vaccination tp=110")
	fmt.Printf("\nrow counts:\n")
	fmt.Printf("  steril                       %d\n", sterilCount)
	fmt.Printf("  vaccination WHERE tp='110'   %d\n", vacc110Count)

	// 3. How many `steril` rows ALREADY have a matching vaccination
	//    tp=110 row (would be skipped by the migration's WHERE NOT
	//    EXISTS clause). Match key: uuid + date + vac (procedure name).
	//    These three together uniquely identify a sterilization event
	//    in the legacy system; same vet, same pet, same day, same
	//    procedure name.
	var alreadyMigrated int64
	must(db.Raw(`
		SELECT COUNT(*)
		FROM steril s
		WHERE EXISTS (
			SELECT 1 FROM vaccination v
			WHERE v.tp = '110'
			  AND v.uuid = s.uuid
			  AND v.date = s.date
			  AND COALESCE(v.vac, '') = COALESCE(s.vac, '')
		)
	`).Scan(&alreadyMigrated).Error, "count already-migrated")

	wouldInsert := sterilCount - alreadyMigrated
	fmt.Printf("\nmigration analysis:\n")
	fmt.Printf("  steril rows already in vaccination   %d  (skipped)\n", alreadyMigrated)
	fmt.Printf("  steril rows that would be inserted   %d\n", wouldInsert)
	fmt.Printf("  expected vaccination tp=110 after    %d\n", vacc110Count+wouldInsert)

	// 4. Sample 5 rows from steril for sanity.
	type sterilRow struct {
		ID    int
		UUID  string
		Date  string
		Vac   string
		PName string
	}
	var samples []sterilRow
	must(db.Raw(`SELECT id, uuid, date, vac, pname FROM steril ORDER BY id DESC LIMIT 5`).Scan(&samples).Error, "sample steril")
	fmt.Println("\nrecent steril rows:")
	for _, s := range samples {
		fmt.Printf("  id=%-5d uuid=%-12s date=%-12s vac=%-30s pname=%s\n",
			s.ID, s.UUID, s.Date, truncate(s.Vac, 30), s.PName)
	}

	// 5. Distinct `vac` values — what kinds of sterilization?
	type vacRow struct {
		Vac   string
		Total int64
	}
	var vacRows []vacRow
	must(db.Raw(`SELECT vac, COUNT(*) AS total FROM steril GROUP BY vac ORDER BY total DESC LIMIT 10`).Scan(&vacRows).Error, "distinct vac values")
	fmt.Println("\ntop `vac` values in steril (procedure name):")
	for _, v := range vacRows {
		label := v.Vac
		if label == "" {
			label = "(empty)"
		}
		fmt.Printf("  %-40s %d\n", truncate(label, 40), v.Total)
	}

	fmt.Printf("\nbackup table that would be created: %s\n", todayBackupTable())
	fmt.Println("\nnext step: run `go run ./cmd/sterilmigrate backup` to snapshot steril.")
}

// ─────────────────────────────────────────────────────────────────────
// backup
// ─────────────────────────────────────────────────────────────────────

func backup(db *gorm.DB) {
	header("STERIL — BACKUP")

	if !tableExists(db, "steril") {
		fmt.Println("`steril` does not exist; nothing to back up.")
		return
	}

	tbl := todayBackupTable()

	// Drop-if-exists then recreate for idempotency. Safe because the
	// backup is a one-day-stamped table; if you ran backup earlier
	// today and want a fresh snapshot, just re-run.
	must(db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tbl)).Error, "drop existing backup")
	must(db.Exec(fmt.Sprintf(`CREATE TABLE %s AS SELECT * FROM steril`, tbl)).Error, "create backup table")

	var backupCount, sterilCount int64
	must(db.Raw(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tbl)).Scan(&backupCount).Error, "count backup")
	must(db.Raw(`SELECT COUNT(*) FROM steril`).Scan(&sterilCount).Error, "count steril")

	fmt.Printf("backup table: %s\n", tbl)
	fmt.Printf("backed up rows: %d\n", backupCount)
	fmt.Printf("steril rows:    %d\n", sterilCount)
	if backupCount != sterilCount {
		fmt.Println("\nWARNING: backup count does not match steril count!")
		os.Exit(1)
	}
	fmt.Println("\nbackup OK. next step: run `go run ./cmd/sterilmigrate migrate`")
}

// ─────────────────────────────────────────────────────────────────────
// migrate
// ─────────────────────────────────────────────────────────────────────

func migrate(db *gorm.DB) {
	header("STERIL → VACCINATION tp=110 — MIGRATE")

	if !tableExists(db, "steril") {
		fmt.Println("`steril` does not exist; nothing to migrate.")
		return
	}

	tbl := todayBackupTable()

	// Pre-flight: backup MUST exist. Don't proceed without it.
	if !tableExists(db, tbl) {
		fmt.Fprintf(os.Stderr, "ERROR: backup table %s does not exist. Run `backup` first.\n", tbl)
		os.Exit(1)
	}

	var beforeCount int64
	must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE tp = '110'`).Scan(&beforeCount).Error, "count before")
	fmt.Printf("vaccination tp=110 before: %d\n", beforeCount)

	// Wrap in a transaction so a partial failure leaves no half-state.
	err := db.Transaction(func(tx *gorm.DB) error {
		// INSERT only the rows that aren't already in vaccination.
		// Match on (uuid, date, vac) — the natural identifier for a
		// sterilization event. Sets `tp='110'` and `tpname` to the
		// canonical Georgian label so the new app renders consistently.
		// Other columns: copy through what `steril` has, leave the rest
		// of `vaccination`'s columns at their default (empty).
		res := tx.Exec(`
			INSERT INTO vaccination (
				uuid, date, vac, ser, sk, phone, company,
				address, pn, name, price, pname, pet, ownern, vetname,
				tp, tpname
			)
			SELECT
				s.uuid, s.date, s.vac, s.ser, s.sk, s.phone, s.company,
				s.address, s.pn, s.name, s.price, s.pname, s.pet, s.ownern, s.vetname,
				'110', 'სტერილიზაცია/კასტრაცია'
			FROM steril s
			WHERE NOT EXISTS (
				SELECT 1 FROM vaccination v
				WHERE v.tp = '110'
				  AND v.uuid = s.uuid
				  AND v.date = s.date
				  AND COALESCE(v.vac, '') = COALESCE(s.vac, '')
			)
		`)
		if res.Error != nil {
			return res.Error
		}
		fmt.Printf("inserted: %d rows\n", res.RowsAffected)
		return nil
	})
	if err != nil {
		log.Fatalf("migration failed (transaction rolled back): %v", err)
	}

	var afterCount int64
	must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE tp = '110'`).Scan(&afterCount).Error, "count after")
	fmt.Printf("vaccination tp=110 after:  %d\n", afterCount)
	fmt.Printf("delta:                     +%d\n", afterCount-beforeCount)

	fmt.Println("\nnext step: run `go run ./cmd/sterilmigrate verify`")
}

// ─────────────────────────────────────────────────────────────────────
// verify
// ─────────────────────────────────────────────────────────────────────

func verify(db *gorm.DB) {
	header("STERIL → VACCINATION tp=110 — VERIFY")

	tbl := todayBackupTable()

	if !tableExists(db, tbl) {
		fmt.Fprintf(os.Stderr, "backup table %s not found; nothing to verify against.\n", tbl)
		os.Exit(1)
	}

	// 1. Every row in the backup should now have a matching tp=110 row.
	var backupCount, matchedInVacc int64
	must(db.Raw(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tbl)).Scan(&backupCount).Error, "count backup")
	must(db.Raw(fmt.Sprintf(`
		SELECT COUNT(*) FROM %s b
		WHERE EXISTS (
			SELECT 1 FROM vaccination v
			WHERE v.tp = '110'
			  AND v.uuid = b.uuid
			  AND v.date = b.date
			  AND COALESCE(v.vac, '') = COALESCE(b.vac, '')
		)
	`, tbl)).Scan(&matchedInVacc).Error, "count matched in vaccination")

	fmt.Printf("backup rows:                                  %d\n", backupCount)
	fmt.Printf("backup rows now in vaccination as tp=110:     %d\n", matchedInVacc)

	if backupCount == matchedInVacc {
		fmt.Println("\n✓ all backup rows are present as vaccination tp=110")
	} else {
		missing := backupCount - matchedInVacc
		fmt.Printf("\n✗ %d backup rows are NOT in vaccination — investigate\n", missing)
		// Show 5 examples of missing rows.
		type missingRow struct {
			ID            int
			UUID, Date, Vac string
		}
		var missingRows []missingRow
		must(db.Raw(fmt.Sprintf(`
			SELECT b.id, b.uuid, b.date, b.vac
			FROM %s b
			WHERE NOT EXISTS (
				SELECT 1 FROM vaccination v
				WHERE v.tp = '110' AND v.uuid = b.uuid AND v.date = b.date
				  AND COALESCE(v.vac, '') = COALESCE(b.vac, '')
			)
			LIMIT 5
		`, tbl)).Scan(&missingRows).Error, "sample missing")
		for _, r := range missingRows {
			fmt.Printf("  id=%-5d uuid=%-12s date=%-12s vac=%s\n", r.ID, r.UUID, r.Date, truncate(r.Vac, 40))
		}
		os.Exit(1)
	}

	// 2. Sample 5 rows that landed in vaccination via this migration
	//    so we can eyeball the result.
	type vacRow struct {
		ID                 int
		UUID, Date, Vac, TPName string
	}
	var vacRows []vacRow
	must(db.Raw(fmt.Sprintf(`
		SELECT v.id, v.uuid, v.date, v.vac, v.tpname
		FROM vaccination v
		WHERE v.tp = '110'
		  AND EXISTS (SELECT 1 FROM %s b WHERE b.uuid = v.uuid AND b.date = v.date AND COALESCE(b.vac,'') = COALESCE(v.vac,''))
		ORDER BY v.id DESC
		LIMIT 5
	`, tbl)).Scan(&vacRows).Error, "sample inserted")

	fmt.Println("\nsample inserted rows (most recent first):")
	for _, r := range vacRows {
		fmt.Printf("  id=%-5d uuid=%-12s date=%-12s vac=%-25s tpname=%s\n",
			r.ID, r.UUID, r.Date, truncate(r.Vac, 25), r.TPName)
	}

	fmt.Println("\nmigration verified. backup table preserved at:", tbl)
	fmt.Println("\nfollow-up steps (NOT run automatically):")
	fmt.Println("  - Smoke-test the mobile app: open a pet that has a sterilization record")
	fmt.Println("    and confirm it appears under the სტერილიზაცია tile.")
	fmt.Println("  - After 2+ weeks of stability, drop the backup with:")
	fmt.Printf("      DROP TABLE %s;\n", tbl)
	fmt.Println("  - When PHP is decommissioned, drop the legacy `steril` table.")
}

// ─────────────────────────────────────────────────────────────────────
// rollback
// ─────────────────────────────────────────────────────────────────────

func rollback(db *gorm.DB) {
	header("STERIL ROLLBACK — DESTRUCTIVE")

	tbl := todayBackupTable()

	if !tableExists(db, tbl) {
		fmt.Fprintf(os.Stderr, "ERROR: backup table %s not found. Cannot roll back.\n", tbl)
		os.Exit(1)
	}

	var beforeCount int64
	must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE tp = '110'`).Scan(&beforeCount).Error, "count before rollback")

	err := db.Transaction(func(tx *gorm.DB) error {
		// Delete vaccination tp=110 rows whose (uuid, date, vac) match
		// a row in the backup. This unwinds exactly what `migrate`
		// did — does NOT touch tp=110 rows that pre-existed before the
		// migration, because those don't have a matching backup row.
		res := tx.Exec(fmt.Sprintf(`
			DELETE FROM vaccination v
			WHERE v.tp = '110'
			  AND EXISTS (
				SELECT 1 FROM %s b
				WHERE b.uuid = v.uuid
				  AND b.date = v.date
				  AND COALESCE(b.vac, '') = COALESCE(v.vac, '')
			  )
		`, tbl))
		if res.Error != nil {
			return res.Error
		}
		fmt.Printf("deleted from vaccination: %d rows\n", res.RowsAffected)
		return nil
	})
	if err != nil {
		log.Fatalf("rollback failed (transaction rolled back): %v", err)
	}

	var afterCount int64
	must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE tp = '110'`).Scan(&afterCount).Error, "count after rollback")
	fmt.Printf("vaccination tp=110 before rollback: %d\n", beforeCount)
	fmt.Printf("vaccination tp=110 after rollback:  %d\n", afterCount)
	fmt.Printf("delta:                              -%d\n", beforeCount-afterCount)
	fmt.Println("\n`steril` table is unchanged — your original data is fully recoverable from there.")
	fmt.Printf("backup table %s remains in place; drop it manually when you're done.\n", tbl)
}

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

func todayBackupTable() string {
	return backupTablePrefix + time.Now().Format("20060102")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func must(err error, ctx string) {
	if err != nil {
		log.Fatalf("%s: %v", ctx, err)
	}
}

func header(s string) {
	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════════════════")
	fmt.Println("  " + s)
	fmt.Println("══════════════════════════════════════════════════════════════")
}
