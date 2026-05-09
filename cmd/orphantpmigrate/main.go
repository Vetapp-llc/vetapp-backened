// orphantpmigrate inspects and re-homes orphan tp values in the
// `vaccination` table — tps that exist in production data but no PHP
// `add*` form ever emits them. From the explore-agent research and
// stats run, those tps are 3, 4, 5, and 555.
//
// The migration target for each orphan is determined by looking at
// which columns are populated. For example, an orphan tp whose `deh`
// column is filled in 90%+ of rows is almost certainly old
// dehelminization data → migrate to tp=12.
//
// Run modes (positional arg):
//
//	precheck   — read-only column-pattern analysis. Prints proposed
//	            migration map. Run this first.
//	backup     — snapshot orphan rows to a date-stamped table for
//	            rollback. Idempotent.
//	migrate    — transactional UPDATE tp + tpname based on the
//	            proposedMap below. Pre-condition: backup must exist.
//	verify     — confirms orphans gone, counts match expected.
//	rollback   — restores tp/tpname from the backup table.
//	            Requires --confirm-rollback.
package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"vetapp-backend/internal/config"
	"vetapp-backend/internal/database"

	"gorm.io/gorm"
)

const backupTablePrefix = "_orphantp_backup_"

// orphanTPs are the tp values we want to investigate and migrate. From
// the production stats: tp=3 (~thousands), tp=4 (3 rows), tp=5
// (~thousands), tp=555 (2 rows).
var orphanTPs = []string{"3", "4", "5", "555"}

// proposedMap is the planned migration target per orphan tp, decided
// after the `precheck` phase showed actual column patterns and sample
// data on 2026-04-28.
//
// Findings & rationale (only 8 orphan rows total):
//
//	tp=3   (1 row)  → tp=11  ecto.  vac1="Frontline Plus" (a drops product).
//	                              The lone row was a dev test created via
//	                              the old buggy form that mis-labelled
//	                              tp=3 as "ECTOPARASITES".
//	tp=4   (3 rows) → SPLIT.  Two rows have vac/vac1/vac2/vac3 filled
//	                              with ecto product names (Advocate,
//	                              NexGuard, Bars, Chistotel) → tp=11.
//	                              Third row has only diagn=
//	                              "FIV/FeLV - უარყოფითი" (a feline test
//	                              result) → tp=107 (generic "other"
//	                              bucket — we can't infer test species
//	                              cleanly enough to send it to tp=22).
//	tp=5   (2 rows) → tp=12  dehel. Both rows have vac="Drontal Plus" /
//	                              "Milbemax" — these are dewormer brands.
//	tp=555 (2 rows) → tp=107 other. Russian-language clinical exam
//	                              notes from 2023; data spread across
//	                              all columns, no clear category.
//
// `migrate` runs each rule below as a separate UPDATE statement
// inside one big transaction.
type rule struct {
	WhereClause string // appended after `WHERE tp = '<tp>' AND `, or empty for "all rows of this tp"
	NewTP       string
	NewTPName   string
	Reason      string
}

