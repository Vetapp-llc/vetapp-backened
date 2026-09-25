// Sync tool: full copy from MySQL (cPanel) → Supabase (PostgreSQL).
//
// Modes:
//   go run ./cmd/sync                     # incremental — inserts only missing-by-id rows
//   go run ./cmd/sync --update            # ALSO refresh rows edited upstream (upsert)
//   go run ./cmd/sync --full              # TRUNCATE + full re-copy (interactive confirm)
//   go run ./cmd/sync --full --yes        # full re-copy, skip confirmation prompt
//   go run ./cmd/sync --dry-run           # print row-count diff, no writes
//   go run ./cmd/sync --full --dry-run    # plan a full re-copy without doing it
//
// Which mode do I want?
//
// The PHP app is still live, so MySQL rows are not just APPENDED —
// they are EDITED. Plain incremental mode compares ids only, so an
// edit to an already-synced row is invisible to it: measured on
// 2026-07-27, 592 of 31,958 pets (1.9%) had drifted, including 388
// microchip numbers that exist in MySQL but are blank in Postgres
// because the chip was registered after the pet first synced.
//
// Use --update for the routine sync while the old app is still in
// production. Plain incremental is only appropriate for append-only
// tables, and cheaper mainly because it moves less data.
//
// MySQL is the source of truth. Supabase becomes an exact copy. For
// memberlogin_members, passwords are re-encrypted (MySQL salt → PG salt).
//
// Safety guardrails:
//   * `--dry-run` reads from both DBs and prints per-table counts; no
//     writes happen.
//   * `--full` first prints a summary of every table that would be
//     truncated along with current Supabase row counts, then waits
//     for the operator to type "yes". Pass `--yes` to bypass the
//     prompt (e.g. from a CI runner).
//   * After each table syncs, the post-sync row count is compared
//     against MySQL and a WARNING is logged if it differs.

package main

