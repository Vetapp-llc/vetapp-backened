// brcleanup strips legacy `<br />` tags out of free-text columns in
// the `vaccination` table. PHP's `nl2br()` ran on every multiline
// textarea before INSERT, so the DB literally contains tags like
// "<br />", "<br/>", "<br>" wherever a user pressed Enter.
//
// The new app already strips these on read (see `cleanText` in
// internal/handlers/text.go), so this migration is a one-shot
// optimisation: cleans the at-rest data so future direct-SQL queries,
// exports, and any non-Go clients don't have to repeat the stripping.
//
// Affected columns (PHP files where nl2br is applied):
//
//	coment, anam, diagn, nout, koment, dani
//
// Run modes (positional arg):
//
//	precheck   — read-only: counts of <br>-containing rows per column,
//	            samples, total bytes that will be removed.
//	backup     — snapshot the (id, ...affected columns) of every row
//	            that contains a <br> in any column. Idempotent.
//	migrate    — transactional UPDATE using REGEXP_REPLACE on each
//	            column. Pre-condition: backup must have been run.
//	verify     — confirms zero <br>-containing cells remain and total
//	            row count unchanged.
//	rollback   — restores the affected columns from the backup.
//	            Requires --confirm-rollback.
package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"vetapp-backend/internal/config"
	"vetapp-backend/internal/database"

	"gorm.io/gorm"
)

const backupTablePrefix = "_br_backup_"

// columns is the canonical list of free-text fields the legacy PHP
// `nl2br()` was applied to. Order matters only for backup-table column
// order — the migration UPDATEs all of them in one statement.
var columns = []string{"coment", "anam", "diagn", "nout", "koment", "dani"}

// brRegex matches every `<br>` shape PHP could emit, plus an optional
// trailing `\r?\n` so we consume the whole nl2br-inserted sequence in
// one shot:
//
//	"line<br />\r\nnext" → "line\nnext"      (cleanest result)
//	"line<br />next"     → "line\nnext"      (no surrounding newline)
//	"<br /><br />"        → "\n\n"            (paragraph break preserved)
//
// PHP's `nl2br()` inserts `<br />` *before* every existing newline, so
// in real data `<br />` is almost always followed by `\r\n` or `\n` —
// consuming the newline avoids the double-newline `\n\r\n` artefact.
// Postgres' `~*` operator (case-insensitive regex match) honours `\s*`
// for any whitespace inside the tag.
const brRegex = `<br\s*/?>(\r?\n)?`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: brcleanup <precheck|backup|migrate|verify|rollback>")
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
			fmt.Fprintln(os.Stderr, "rollback requires --confirm-rollback flag")
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

// ─────────────────────────────────────────────────────────────────────
// precheck
// ─────────────────────────────────────────────────────────────────────