var proposedMap = map[string][]rule{
	"3": {
		{NewTP: "11", NewTPName: "ექტოპარაზიტების პრევენცია",
			Reason: "vac1=\"Frontline Plus\" — ectoparasite drops"},
	},
	"4": {
		// Rows with any ecto-shape column populated → tp=11.
		// COALESCE(...,'') treats NULL the same as empty so this works
		// regardless of whether the column is NULL or '' in the DB.
		{
			WhereClause: "(COALESCE(vac,'') <> '' OR COALESCE(vac1,'') <> '' OR COALESCE(vac2,'') <> '' OR COALESCE(vac3,'') <> '')",
			NewTP:       "11",
			NewTPName:   "ექტოპარაზიტების პრევენცია",
			Reason:      "ecto-shape vac/vac1/vac2/vac3 populated",
		},
		// Rows that aren't ecto-shape (only diagn etc.) → tp=107 catch-all.
		{
			WhereClause: "(COALESCE(vac,'') = '' AND COALESCE(vac1,'') = '' AND COALESCE(vac2,'') = '' AND COALESCE(vac3,'') = '')",
			NewTP:       "107",
			NewTPName:   "სხვა პროცედურა",
			Reason:      "no ecto columns — bucket as \"other\"",
		},
	},
	"5": {
		{NewTP: "12", NewTPName: "დეჰელმინთიზაცია",
			Reason: "vac contains dewormer brands (Drontal Plus, Milbemax)"},
	},
	"555": {
		{NewTP: "107", NewTPName: "სხვა პროცედურა",
			Reason: "Russian-language clinical exam notes — bucket as \"other\""},
	},
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: orphantpmigrate <precheck|backup|migrate|verify|rollback>")
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
	header("ORPHAN tp INVESTIGATION — PRECHECK (read-only)")

	// 1. Count total rows per orphan tp.
	type tpCount struct {
		Tp    string
		Total int64
	}
	var countsRows []tpCount
	must(db.Raw(`
		SELECT tp, COUNT(*) AS total
		FROM vaccination
		WHERE tp IN ?
		GROUP BY tp
		ORDER BY tp
	`, orphanTPs).Scan(&countsRows).Error, "count orphans")

	totalsByTP := map[string]int64{}
	var grandTotal int64
	for _, r := range countsRows {
		totalsByTP[r.Tp] = r.Total
		grandTotal += r.Total
	}

	fmt.Println("orphan tp record counts:")
	for _, tp := range orphanTPs {
		fmt.Printf("  tp=%-4s  %6d records\n", tp, totalsByTP[tp])
	}
	fmt.Printf("  %-7s %6d total\n\n", "TOTAL", grandTotal)
	if grandTotal == 0 {
		fmt.Println("No orphan rows found. Nothing to do.")
		return
	}

	// 2. Per orphan tp: distribution of `tpname` values. The legacy
	//    PHP forms hard-coded `tpname` so this is a strong hint about
	//    what the row's category was supposed to be.
	for _, tp := range orphanTPs {
		if totalsByTP[tp] == 0 {
			continue
		}
		fmt.Printf("───── tp=%s — top tpname values ─────\n", tp)
		type nameRow struct {
			TPName string
			Total  int64
		}
		var names []nameRow
		must(db.Raw(`
			SELECT tpname, COUNT(*) AS total
			FROM vaccination
			WHERE tp = ?
			GROUP BY tpname
			ORDER BY total DESC
			LIMIT 5
		`, tp).Scan(&names).Error, "tpname distribution")
		for _, n := range names {
			label := n.TPName
			if label == "" {
				label = "(empty)"
			}
			fmt.Printf("  %-50s %d\n", truncate(label, 50), n.Total)
		}
		fmt.Println()
	}

	// 3. Per orphan tp: which columns have non-empty data, as a
	//    percentage of rows. The dominant pattern reveals the
	//    canonical category.
	type fillRow struct {
		Tp    string
		Total int64
		// Column-level non-empty counts.
		WithVac, WithVacN, WithSer, WithDeh                                int64
		WithVac1, WithVac2, WithVac3, WithVac4                             int64
		WithVac5, WithVac6, WithVac7, WithVac8, WithVac9                   int64
		WithDiagn, WithAnam, WithNout, WithKoment, WithDani, WithComent    int64
		WithTest1, WithTest2, WithTest3, WithTest4                          int64
		WithTest5, WithTest6, WithTest7, WithTest8                          int64
	}
	for _, tp := range orphanTPs {
		if totalsByTP[tp] == 0 {
			continue
		}
		var f fillRow
		must(db.Raw(`
			SELECT
				COUNT(*) AS total,
				COUNT(*) FILTER (WHERE vac    IS NOT NULL AND vac    <> '') AS with_vac,
				COUNT(*) FILTER (WHERE vacn   IS NOT NULL AND vacn   <> '') AS with_vac_n,
				COUNT(*) FILTER (WHERE ser    IS NOT NULL AND ser    <> '') AS with_ser,
				COUNT(*) FILTER (WHERE deh    IS NOT NULL AND deh    <> '') AS with_deh,
				COUNT(*) FILTER (WHERE vac1   IS NOT NULL AND vac1   <> '') AS with_vac1,
				COUNT(*) FILTER (WHERE vac2   IS NOT NULL AND vac2   <> '') AS with_vac2,
				COUNT(*) FILTER (WHERE vac3   IS NOT NULL AND vac3   <> '') AS with_vac3,
				COUNT(*) FILTER (WHERE vac4   IS NOT NULL AND vac4   <> '') AS with_vac4,
				COUNT(*) FILTER (WHERE vac5   IS NOT NULL AND vac5   <> '') AS with_vac5,
				COUNT(*) FILTER (WHERE vac6   IS NOT NULL AND vac6   <> '') AS with_vac6,
				COUNT(*) FILTER (WHERE vac7   IS NOT NULL AND vac7   <> '') AS with_vac7,
				COUNT(*) FILTER (WHERE vac8   IS NOT NULL AND vac8   <> '') AS with_vac8,
				COUNT(*) FILTER (WHERE vac9   IS NOT NULL AND vac9   <> '') AS with_vac9,
				COUNT(*) FILTER (WHERE diagn  IS NOT NULL AND diagn  <> '') AS with_diagn,
				COUNT(*) FILTER (WHERE anam   IS NOT NULL AND anam   <> '') AS with_anam,
				COUNT(*) FILTER (WHERE nout   IS NOT NULL AND nout   <> '') AS with_nout,
				COUNT(*) FILTER (WHERE koment IS NOT NULL AND koment <> '') AS with_koment,
				COUNT(*) FILTER (WHERE dani   IS NOT NULL AND dani   <> '') AS with_dani,
				COUNT(*) FILTER (WHERE coment IS NOT NULL AND coment <> '') AS with_coment,
				COUNT(*) FILTER (WHERE test1  IS NOT NULL AND test1  <> '') AS with_test1,
				COUNT(*) FILTER (WHERE test2  IS NOT NULL AND test2  <> '') AS with_test2,
				COUNT(*) FILTER (WHERE test3  IS NOT NULL AND test3  <> '') AS with_test3,
				COUNT(*) FILTER (WHERE test4  IS NOT NULL AND test4  <> '') AS with_test4,
				COUNT(*) FILTER (WHERE test5  IS NOT NULL AND test5  <> '') AS with_test5,
				COUNT(*) FILTER (WHERE test6  IS NOT NULL AND test6  <> '') AS with_test6,
				COUNT(*) FILTER (WHERE test7  IS NOT NULL AND test7  <> '') AS with_test7,
				COUNT(*) FILTER (WHERE test8  IS NOT NULL AND test8  <> '') AS with_test8
			FROM vaccination
			WHERE tp = ?
		`, tp).Scan(&f).Error, "fill rates")

		fmt.Printf("───── tp=%s — column fill rates (n=%d) ─────\n", tp, f.Total)
		entries := []struct {
			Name  string
			Count int64
		}{
			{"vac", f.WithVac},
			{"vacn", f.WithVacN},
			{"ser", f.WithSer},
			{"deh", f.WithDeh},
			{"vac1", f.WithVac1},
			{"vac2", f.WithVac2},
			{"vac3", f.WithVac3},
			{"vac4", f.WithVac4},
			{"vac5", f.WithVac5},
			{"vac6", f.WithVac6},
			{"vac7", f.WithVac7},
			{"vac8", f.WithVac8},
			{"vac9", f.WithVac9},
			{"diagn", f.WithDiagn},
			{"anam", f.WithAnam},
			{"nout", f.WithNout},
			{"koment", f.WithKoment},
			{"dani", f.WithDani},
			{"coment", f.WithComent},
			{"test1", f.WithTest1},
			{"test2", f.WithTest2},
			{"test3", f.WithTest3},
			{"test4", f.WithTest4},
			{"test5", f.WithTest5},
			{"test6", f.WithTest6},
			{"test7", f.WithTest7},
			{"test8", f.WithTest8},
		}
		// Sort by count descending so the dominant pattern is obvious.
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].Count > entries[j].Count
		})
		for _, e := range entries {
			if e.Count == 0 {
				continue
			}
			pct := float64(e.Count) * 100 / float64(f.Total)
			fmt.Printf("  %-8s %6d  (%5.1f%%)\n", e.Name, e.Count, pct)
		}
		fmt.Println()
	}

	// 4. For each orphan tp, sample 5 rows so we can eyeball the data.
	for _, tp := range orphanTPs {
		if totalsByTP[tp] == 0 {
			continue
		}
		fmt.Printf("───── tp=%s — 5 sample rows ─────\n", tp)
		type sample struct {
			ID                                   int
			UUID, Date, TPName, Vac, VacN, Deh   string
			Vac1, Vac2, Vac3, Vac4, Vac5, Vac6   string
			Diagn                                string
		}
		var rows []sample
		must(db.Raw(`
			SELECT id, uuid, date, tpname, vac, vacn, deh,
			       vac1, vac2, vac3, vac4, vac5, vac6, diagn
			FROM vaccination
			WHERE tp = ?
			ORDER BY id DESC
			LIMIT 5
		`, tp).Scan(&rows).Error, "sample rows")
		for _, r := range rows {
			fmt.Printf("  id=%d uuid=%s date=%s tpname=%s\n",
				r.ID, r.UUID, r.Date, truncate(r.TPName, 30))
			if r.Vac != "" {
				fmt.Printf("     vac    = %s\n", truncate(r.Vac, 60))
			}
			if r.VacN != "" {
				fmt.Printf("     vacn   = %s\n", truncate(r.VacN, 60))
			}
			if r.Deh != "" {
				fmt.Printf("     deh    = %s\n", truncate(r.Deh, 60))
			}
			if r.Vac1 != "" {
				fmt.Printf("     vac1   = %s\n", truncate(r.Vac1, 60))
			}
			if r.Vac2 != "" {
				fmt.Printf("     vac2   = %s\n", truncate(r.Vac2, 60))
			}
			if r.Vac3 != "" {
				fmt.Printf("     vac3   = %s\n", truncate(r.Vac3, 60))
			}
			if r.Vac4 != "" {
				fmt.Printf("     vac4   = %s\n", truncate(r.Vac4, 60))
			}
			if r.Vac5 != "" {
				fmt.Printf("     vac5   = %s\n", truncate(r.Vac5, 60))
			}
			if r.Vac6 != "" {
				fmt.Printf("     vac6   = %s\n", truncate(r.Vac6, 60))
			}
			if r.Diagn != "" {
				fmt.Printf("     diagn  = %s\n", truncate(r.Diagn, 60))
			}
		}
		fmt.Println()
	}

	// 5. Print proposed migration map. Multi-rule entries (tp=4 with
	//    its conditional split) appear as multiple bullets.
	fmt.Println("───── proposed migration map ─────")
	for _, tp := range orphanTPs {
		if totalsByTP[tp] == 0 {
			continue
		}
		rules := proposedMap[tp]
		for i, rule := range rules {
			prefix := "  tp=" + tp
			if i > 0 {
				prefix = "        "
			}
			cond := ""
			if rule.WhereClause != "" {
				cond = "  WHERE " + rule.WhereClause
			}
			fmt.Printf("%-7s →  tp=%-4s (%s)%s\n            [%s]\n",
				prefix, rule.NewTP, rule.NewTPName, cond, rule.Reason)
		}
	}

	fmt.Printf("\nbackup table that would be created: %s\n", todayBackupTable())
	fmt.Println("\nnext step: review the analysis above, edit `proposedMap` if needed,")
	fmt.Println("           then run `go run ./cmd/orphantpmigrate backup`")
}