import (
	"bufio"
	"crypto/aes"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Credentials come from the environment. They were previously hardcoded
// here, which put the production MySQL and Supabase passwords in git
// history — anyone with repository access had both databases.
//
// The salts are NOT secrets in the same sense (they are a fixed part of
// the legacy AES scheme and are useless without a password column), so
// they keep working defaults; the DSNs have none and the tool refuses
// to start without them.
//
// Local use: put these in vetapp-backend/.env, which is gitignored.
//
//	SYNC_MYSQL_DSN  user:pass@tcp(host:3306)/dbname
//	SYNC_PG_DSN     host=... port=6543 user=... password=... dbname=...
var (
	mysqlDSN  string
	pgDSN     string
	mysqlSalt string
	pgSalt    string
)

// loadCredentials fills the DSNs from the environment.
//
// This runs from main() rather than as a package-level initialiser
// because those execute BEFORE godotenv.Load(), so values from .env
// would not be visible yet and the tool would refuse to start even with
// a correctly populated file.
func loadCredentials() {
	mysqlDSN = os.Getenv("SYNC_MYSQL_DSN")
	pgDSN = os.Getenv("SYNC_PG_DSN")
	mysqlSalt = envOr("SYNC_MYSQL_SALT", "RZ8HU1EB")
	pgSalt = envOr("SYNC_PG_SALT", "DW3Z07FI")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// dryRun, when true, suppresses every write (TRUNCATE, INSERT) and
// turns the tool into a row-count diff report.
var dryRun bool

// updateExisting turns inserts into upserts so rows EDITED in the live
// MySQL app are refreshed in Postgres, not just newly-created ones.
// See conflictClause for why this matters.
var updateExisting bool

func main() {
	var (
		fullSync   = flag.Bool("full", false, "TRUNCATE Supabase + full re-copy from MySQL (default: incremental)")
		dr         = flag.Bool("dry-run", false, "Read-only mode — report per-table counts, do not write")
		assumeYes  = flag.Bool("yes", false, "Skip the interactive confirmation prompt for --full")
		relocate   = flag.Bool("relocate-app-rows", false, "Move rows the app created before migration 016 (ids above MySQL's highest, below 1,000,000,000) into the app id range, fixing references")
		discardApp = flag.Bool("discard-app-data", false, "With --full: allow erasing rows created, edited or deleted in the new app")
		update     = flag.Bool("update", false, "Also refresh rows that already exist in Supabase but were EDITED in MySQL (upsert). Without this, only brand-new ids are copied and upstream edits are silently lost.")
	)
	flag.Parse()
	dryRun = *dr

	// Pick up vetapp-backend/.env when run from the repo, so the tool
	// works locally without exporting anything by hand.
	_ = godotenv.Load(".env")
	loadCredentials()

	if mysqlDSN == "" || pgDSN == "" {
		log.Fatal("SYNC_MYSQL_DSN and SYNC_PG_DSN must be set.\n" +
			"  Add them to vetapp-backend/.env (gitignored) or export them:\n" +
			"    SYNC_MYSQL_DSN=\"user:pass@tcp(host:3306)/dbname\"\n" +
			"    SYNC_PG_DSN=\"host=... port=6543 user=... password=... dbname=postgres sslmode=require default_query_exec_mode=simple_protocol\"")
	}
	updateExisting = *update
	// A full re-copy always rewrites every row, so upsert semantics are
	// implied — and needed, because TRUNCATE may be skipped for tables
	// with dependent rows.
	if *fullSync {
		updateExisting = true
	}

	switch {
	case *fullSync && dryRun:
		log.Println("=== MySQL → Supabase FULL Sync (DRY RUN) ===")
	case *fullSync:
		log.Println("=== MySQL → Supabase FULL Sync ===")
	case dryRun:
		log.Println("=== MySQL → Supabase Incremental Sync (DRY RUN) ===")
	default:
		log.Println("=== MySQL → Supabase Incremental Sync ===")
	}

	my, err := sql.Open("mysql", mysqlDSN)
	if err != nil {
		log.Fatalf("MySQL connect: %v", err)
	}
	defer my.Close()
	log.Println("Connected to MySQL")

	pg, err := sql.Open("pgx", pgDSN)
	if err != nil {
		log.Fatalf("PG connect: %v", err)
	}
	defer pg.Close()
	log.Println("Connected to Supabase")

	// Get all MySQL tables
	tables := getMySQLTables(my)
	log.Printf("Found %d MySQL tables", len(tables))

	// Pre-flight summary — always show, in every mode. Gives the
	// operator a chance to spot an obvious mistake (wrong DSN, empty
	// MySQL, etc.) BEFORE any destructive op.
	preflight := buildPreflight(my, pg, tables)
	printPreflight(preflight, *fullSync)
	loadProtected(pg)
	earlyRows := handleEarlyAppRows(my, pg, *relocate)

	if *fullSync && !dryRun {
		if !*assumeYes {
			if !confirm("Type 'yes' to TRUNCATE every Supabase table listed above and re-copy from MySQL: ") {
				log.Println("Aborted by operator.")
				return
			}
		}
		log.Println("\n=== Truncating Supabase tables ===")
		guardedTruncate(pg, tables, earlyRows, *discardApp)
		for _, t := range []string{"memberlogin_sms_codes"} {
			if dryRun {
				log.Printf("  [dry-run] would TRUNCATE %s", t)
				continue
			}
			if _, err := pg.Exec(fmt.Sprintf("TRUNCATE TABLE \"%s\" CASCADE", t)); err != nil {
				log.Printf("  skip %s: %v", t, err)
			} else {
				log.Printf("  truncated %s", t)
			}
		}
	} else if *fullSync && dryRun {
		log.Println("\n[dry-run] would TRUNCATE all Supabase tables")
	}

	// Sync all tables
	log.Println("\n=== Syncing data ===")
	for _, table := range tables {
		if table == "memberlogin_members" {
			syncMembers(my, pg, *fullSync)
			continue
		}
		if table == "memberlogin_plugin_log" {
			log.Printf("--- %s --- skipped (not in Supabase)", table)
			continue
		}
		syncTable(my, pg, table, *fullSync)
	}
	for _, table := range tables {
		reapplyAppDeletes(pg, table)
	}
	if len(keptRows) > 0 {
		log.Println("\n=== Kept the new app's version (edited or deleted there) ===")
		for t, n := range keptRows {
			log.Printf("  %-40s %d row(s)", t, n)
		}
	}

	// Repair identity sequences. Rows are inserted with their original
	// MySQL primary keys, and an explicit-ID INSERT never advances a
	// Postgres sequence — so after every sync each sequence still points
	// wherever it did before, and the next natural INSERT collides with
	// an existing row ("duplicate key value violates unique constraint").
	// This must run after every import, not just once: skipping it broke
	// pet creation and procedure recording entirely in July 2026.
	if !dryRun {
		log.Println("\n=== Resyncing identity sequences ===")
		resyncSequences(pg)
	} else {
		log.Println("\n[dry-run] would resync identity sequences")
	}

	// Post-flight verification — re-count Supabase and compare to MySQL.
	if !dryRun {
		log.Println("\n=== Post-sync verification ===")
		verifyCounts(my, pg, tables)
	}

	log.Println("\n=== Sync complete ===")
}

// resyncSequences fast-forwards every sequence in the public schema
// that trails MAX(id) in its own column, so the next INSERT gets a free
// ID instead of colliding with an imported row.
//
// Mirrors migration 009 (internal/database/migrations), which repairs
// the same drift at server start. Duplicated deliberately: this tool
// talks to Postgres over database/sql and pulling in GORM plus the
// whole migration registry just to share one loop is a worse trade
// than ~40 lines that can be read on their own.
//
// setval(seq, n, false) makes the NEXT nextval() return exactly n, so
// we pass MAX(id)+1. Failures are logged and skipped rather than fatal
// — a sequence we can't repair shouldn't discard a completed sync.
func resyncSequences(pg *sql.DB) {
	rows, err := pg.Query(`
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND column_default LIKE 'nextval%'`)
	if err != nil {
		log.Printf("  WARN could not list sequences: %v", err)
		return
	}
	type col struct{ table, column string }
	var cols []col
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.table, &c.column); err != nil {
			continue
		}
		cols = append(cols, c)
	}
	rows.Close()

	fixed := 0
	for _, c := range cols {
		var seqName sql.NullString
		if err := pg.QueryRow(`SELECT pg_get_serial_sequence($1, $2)`,
			"public."+c.table, c.column).Scan(&seqName); err != nil || !seqName.Valid || seqName.String == "" {
			continue
		}

		var lastVal int64
		if err := pg.QueryRow(fmt.Sprintf(`SELECT last_value FROM %s`, seqName.String)).Scan(&lastVal); err != nil {
			log.Printf("  WARN %s: read last_value: %v", seqName.String, err)
			continue
		}

		var maxID int64
		if err := pg.QueryRow(fmt.Sprintf(`SELECT COALESCE(MAX(%q), 0) FROM %q`,
			c.column, c.table)).Scan(&maxID); err != nil {
			log.Printf("  WARN %s.%s: read max: %v", c.table, c.column, err)
			continue
		}

		if lastVal >= maxID {
			continue
		}
		if _, err := pg.Exec(fmt.Sprintf(`SELECT setval('%s', %d, false)`,
			seqName.String, maxID+1)); err != nil {
			log.Printf("  WARN %s: setval failed: %v", seqName.String, err)
			continue
		}
		log.Printf("  %s.%s: %d → %d", c.table, c.column, lastVal, maxID+1)
		fixed++
	}
	log.Printf("  resynced %d sequence(s)", fixed)
}