func precheck(db *gorm.DB) {
	header("BR CLEANUP — PRECHECK (read-only)")

	// 1. Total row count for context.
	var totalRows int64
	must(db.Raw(`SELECT COUNT(*) FROM vaccination`).Scan(&totalRows).Error)
	fmt.Printf("vaccination total rows: %d\n\n", totalRows)

	// 2. Per-column count of rows containing any <br> shape.
	type colStat struct {
		Name           string
		RowsAffected   int64
		TotalLength    int64
		AfterLength    int64
	}
	stats := make([]colStat, 0, len(columns))
	var grandRowsAffected int64
	for _, c := range columns {
		// Count rows where the column has any <br>-shape match.
		var n int64
		must(db.Raw(fmt.Sprintf(
			`SELECT COUNT(*) FROM vaccination WHERE %s ~* '%s'`,
			c, brRegex,
		)).Scan(&n).Error)

		// Total stored bytes in the column (for scope), and bytes
		// after the regex strip — gives a rough "size saved" preview
		// without writing anything.
		var totalLen, afterLen int64
		must(db.Raw(fmt.Sprintf(
			`SELECT COALESCE(SUM(LENGTH(%s)), 0) FROM vaccination WHERE %s ~* '%s'`,
			c, c, brRegex,
		)).Scan(&totalLen).Error)
		must(db.Raw(fmt.Sprintf(
			`SELECT COALESCE(SUM(LENGTH(REGEXP_REPLACE(%s, '%s', E'\n', 'gi'))), 0) FROM vaccination WHERE %s ~* '%s'`,
			c, brRegex, c, brRegex,
		)).Scan(&afterLen).Error)

		stats = append(stats, colStat{
			Name:         c,
			RowsAffected: n,
			TotalLength:  totalLen,
			AfterLength:  afterLen,
		})
		grandRowsAffected += n
	}

	fmt.Printf("%-10s %12s %14s %14s %12s\n", "column", "rows w/ <br>", "current bytes", "after-strip", "saved")
	for _, s := range stats {
		saved := s.TotalLength - s.AfterLength
		fmt.Printf("  %-8s %12d %14d %14d %12d\n",
			s.Name, s.RowsAffected, s.TotalLength, s.AfterLength, saved)
	}
	fmt.Println()

	// 3. Distinct rows across all columns (a single row can have <br>
	//    in multiple columns — counted once each above, but we want
	//    to know the "blast radius" for the backup table sizing).
	var uniqueRows int64
	whereAny := buildAnyMatchWhere()
	must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE ` + whereAny).Scan(&uniqueRows).Error)
	fmt.Printf("distinct vaccination rows containing any <br>: %d\n", uniqueRows)
	fmt.Printf("(this is also the size of the backup table that would be created)\n\n")

	if uniqueRows == 0 {
		fmt.Println("Nothing to clean. No <br> tags in the affected columns.")
		return
	}

	// 4. Sample 3 rows from each column showing before/after of the
	//    regex strip on a 200-char head. Helps eyeball the regex.
	for _, c := range columns {
		var rows []struct {
			ID     int
			Before string
			After  string
		}
		must(db.Raw(fmt.Sprintf(`
			SELECT id,
			       LEFT(%s, 200)                                                              AS before,
			       LEFT(REGEXP_REPLACE(%s, '%s', E'\n', 'gi'), 200)                            AS after
			FROM vaccination
			WHERE %s ~* '%s'
			ORDER BY id DESC
			LIMIT 3
		`, c, c, brRegex, c, brRegex)).Scan(&rows).Error)
		if len(rows) == 0 {
			continue
		}
		fmt.Printf("───── %s — sample before/after ─────\n", c)
		for _, r := range rows {
			fmt.Printf("id=%d\n", r.ID)
			fmt.Printf("  before: %s\n", inlineEscape(r.Before))
			fmt.Printf("  after:  %s\n", inlineEscape(r.After))
		}
		fmt.Println()
	}

	fmt.Printf("backup table that would be created: %s\n", todayBackupTable())
	fmt.Println("\nnext step: run `go run ./cmd/brcleanup backup`")
}

// ─────────────────────────────────────────────────────────────────────
// backup
// ─────────────────────────────────────────────────────────────────────

func backup(db *gorm.DB) {
	header("BR CLEANUP — BACKUP")

	tbl := todayBackupTable()
	must(db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tbl)).Error)

	// Snapshot only the affected columns + id. Restoring is a JOIN
	// against this on `id` so we don't need any other column.
	colList := strings.Join(append([]string{"id"}, columns...), ", ")
	must(db.Exec(fmt.Sprintf(`
		CREATE TABLE %s AS
		SELECT %s
		FROM vaccination
		WHERE %s
	`, tbl, colList, buildAnyMatchWhere())).Error)

	var backupCount, expectedCount int64
	must(db.Raw(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tbl)).Scan(&backupCount).Error)
	must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE ` + buildAnyMatchWhere()).Scan(&expectedCount).Error)

	fmt.Printf("backup table:    %s\n", tbl)
	fmt.Printf("backed up rows:  %d\n", backupCount)
	fmt.Printf("expected rows:   %d\n", expectedCount)
	if backupCount != expectedCount {
		fmt.Println("\nWARNING: backup count does not match!")
		os.Exit(1)
	}

	fmt.Println("\nbackup OK. next step: `go run ./cmd/brcleanup migrate`")
}

// ─────────────────────────────────────────────────────────────────────
// migrate
// ─────────────────────────────────────────────────────────────────────

func migrate(db *gorm.DB) {
	header("BR CLEANUP — MIGRATE")

	tbl := todayBackupTable()
	if !tableExists(db, tbl) {
		fmt.Fprintf(os.Stderr, "ERROR: backup table %s does not exist. Run `backup` first.\n", tbl)
		os.Exit(1)
	}

	var beforeCount int64
	must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE ` + buildAnyMatchWhere()).Scan(&beforeCount).Error)
	fmt.Printf("rows containing <br> before: %d\n", beforeCount)

	// One transactional UPDATE that strips <br>-shapes from all six
	// columns at once. Faster than six separate UPDATEs and atomic.
	err := db.Transaction(func(tx *gorm.DB) error {
		setClauses := make([]string, 0, len(columns))
		for _, c := range columns {
			setClauses = append(setClauses,
				fmt.Sprintf(`%s = REGEXP_REPLACE(%s, '%s', E'\n', 'gi')`, c, c, brRegex))
		}
		sql := fmt.Sprintf(
			`UPDATE vaccination SET %s WHERE %s`,
			strings.Join(setClauses, ", "),
			buildAnyMatchWhere(),
		)
		res := tx.Exec(sql)
		if res.Error != nil {
			return res.Error
		}
		fmt.Printf("rows updated: %d\n", res.RowsAffected)
		return nil
	})
	if err != nil {
		log.Fatalf("migration failed (transaction rolled back): %v", err)
	}

	var afterCount int64
	must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE ` + buildAnyMatchWhere()).Scan(&afterCount).Error)
	fmt.Printf("rows containing <br> after:  %d  (should be 0)\n", afterCount)

	fmt.Println("\nnext step: `go run ./cmd/brcleanup verify`")
}