// ─────────────────────────────────────────────────────────────────────
// backup
// ─────────────────────────────────────────────────────────────────────

func backup(db *gorm.DB) {
	header("ORPHAN tp — BACKUP")

	tbl := todayBackupTable()
	must(db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tbl)).Error, "drop existing backup")

	// We only need (id, tp, tpname) to be able to undo the migration.
	// The `id` is the primary key — sufficient to find the row again.
	// We keep `tp` and `tpname` so rollback can restore them exactly.
	must(db.Exec(fmt.Sprintf(`
		CREATE TABLE %s AS
		SELECT id, tp, tpname
		FROM vaccination
		WHERE tp IN ('3','4','5','555')
	`, tbl)).Error, "create backup table")

	var backupCount, orphanCount int64
	must(db.Raw(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tbl)).Scan(&backupCount).Error, "count backup")
	must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE tp IN ('3','4','5','555')`).Scan(&orphanCount).Error, "count orphans")

	fmt.Printf("backup table:  %s\n", tbl)
	fmt.Printf("backed up:     %d rows\n", backupCount)
	fmt.Printf("orphan rows:   %d\n", orphanCount)
	if backupCount != orphanCount {
		fmt.Println("\nWARNING: backup count does not match orphan count!")
		os.Exit(1)
	}
	fmt.Println("\nbackup OK. next step: `go run ./cmd/orphantpmigrate migrate`")
}

// ─────────────────────────────────────────────────────────────────────
// migrate
// ─────────────────────────────────────────────────────────────────────

func migrate(db *gorm.DB) {
	header("ORPHAN tp → CANONICAL tp — MIGRATE")

	tbl := todayBackupTable()
	if !tableExists(db, tbl) {
		fmt.Fprintf(os.Stderr, "ERROR: backup table %s does not exist. Run `backup` first.\n", tbl)
		os.Exit(1)
	}

	// Print before counts.
	fmt.Println("before migration:")
	for _, tp := range orphanTPs {
		var n int64
		must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE tp = ?`, tp).Scan(&n).Error, "count before")
		fmt.Printf("  tp=%-4s  %d\n", tp, n)
	}

	// Transactional UPDATE per orphan tp. One transaction wraps all of
	// them so a failure on any tp rolls back every prior update.
	// Multi-rule tps (tp=4 split into ecto/other) run their rules in
	// order; the second rule's WHERE clause must be careful not to
	// accidentally re-match rows the first rule already moved (which
	// is why the rules are mutually exclusive on column shape).
	err := db.Transaction(func(tx *gorm.DB) error {
		var totalUpdated int64
		for _, tp := range orphanTPs {
			rules, ok := proposedMap[tp]
			if !ok || len(rules) == 0 {
				return fmt.Errorf("no migration target defined for tp=%s", tp)
			}
			for _, rule := range rules {
				where := "tp = ?"
				args := []interface{}{rule.NewTP, rule.NewTPName, tp}
				if rule.WhereClause != "" {
					where = "tp = ? AND " + rule.WhereClause
				}
				sql := "UPDATE vaccination SET tp = ?, tpname = ? WHERE " + where
				res := tx.Exec(sql, args...)
				if res.Error != nil {
					return res.Error
				}
				cond := ""
				if rule.WhereClause != "" {
					cond = " (" + truncate(rule.WhereClause, 50) + ")"
				}
				fmt.Printf("  tp=%s → tp=%s%s: updated %d rows\n",
					tp, rule.NewTP, cond, res.RowsAffected)
				totalUpdated += res.RowsAffected
			}
		}
		fmt.Printf("\ntotal: %d rows migrated\n", totalUpdated)
		return nil
	})
	if err != nil {
		log.Fatalf("migration failed (transaction rolled back): %v", err)
	}

	fmt.Println("\nafter migration:")
	for _, tp := range orphanTPs {
		var n int64
		must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE tp = ?`, tp).Scan(&n).Error, "count after")
		fmt.Printf("  tp=%-4s  %d  (should be 0)\n", tp, n)
	}

	fmt.Println("\nnext step: `go run ./cmd/orphantpmigrate verify`")
}

// ─────────────────────────────────────────────────────────────────────
// verify
// ─────────────────────────────────────────────────────────────────────

func verify(db *gorm.DB) {
	header("ORPHAN tp — VERIFY")

	tbl := todayBackupTable()
	if !tableExists(db, tbl) {
		fmt.Fprintf(os.Stderr, "backup table %s not found.\n", tbl)
		os.Exit(1)
	}

	// 1. No orphans should remain.
	var remaining int64
	must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE tp IN ('3','4','5','555')`).Scan(&remaining).Error, "count remaining orphans")
	fmt.Printf("orphan rows remaining: %d  (expected 0)\n", remaining)
	if remaining != 0 {
		fmt.Println("✗ orphans still present. investigate.")
		os.Exit(1)
	}

	// 2. Per backup row, verify the post-migration tp is one of the
	//    expected new tps for its original orphan tp. (Multi-rule
	//    splits like tp=4 → {11, 107} are handled by accepting any of
	//    the new tps from `proposedMap[tp]`.)
	for _, tp := range orphanTPs {
		rules := proposedMap[tp]
		if len(rules) == 0 {
			continue
		}
		var expected int64
		must(db.Raw(fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE tp = ?`, tbl), tp).Scan(&expected).Error, "count expected from backup")
		if expected == 0 {
			continue
		}

		// Build the set of acceptable new tps.
		newTPs := make([]string, 0, len(rules))
		for _, r := range rules {
			newTPs = append(newTPs, r.NewTP)
		}

		var matched int64
		must(db.Raw(fmt.Sprintf(`
			SELECT COUNT(*)
			FROM vaccination v
			JOIN %s b ON v.id = b.id
			WHERE b.tp = ? AND v.tp IN ?
		`, tbl), tp, newTPs).Scan(&matched).Error, "count matched")

		ok := matched == expected
		mark := "✓"
		if !ok {
			mark = "✗"
		}
		fmt.Printf("  %s tp=%s → tp ∈ %v   expected %d   matched %d\n",
			mark, tp, newTPs, expected, matched)
		if !ok {
			os.Exit(1)
		}
	}

	fmt.Println("\nmigration verified.")
	fmt.Printf("backup table preserved at: %s\n", tbl)
	fmt.Println("\nfollow-up:")
	fmt.Println("  - Smoke-test the mobile app (full history should still show all records).")
	fmt.Println("  - After 2+ weeks of stability, drop the backup with:")
	fmt.Printf("      DROP TABLE %s;\n", tbl)
}

// ─────────────────────────────────────────────────────────────────────
// rollback
// ─────────────────────────────────────────────────────────────────────

func rollback(db *gorm.DB) {
	header("ORPHAN tp ROLLBACK — DESTRUCTIVE")

	tbl := todayBackupTable()
	if !tableExists(db, tbl) {
		fmt.Fprintf(os.Stderr, "ERROR: backup table %s not found.\n", tbl)
		os.Exit(1)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		// Restore tp + tpname from the backup table for every row that
		// was migrated. Match on `id` (primary key).
		res := tx.Exec(fmt.Sprintf(`
			UPDATE vaccination v
			SET tp = b.tp, tpname = b.tpname
			FROM %s b
			WHERE v.id = b.id
		`, tbl))
		if res.Error != nil {
			return res.Error
		}
		fmt.Printf("restored %d rows\n", res.RowsAffected)
		return nil
	})
	if err != nil {
		log.Fatalf("rollback failed: %v", err)
	}

	// Confirm orphans are back.
	for _, tp := range orphanTPs {
		var n int64
		must(db.Raw(`SELECT COUNT(*) FROM vaccination WHERE tp = ?`, tp).Scan(&n).Error, "count after rollback")
		fmt.Printf("  tp=%s  %d\n", tp, n)
	}
	fmt.Printf("\nbackup table %s remains in place.\n", tbl)
}

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

func tableExists(db *gorm.DB, name string) bool {
	var n int64
	must(db.Raw(`
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = ?
	`, name).Scan(&n).Error, "table-exists check")
	return n > 0
}

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