// preflightRow is one line of the per-table sync plan.
type preflightRow struct {
	Table   string
	MySQL   int
	PG      int
	Missing int // for incremental mode: rows in MySQL but not in PG
	PGErr   string
}

// buildPreflight collects MySQL + Supabase row counts for every table.
// Missing-id detection only runs for tables with an `id` column —
// tables without one (rare, legacy) report missing=-1 and rely on
// truncate+full mode to refresh.
func buildPreflight(my, pg *sql.DB, tables []string) []preflightRow {
	rows := make([]preflightRow, 0, len(tables))
	for _, t := range tables {
		if t == "memberlogin_plugin_log" {
			continue
		}
		var (
			myCount, pgCount int
			pgErr            string
		)
		if err := my.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM `%s`", t)).Scan(&myCount); err != nil {
			myCount = -1
		}
		if err := pg.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, t)).Scan(&pgCount); err != nil {
			pgCount = -1
			pgErr = err.Error()
		}
		rows = append(rows, preflightRow{Table: t, MySQL: myCount, PG: pgCount, PGErr: pgErr})
	}
	return rows
}

// printPreflight renders the table-by-table diff between MySQL and
// Supabase so the operator can sanity-check the plan before any write.
func printPreflight(rows []preflightRow, full bool) {
	fmt.Println()
	fmt.Println("Table                                         MySQL       Supabase    Note")
	fmt.Println("--------------------------------------------------------------------------")
	var totalMy, totalPg int
	for _, r := range rows {
		note := ""
		switch {
		case r.PGErr != "":
			note = "PG error: " + r.PGErr
		case r.MySQL < 0:
			note = "MySQL count failed"
		case r.PG < 0:
			note = "Supabase missing"
		case full && r.PG > 0:
			note = "WILL BE TRUNCATED"
		case r.MySQL > r.PG:
			note = fmt.Sprintf("incremental: +%d", r.MySQL-r.PG)
		case r.MySQL < r.PG:
			note = fmt.Sprintf("PG has %d extra", r.PG-r.MySQL)
		}
		if r.MySQL >= 0 {
			totalMy += r.MySQL
		}
		if r.PG >= 0 {
			totalPg += r.PG
		}
		fmt.Printf("%-44s  %10d  %10d  %s\n", trunc(r.Table, 44), r.MySQL, r.PG, note)
	}
	fmt.Println("--------------------------------------------------------------------------")
	fmt.Printf("%-44s  %10d  %10d\n", "TOTAL", totalMy, totalPg)
	fmt.Println()
}

// verifyCounts re-reads counts after the sync and warns on mismatch.
// A mismatch isn't always a bug (legacy rows MySQL knows about but PG
// can't accept due to FK / column constraint), but it's worth surfacing.
func verifyCounts(my, pg *sql.DB, tables []string) {
	mismatches := 0
	for _, t := range tables {
		if t == "memberlogin_plugin_log" {
			continue
		}
		var myN, pgN int
		_ = my.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM `%s`", t)).Scan(&myN)
		if err := pg.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, t)).Scan(&pgN); err != nil {
			log.Printf("  WARN %-40s pg count failed: %v", t, err)
			continue
		}
		if myN != pgN {
			log.Printf("  WARN %-40s MySQL=%d  PG=%d  (diff=%d)", t, myN, pgN, myN-pgN)
			mismatches++
		}
	}
	if mismatches == 0 {
		log.Println("  all table counts match ✓")
	} else {
		log.Printf("  %d table(s) with row-count mismatch — investigate before relying on this DB", mismatches)
	}
}