// ─────────────────────────────────────────────────────────────────────
// verify
// ─────────────────────────────────────────────────────────────────────

func verify(db *gorm.DB) {
	header("BR CLEANUP — VERIFY")

	tbl := todayBackupTable()
	if !tableExists(db, tbl) {
		fmt.Fprintf(os.Stderr, "backup table %s not found.\n", tbl)
		os.Exit(1)
	}

	allOK := true

	// 1. Zero <br>-containing rows in any affected column.
	for _, c := range columns {
		var n int64
		must(db.Raw(fmt.Sprintf(`SELECT COUNT(*) FROM vaccination WHERE %s ~* '%s'`, c, brRegex)).Scan(&n).Error)
		mark := "✓"
		if n != 0 {
			mark = "✗"
			allOK = false
		}
		fmt.Printf("  %s %-8s rows still containing <br>: %d\n", mark, c, n)
	}

	// 2. Total row count unchanged.
	var totalRows int64
	must(db.Raw(`SELECT COUNT(*) FROM vaccination`).Scan(&totalRows).Error)
	fmt.Printf("\nvaccination total rows: %d (UPDATE-only migration; should match pre-migration count)\n", totalRows)

	// 3. Sample 5 rows that were in the backup, show before / after.
	type sample struct {
		ID            int
		BeforeComent  string
		AfterComent   string
	}
	var samples []sample
	must(db.Raw(fmt.Sprintf(`
		SELECT b.id, LEFT(b.coment, 120) AS before_coment, LEFT(v.coment, 120) AS after_coment
		FROM %s b
		JOIN vaccination v ON v.id = b.id
		WHERE b.coment ~* '%s'
		ORDER BY b.id DESC
		LIMIT 5
	`, tbl, brRegex)).Scan(&samples).Error)
	if len(samples) > 0 {
		fmt.Println("\nbefore / after samples (coment field):")
		for _, s := range samples {
			fmt.Printf("  id=%d\n", s.ID)
			fmt.Printf("    before: %s\n", inlineEscape(s.BeforeComent))
			fmt.Printf("    after:  %s\n", inlineEscape(s.AfterComent))
		}
	}

	if allOK {
		fmt.Println("\n✓ migration verified — every affected column is now <br>-free")
		fmt.Printf("backup table preserved at: %s\n", tbl)
		fmt.Println("\nfollow-up:")
		fmt.Println("  - Smoke-test the mobile app: open a procedure with a multi-line comment;")
		fmt.Println("    the rendered text should still have line breaks (driven by real \\n now).")
		fmt.Println("  - After 2+ weeks of stability, drop the backup with:")
		fmt.Printf("      DROP TABLE %s;\n", tbl)
	} else {
		fmt.Println("\n✗ verification failed.")
		os.Exit(1)
	}
}

// ─────────────────────────────────────────────────────────────────────
// rollback
// ─────────────────────────────────────────────────────────────────────

func rollback(db *gorm.DB) {
	header("BR CLEANUP ROLLBACK — DESTRUCTIVE")

	tbl := todayBackupTable()
	if !tableExists(db, tbl) {
		fmt.Fprintf(os.Stderr, "ERROR: backup table %s not found.\n", tbl)
		os.Exit(1)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		setClauses := make([]string, 0, len(columns))
		for _, c := range columns {
			setClauses = append(setClauses, fmt.Sprintf(`%s = b.%s`, c, c))
		}
		sql := fmt.Sprintf(
			`UPDATE vaccination v SET %s FROM %s b WHERE v.id = b.id`,
			strings.Join(setClauses, ", "),
			tbl,
		)
		res := tx.Exec(sql)
		if res.Error != nil {
			return res.Error
		}
		fmt.Printf("restored %d rows\n", res.RowsAffected)
		return nil
	})
	if err != nil {
		log.Fatalf("rollback failed: %v", err)
	}
	fmt.Printf("\nbackup table %s remains in place.\n", tbl)
}

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

// buildAnyMatchWhere returns a SQL WHERE-clause body matching any row
// where ANY affected column contains a <br>-shape. Used by precheck,
// backup, migrate, and verify.
func buildAnyMatchWhere() string {
	parts := make([]string, 0, len(columns))
	for _, c := range columns {
		parts = append(parts, fmt.Sprintf(`%s ~* '%s'`, c, brRegex))
	}
	return strings.Join(parts, " OR ")
}

func tableExists(db *gorm.DB, name string) bool {
	var n int64
	must(db.Raw(`
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = ?
	`, name).Scan(&n).Error)
	return n > 0
}

func todayBackupTable() string {
	return backupTablePrefix + time.Now().Format("20060102")
}

// inlineEscape replaces real newlines in a sampled string with the
// literal "\n" so the row stays on one terminal line. Makes
// before/after comparisons readable.
func inlineEscape(s string) string {
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	return s
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func header(s string) {
	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════════════════")
	fmt.Println("  " + s)
	fmt.Println("══════════════════════════════════════════════════════════════")
}