// confirm reads a line from stdin and returns true only if the user
// typed exactly "yes" (case-insensitive). Anything else aborts.
func confirm(prompt string) bool {
	fmt.Print(prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(line), "yes")
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// startTimer / endTimer are wrappers used in syncTable so the per-
// table progress line includes wall time. Tiny, but answers "is it
// hung?" without checking htop.
func startTimer() time.Time { return time.Now() }
func elapsed(t time.Time) string {
	return time.Since(t).Round(time.Millisecond).String()
}

func getMySQLTables(my *sql.DB) []string {
	rows, err := my.Query("SHOW TABLES")
	if err != nil {
		log.Fatalf("SHOW TABLES: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		tables = append(tables, t)
	}
	return tables
}

func getMySQLColumns(my *sql.DB, table string) []string {
	rows, err := my.Query(fmt.Sprintf("SHOW COLUMNS FROM `%s`", table))
	if err != nil {
		log.Fatalf("SHOW COLUMNS %s: %v", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var field, typ, null, key string
		var defVal, extra sql.NullString
		rows.Scan(&field, &typ, &null, &key, &defVal, &extra)
		cols = append(cols, field)
	}
	return cols
}

func truncateAll(pg *sql.DB, tables []string) {
	for _, t := range tables {
		if t == "memberlogin_plugin_log" {
			continue
		}
		if dryRun {
			log.Printf("  [dry-run] would TRUNCATE %s", t)
			continue
		}
		_, err := pg.Exec(fmt.Sprintf("TRUNCATE TABLE \"%s\" CASCADE", t))
		if err != nil {
			log.Printf("  skip truncate %s: %v", t, err)
		} else {
			log.Printf("  truncated %s", t)
		}
	}
}

const batchSize = 200

// syncTable dynamically discovers columns and copies rows in batches.
// If fullSync=false, only copies rows with IDs missing in Supabase.
func syncTable(my, pg *sql.DB, table string, fullSync bool) {
	t0 := startTimer()
	defer func() { log.Printf("  done in %s", elapsed(t0)) }()

	cols := getMySQLColumns(my, table)
	if len(cols) == 0 {
		log.Printf("--- %s --- no columns found, skipping", table)
		return
	}

	var totalRows int
	var query string

	if fullSync {
		my.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM `%s`", table)).Scan(&totalRows)
		log.Printf("--- %s --- %d rows, %d columns", table, totalRows, len(cols))
		if totalRows == 0 {
			log.Printf("  empty, skipping")
			return
		}
		query = fmt.Sprintf("SELECT %s FROM `%s`", mysqlQuoteCols(cols), table)
	} else if updateExisting {
		// Refresh mode: re-copy EVERY upstream row and let the upsert
		// settle which are new and which changed. Comparing row
		// contents client-side would mean pulling both tables in full
		// anyway, so we let Postgres do the work.
		my.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM `%s`", table)).Scan(&totalRows)
		log.Printf("--- %s --- %d rows (refresh: insert new + update changed)", table, totalRows)
		if totalRows == 0 {
			log.Printf("  empty, skipping")
			return
		}
		query = fmt.Sprintf("SELECT %s FROM `%s`", mysqlQuoteCols(cols), table)
	} else {
		// Incremental: find missing IDs.
		//
		// NOTE: this mode copies only ids ABSENT from Postgres, so a row
		// edited upstream after it was first synced is never refreshed.
		// Use --update to repair that drift.
		missing := findMissingIDs(my, pg, table)
		totalRows = len(missing)
		log.Printf("--- %s --- %d missing rows", table, totalRows)
		if totalRows == 0 {
			return
		}
		// Build WHERE id IN (...) clause
		idStrs := make([]string, len(missing))
		for i, id := range missing {
			idStrs[i] = fmt.Sprintf("%d", id)
		}
		query = fmt.Sprintf("SELECT %s FROM `%s` WHERE id IN (%s)",
			mysqlQuoteCols(cols), table, strings.Join(idStrs, ","))
	}

	rows, err := my.Query(query)
	if err != nil {
		log.Printf("  ERROR query: %v", err)
		return
	}
	defer rows.Close()

	inserted := 0
	errors := 0
	var batch [][]interface{}

	for rows.Next() {
		dest := makeScanDest(len(cols))
		if err := rows.Scan(dest...); err != nil {
			errors++
			continue
		}
		batch = append(batch, extractValuesFor(table, cols, dest))

		if len(batch) >= batchSize {
			n, e := insertBatch(pg, table, cols, batch)
			inserted += n
			errors += e
			batch = batch[:0]
			if inserted%5000 == 0 {
				log.Printf("  progress: %d/%d", inserted, totalRows)
			}
		}
	}
	// Flush remaining
	if len(batch) > 0 {
		n, e := insertBatch(pg, table, cols, batch)
		inserted += n
		errors += e
	}
	if errors > 0 {
		log.Printf("  %d errors", errors)
	}
	log.Printf("  synced %d/%d", inserted, totalRows)
}

// --- Protecting new-app writes (see migration 016) ---
//
// While PHP and the new app both run, a row can be changed on either
// side. The app's changes are recorded in `app_changes` by a trigger;
// for those rows the app's version wins: the sync neither overwrites an
// edited row nor re-inserts a deleted one. Rows the app created carry ids
// from 1,000,000,000 up, which MySQL never reaches.

// protected[table] holds the ids the app has edited or deleted.
var protected = map[string]map[int64]bool{}

// keptRows counts, per table, MySQL rows skipped because the app owns them.
var keptRows = map[string]int{}

// haveAppChanges is true once app_changes is known to exist (migration
// 016); without it the sync behaves as before and protects nothing.
var haveAppChanges bool

// loadProtected reads app_changes once. A database without the table
// (migration 016 not yet applied) protects nothing and says so.
func loadProtected(pg *sql.DB) {
	// Only a database that has never run migration 016 may sync without
	// protection. Any other failure aborts: syncing with protection
	// silently off would overwrite the app's work.
	var exists bool
	if err := pg.QueryRow(`SELECT to_regclass('public.app_changes') IS NOT NULL`).Scan(&exists); err != nil {
		log.Fatalf("cannot check for app_changes: %v", err)
	}
	if !exists {
		log.Printf("  NOTE app_changes does not exist — run the backend once to apply migrations 016/017. New-app edits are NOT protected in this run.")
		return
	}
	rows, err := pg.Query(`SELECT table_name, row_id FROM app_changes`)
	if err != nil {
		log.Fatalf("cannot read app_changes: %v", err)
	}
	defer rows.Close()
	haveAppChanges = true
	n := 0
	for rows.Next() {
		var t string
		var id int64
		if rows.Scan(&t, &id) == nil {
			if protected[t] == nil {
				protected[t] = map[int64]bool{}
			}
			protected[t][id] = true
			n++
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("cannot read app_changes: %v", err)
	}
	log.Printf("  %d row(s) edited or deleted in the new app will be kept as they are", n)
}

// idIndex is the position of the `id` column, or -1.
func idIndex(cols []string) int {
	for i, c := range cols {
		if strings.EqualFold(c, "id") {
			return i
		}
	}
	return -1
}

func asInt64(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case []byte:
		n, err := strconv.ParseInt(string(x), 10, 64)
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	}
	return 0, false
}

// dropProtected removes rows the app owns from a batch.
func dropProtected(table string, cols []string, batch [][]interface{}) [][]interface{} {
	prot := protected[table]
	idx := idIndex(cols)
	if len(prot) == 0 || idx < 0 {
		return batch
	}
	out := batch[:0]
	for _, row := range batch {
		if id, ok := asInt64(row[idx]); ok && prot[id] {
			keptRows[table]++
			continue
		}
		out = append(out, row)
	}
	return out
}

// execAsSync runs one write in a transaction marked as the sync's own
// (the change-log trigger ignores it). It first takes the table's sync
// lock exclusively — see migration 017: that waits out every in-flight
// app transaction on the table, so the write's snapshot includes their
// app_changes entries and the protection guard cannot miss them.
func execAsSync(pg *sql.DB, table, query string, args ...interface{}) error {
	tx, err := pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SET LOCAL vetapp.sync = 'on'`); err != nil {
		return err
	}
	if haveAppChanges {
		if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('vetapp-sync|' || $1))`, table); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(query, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// reapplyAppDeletes deletes again any row the app deleted — covering a
// row re-inserted by a sync that read MySQL just before the app's delete.
func reapplyAppDeletes(pg *sql.DB, table string) {
	if dryRun || !haveAppChanges {
		return
	}
	var n int
	if err := pg.QueryRow(`SELECT COUNT(*) FROM app_changes WHERE table_name = $1 AND op = 'delete'`, table).Scan(&n); err != nil || n == 0 {
		return
	}
	q := fmt.Sprintf(`DELETE FROM %q WHERE id IN (SELECT row_id FROM app_changes WHERE table_name = $1 AND op = 'delete')`, table)
	if err := execAsSync(pg, table, q, table); err != nil {
		log.Printf("  WARN re-applying app deletes on %s: %v", table, err)
	}
}

// insertBatch inserts multiple rows in a single INSERT statement.
// In dry-run mode it counts the rows it *would* insert and returns,
// so the per-table progress numbers stay informative.
func insertBatch(pg *sql.DB, table string, cols []string, batch [][]interface{}) (int, int) {
	batch = dropProtected(table, cols, batch)
	if len(batch) == 0 {
		return 0, 0
	}
	if dryRun {
		return len(batch), 0
	}

	// Build: INSERT INTO "table" ("c1","c2") VALUES ($1,$2), ($3,$4), ...
	nCols := len(cols)
	var valueClauses []string
	var allVals []interface{}
	idx := 1
	for _, row := range batch {
		placeholders := make([]string, nCols)
		for j := range placeholders {
			placeholders[j] = fmt.Sprintf("$%d", idx)
			idx++
		}
		valueClauses = append(valueClauses, "("+strings.Join(placeholders, ", ")+")")
		allVals = append(allVals, row...)
	}

	suffix := conflictClause(pg, table, cols)

	query := fmt.Sprintf(
		"INSERT INTO \"%s\" (%s) VALUES %s%s",
		table, pgQuoteCols(cols), strings.Join(valueClauses, ", "), suffix,
	)

	err := execAsSync(pg, table, query, allVals...)
	if err != nil {
		// If batch fails, fall back to row-by-row so one bad row can't
		// discard the whole batch. Errors are reported per row.
		ok := 0
		bad := 0
		single := fmt.Sprintf(
			"INSERT INTO \"%s\" (%s) VALUES (%s)%s",
			table, pgQuoteCols(cols), pgPlaceholders(nCols), suffix,
		)
		for _, row := range batch {
			if err := execAsSync(pg, table, single, row...); err != nil {
				bad++
				// Log the first few so failures aren't silent. The old
				// behaviour incremented a counter and swallowed the
				// reason, which hid a real bug for weeks (see the
				// memberlogin_users timestamp failure).
				if bad <= 3 {
					log.Printf("  row error (%s id=%v): %v", table, firstVal(row), err)
				}
			} else {
				ok++
			}
		}
		return ok, bad
	}
	return len(batch), 0
}

// conflictClause returns the ON CONFLICT suffix for an upsert.
//
// Why this exists: MySQL is the source of truth and stays live while
// the PHP app is still in production, so a row can be EDITED upstream
// after it was first copied. Incremental sync only looks for ids
// missing from Postgres, so those edits were invisible — a measured
// 592 of 31,958 pets (1.9%) had drifted, including 388 microchip
// numbers present in MySQL but blank in Postgres because the chip was
// registered after the pet first synced. Chips are a pet's legal
// identifier; silently losing them is not acceptable at cutover.
//
// With updateExisting the insert becomes an upsert, so re-running the
// sync repairs drift instead of skipping it.
//
// The conflict target is the table's ACTUAL primary key, read from
// Postgres — not an assumed `id`. Several legacy tables use something
// else: memberlogin_options is keyed on (foreign_id, key), and
// assuming `id` there produced 83 duplicate-key failures per run.
// Tables with no primary key get a plain INSERT, since there is
// nothing to conflict on.
func conflictClause(pg *sql.DB, table string, cols []string) string {
	if !updateExisting {
		return ""
	}
	pk := primaryKeyCols(pg, table)
	if len(pk) == 0 {
		return ""
	}

	inPK := make(map[string]bool, len(pk))
	for _, c := range pk {
		inPK[strings.ToLower(c)] = true
	}

	sets := make([]string, 0, len(cols))
	for _, c := range cols {
		if inPK[strings.ToLower(c)] {
			continue // never rewrite the key we matched on
		}
		if table == "memberlogin_members" && strings.EqualFold(c, "last_login") {
			// Either system may record a sign-in; keep the later one.
			sets = append(sets, fmt.Sprintf(`%q = GREATEST(EXCLUDED.%q, %q.%q)`, c, c, table, c))
			continue
		}
		sets = append(sets, fmt.Sprintf("%q = EXCLUDED.%q", c, c))
	}

	target := make([]string, len(pk))
	for i, c := range pk {
		target[i] = fmt.Sprintf("%q", c)
	}
	conflict := " ON CONFLICT (" + strings.Join(target, ", ") + ")"

	if len(sets) == 0 {
		// Key-only table: nothing to update, but still don't error.
		return conflict + " DO NOTHING"
	}
	upd := conflict + " DO UPDATE SET " + strings.Join(sets, ", ")
	// An app edit landing while the sync runs (after loadProtected) is
	// still honoured: the upsert skips any row now in app_changes.
	if haveAppChanges && len(pk) == 1 && strings.EqualFold(pk[0], "id") {
		upd += fmt.Sprintf(` WHERE NOT EXISTS (SELECT 1 FROM app_changes c WHERE c.table_name = '%s' AND c.row_id = %q.id)`, table, table)
	}
	return upd
}

// pkCache memoises primary-key lookups so we don't re-query the
// catalog for every batch.
var pkCache = map[string][]string{}

// primaryKeyCols returns a table's primary-key columns in index order.
func primaryKeyCols(pg *sql.DB, table string) []string {
	if cached, ok := pkCache[table]; ok {
		return cached
	}
	rows, err := pg.Query(`
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass AND i.indisprimary
		ORDER BY array_position(i.indkey, a.attnum)`, table)
	if err != nil {
		log.Printf("  WARN %s: could not read primary key (%v) — falling back to plain INSERT", table, err)
		pkCache[table] = nil
		return nil
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			cols = append(cols, c)
		}
	}
	pkCache[table] = cols
	return cols
}

// firstVal renders a row's first column (the id, in practice) for
// error messages.
func firstVal(row []interface{}) string {
	if len(row) == 0 {
		return "?"
	}
	switch v := row[0].(type) {
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

// syncMembers handles memberlogin_members with password re-encryption.
func syncMembers(my, pg *sql.DB, fullSync bool) {
	cols := getMySQLColumns(my, "memberlogin_members")

	var totalRows int
	var query string

	if fullSync {
		my.QueryRow("SELECT COUNT(*) FROM `memberlogin_members`").Scan(&totalRows)
		log.Printf("--- memberlogin_members --- %d rows (with password re-encryption)", totalRows)
		if totalRows == 0 {
			return
		}
		query = fmt.Sprintf("SELECT %s FROM `memberlogin_members`", mysqlQuoteCols(cols))
	} else if updateExisting {
		// Refresh: re-copy every member so upstream edits (email, phone,
		// name changes made in the PHP app) reach Postgres. Passwords are
		// re-encrypted below exactly as in a full sync.
		my.QueryRow("SELECT COUNT(*) FROM `memberlogin_members`").Scan(&totalRows)
		log.Printf("--- memberlogin_members --- %d rows (refresh, with password re-encryption)", totalRows)
		if totalRows == 0 {
			return
		}
		query = fmt.Sprintf("SELECT %s FROM `memberlogin_members`", mysqlQuoteCols(cols))
	} else {
		missing := findMissingIDs(my, pg, "memberlogin_members")
		totalRows = len(missing)
		log.Printf("--- memberlogin_members --- %d missing rows (with password re-encryption)", totalRows)
		if totalRows == 0 {
			return
		}
		idStrs := make([]string, len(missing))
		for i, id := range missing {
			idStrs[i] = fmt.Sprintf("%d", id)
		}
		query = fmt.Sprintf("SELECT %s FROM `memberlogin_members` WHERE id IN (%s)",
			mysqlQuoteCols(cols), strings.Join(idStrs, ","))
	}

	pwIdx := -1
	for i, c := range cols {
		if c == "password" {
			pwIdx = i
			break
		}
	}

	rows, err := my.Query(query)
	if err != nil {
		log.Printf("  ERROR query: %v", err)
		return
	}
	defer rows.Close()

	inserted := 0
	errors := 0
	var batch [][]interface{}

	for rows.Next() {
		dest := make([]interface{}, len(cols))
		for i := range dest {
			dest[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(dest...); err != nil {
			errors++
			continue
		}

		vals := make([]interface{}, len(cols))
		for i, d := range dest {
			raw := *(d.(*sql.RawBytes))
			if raw == nil {
				vals[i] = nil
			} else if i == pwIdx && len(raw) > 0 {
				plain := aesDecrypt(raw, mysqlSalt)
				if plain != "" {
					vals[i] = aesEncrypt(plain, pgSalt)
				} else {
					vals[i] = raw
				}
			} else {
				vals[i] = string(raw)
			}
		}
		batch = append(batch, vals)

		if len(batch) >= batchSize {
			n, e := insertBatch(pg, "memberlogin_members", cols, batch)
			inserted += n
			errors += e
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		n, e := insertBatch(pg, "memberlogin_members", cols, batch)
		inserted += n
		errors += e
	}
	if errors > 0 {
		log.Printf("  %d errors", errors)
	}
	log.Printf("  synced %d/%d", inserted, totalRows)
}

// --- ID comparison helpers ---

func findMissingIDs(my, pg *sql.DB, table string) []int {
	myIDs := getIDs(my, fmt.Sprintf("SELECT id FROM `%s`", table))
	pgIDs := getIDs(pg, fmt.Sprintf("SELECT id FROM \"%s\"", table))

	pgSet := make(map[int]bool, len(pgIDs))
	for _, id := range pgIDs {
		pgSet[id] = true
	}

	var missing []int
	for _, id := range myIDs {
		if !pgSet[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

func getIDs(db *sql.DB, query string) []int {
	rows, err := db.Query(query)
	if err != nil {
		log.Printf("  WARNING getIDs failed: %v", err)
		return nil
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

// --- AES (MySQL-compatible AES-128-ECB) ---

func aesKey(salt string) []byte {
	key := make([]byte, 16)
	for i, b := range []byte(salt) {
		key[i%16] ^= b
	}
	return key
}

func aesDecrypt(ct []byte, salt string) string {
	if len(ct) == 0 || len(ct)%16 != 0 {
		return ""
	}
	block, _ := aes.NewCipher(aesKey(salt))
	plain := make([]byte, len(ct))
	for i := 0; i < len(ct); i += 16 {
		block.Decrypt(plain[i:i+16], ct[i:i+16])
	}
	if pad := int(plain[len(plain)-1]); pad > 0 && pad <= 16 {
		ok := true
		for i := len(plain) - pad; i < len(plain); i++ {
			if plain[i] != byte(pad) {
				ok = false
				break
			}
		}
		if ok {
			return string(plain[:len(plain)-pad])
		}
	}
	for len(plain) > 0 && plain[len(plain)-1] == 0 {
		plain = plain[:len(plain)-1]
	}
	return string(plain)
}

func aesEncrypt(plaintext, salt string) []byte {
	data := []byte(plaintext)
	pad := 16 - len(data)%16
	for i := 0; i < pad; i++ {
		data = append(data, byte(pad))
	}
	block, _ := aes.NewCipher(aesKey(salt))
	ct := make([]byte, len(data))
	for i := 0; i < len(data); i += 16 {
		block.Encrypt(ct[i:i+16], data[i:i+16])
	}
	return ct
}

// --- SQL helpers ---

func mysqlQuoteCols(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = "`" + c + "`"
	}
	return strings.Join(q, ", ")
}

func pgQuoteCols(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = "\"" + c + "\""
	}
	return strings.Join(q, ", ")
}

func pgPlaceholders(n int) string {
	p := make([]string, n)
	for i := range p {
		p[i] = fmt.Sprintf("$%d", i+1)
	}
	return strings.Join(p, ", ")
}

func makeScanDest(n int) []interface{} {
	d := make([]interface{}, n)
	for i := range d {
		// Scan into RawBytes-backed byte slices rather than strings:
		// some legacy columns hold binary (AES ciphertext), and forcing
		// those through a Go string produces invalid UTF-8 that
		// Postgres rejects with SQLSTATE 22021. extractValues decides
		// per column whether the bytes are text or binary.
		d[i] = new(sql.RawBytes)
	}
	return d
}

// binaryCols lists table.column pairs whose contents are binary, not
// text. They must be passed to Postgres as []byte so the driver sends
// them to a bytea column instead of trying to encode them as UTF-8.
//
// memberlogin_users.password is AES ciphertext; a byte like 0xa5 is
// not valid UTF-8 and made the admin row fail to sync on every run.
var binaryCols = map[string]bool{
	"memberlogin_users.password":   true,
	"memberlogin_members.password": true,
}

func extractValuesFor(table string, cols []string, dest []interface{}) []interface{} {
	v := make([]interface{}, len(dest))
	for i, d := range dest {
		raw := *(d.(*sql.RawBytes))
		if raw == nil {
			v[i] = nil
			continue
		}
		// Copy: RawBytes is only valid until the next rows.Next().
		b := make([]byte, len(raw))
		copy(b, raw)

		if i < len(cols) && binaryCols[table+"."+strings.ToLower(cols[i])] {
			v[i] = b
		} else {
			v[i] = string(b)
		}
	}
	return v
}

func extractValues(dest []interface{}) []interface{} {
	v := make([]interface{}, len(dest))
	for i, d := range dest {
		raw := *(d.(*sql.RawBytes))
		if raw == nil {
			v[i] = nil
			continue
		}
		b := make([]byte, len(raw))
		copy(b, raw)
		v[i] = string(b)
	}
	return v
}

// --- Rows the app created before migration 016 ---
//
// Before the app id range existed, an app insert took the next id after
// the highest one in Supabase, i.e. just above MySQL's. When PHP later
// issues that id, the sync overwrites the app's row with an unrelated
// MySQL record. Rows with MySQL.max < id < AppIDBase exist only in
// Supabase and have not been hit yet; they are listed on every run, and
// --relocate-app-rows moves them into the app range.
//
// Moving a row changes its id, so everything pointing at it is updated
// in the same transaction. A relocated pet gets a new id, which breaks
// any QR tag already printed for it — hence opt-in.

const appIDBase = 1000000000

// references lists, per table, the columns elsewhere that hold its id.
// Inventory taken from the Postgres catalogue (every column that holds a
// pet, member, procedure or home-procedure id). pets."userId" and
// memberlogin_options.foreign_id look similar but hold other things.
var references = map[string][][2]string{
	"vaccination": {{"procedure_files", "procedure_id"}, {"analysefile", "caseid"}},
	"pets": {{"vaccination", "uuid"}, {"operationdate", "uuid"}, {"eals", "uuid"}, {"alergy", "uuid"},
		{"homepro", "puuid"}, {"payments_ipay", "pet_id"}, {"procedure_files", "pet_id"}, {"paymethod", "uuid"},
		{"calendar", "uuid"}, {"operation", "uuid"}, {"procedurebi", "uuid"}},
	"memberlogin_members": {{"vaccination", "vetname"}, {"operationdate", "vetname"},
		{"procedure_files", "uploaded_by"}, {"idempotency_keys", "user_id"}, {"email_verification_tokens", "user_id"},
		{"memberlogin_files_members", "member_id"}, {"memberlogin_notes_members", "member_id"},
		{"operation", "vetname"}, {"procedurebi", "vetname"}},
	"homepro": {{"pro", "pruuid"}},
}

var relocatable = []string{"vaccination", "pets", "memberlogin_members", "paymethod", "shop", "prices",
	"eals", "alergy", "operationdate", "homepro", "pro", "analysefile", "payments_ipay"}

// handleEarlyAppRows returns how many early app rows remain unmoved.
func handleEarlyAppRows(my, pg *sql.DB, relocate bool) int {
	found, remaining := 0, 0
	for _, t := range relocatable {
		var myMax int64
		if err := my.QueryRow(fmt.Sprintf("SELECT COALESCE(MAX(id), 0) FROM `%s`", t)).Scan(&myMax); err != nil {
			continue
		}
		rows, err := pg.Query(fmt.Sprintf(`SELECT id FROM %q WHERE id > $1 AND id < $2 ORDER BY id`, t), myMax, appIDBase)
		if err != nil {
			continue
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		if len(ids) == 0 {
			continue
		}
		found += len(ids)
		if !relocate || dryRun {
			remaining += len(ids)
			log.Printf("  WARN %s: %d row(s) created by the app before migration 016 (ids %v…) will be overwritten once MySQL reaches them. Run with --relocate-app-rows.",
				t, len(ids), ids[:min(len(ids), 5)])
			continue
		}
		if !haveAppChanges {
			log.Fatal("--relocate-app-rows needs migrations 016/017: start the backend once, then re-run")
		}
		for _, id := range ids {
			newID, err := relocateRow(pg, t, id)
			if err != nil {
				// Stop rather than sync over a half-moved dataset.
				log.Fatalf("  relocating %s id=%d failed: %v — nothing after this was moved; fix and re-run", t, id, err)
			}
			log.Printf("  relocated %s id %d → %d", t, id, newID)
		}
	}
	if found == 0 {
		log.Println("  no early app rows below the app id range")
	}
	return remaining
}

func relocateRow(pg *sql.DB, table string, id int64) (int64, error) {
	tx, err := pg.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SET LOCAL vetapp.sync = 'on'`); err != nil {
		return 0, err
	}
	// Hold the moved table and every referencing table against app writes.
	lockTables := []string{table}
	for _, ref := range references[table] {
		lockTables = append(lockTables, ref[0])
	}
	sort.Strings(lockTables)
	for _, t := range lockTables {
		if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('vetapp-sync|' || $1))`, t); err != nil {
			return 0, err
		}
	}
	var seq sql.NullString
	if err := tx.QueryRow(`SELECT pg_get_serial_sequence($1, 'id')`, "public."+table).Scan(&seq); err != nil || !seq.Valid {
		return 0, fmt.Errorf("no id sequence")
	}
	// Make sure the sequence itself is in the app range, then draw from
	// it: a floor applied to nextval's result would hand out the same id
	// to every row moved before the sequence caught up.
	if _, err := tx.Exec(fmt.Sprintf(`SELECT setval('%s', GREATEST((SELECT last_value FROM %s), %d))`, seq.String, seq.String, appIDBase)); err != nil {
		return 0, err
	}
	var newID int64
	if err := tx.QueryRow(fmt.Sprintf(`SELECT nextval('%s')`, seq.String)).Scan(&newID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(fmt.Sprintf(`UPDATE %q SET id = $1 WHERE id = $2`, table), newID, id); err != nil {
		return 0, err
	}
	for _, ref := range references[table] {
		var dataType string
		if err := tx.QueryRow(`SELECT data_type FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`,
			ref[0], ref[1]).Scan(&dataType); err != nil {
			continue // referencing table not present
		}
		q := fmt.Sprintf(`UPDATE %q SET %q = $1 WHERE %q = $2`, ref[0], ref[1], ref[1])
		if dataType == "text" || strings.HasPrefix(dataType, "character") {
			_, err = tx.Exec(q, fmt.Sprint(newID), fmt.Sprint(id))
		} else {
			_, err = tx.Exec(q, newID, id)
		}
		if err != nil {
			return 0, fmt.Errorf("%s.%s: %w", ref[0], ref[1], err)
		}
	}
	// A protection entry under the old id would make every future sync
	// skip the unrelated MySQL row that later takes that id.
	if _, err := tx.Exec(`DELETE FROM app_changes WHERE table_name = $1 AND row_id = $2`, table, id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	delete(protected[table], id)
	return newID, nil
}

// guardedTruncate empties every table for --full, but only if doing so
// erases no new-app data — or the operator passed --discard-app-data.
// The check and the TRUNCATEs run in one transaction holding every
// table's sync lock, so no app write can land between them.
func guardedTruncate(pg *sql.DB, tables []string, earlyRows int, discard bool) {
	tx, err := pg.Begin()
	if err != nil {
		log.Fatalf("truncate: %v", err)
	}
	defer tx.Rollback()
	must := func(q string, args ...interface{}) {
		if _, err := tx.Exec(q, args...); err != nil {
			log.Fatalf("truncate: %s: %v", q, err)
		}
	}
	must(`SET LOCAL vetapp.sync = 'on'`)
	sorted := append([]string(nil), tables...)
	sort.Strings(sorted)
	if haveAppChanges {
		for _, t := range sorted {
			must(`SELECT pg_advisory_xact_lock(hashtext('vetapp-sync|' || $1))`, t)
		}
	}
	n := earlyRows
	if haveAppChanges {
		var c int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM app_changes`).Scan(&c); err != nil {
			log.Fatalf("truncate: counting app changes: %v", err)
		}
		n += c
	}
	for _, t := range relocatable {
		var c int
		if err := tx.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %q WHERE id >= %d`, t, appIDBase)).Scan(&c); err == nil {
			n += c
		}
	}
	if n > 0 && !discard {
		log.Fatalf("--full would erase %d row(s) of new-app data (created, edited or deleted there). "+
			"Use --update, which keeps them. Pass --discard-app-data only if losing them is intended.", n)
	}
	for _, t := range tables {
		if t == "memberlogin_plugin_log" {
			continue
		}
		if _, err := tx.Exec(fmt.Sprintf(`TRUNCATE TABLE %q CASCADE`, t)); err != nil {
			log.Printf("  skip %s: %v", t, err)
		}
	}
	if haveAppChanges {
		// Discarded: nothing is protected any more, or the rebuild would
		// skip the very MySQL rows it is meant to restore.
		must(`TRUNCATE app_changes`)
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("truncate commit: %v", err)
	}
	protected = map[string]map[int64]bool{}
	log.Printf("  truncated %d table(s)", len(tables))
}
