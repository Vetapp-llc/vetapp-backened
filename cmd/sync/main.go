// Sync tool: full copy from MySQL (cPanel) → Supabase (PostgreSQL).
//
// Modes:
//   go run ./cmd/sync                     # incremental — inserts only missing-by-id rows
//   go run ./cmd/sync --full              # TRUNCATE + full re-copy (interactive confirm)
//   go run ./cmd/sync --full --yes        # full re-copy, skip confirmation prompt
//   go run ./cmd/sync --dry-run           # print row-count diff, no writes
//   go run ./cmd/sync --full --dry-run    # plan a full re-copy without doing it
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
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	mysqlDSN  = "vetappge_kobula131:chombe1981@tcp(91.239.207.27:3306)/vetappge_login"
	mysqlSalt = "RZ8HU1EB"
	pgSalt    = "DW3Z07FI"
	pgDSN     = "host=aws-1-eu-central-1.pooler.supabase.com port=6543 user=postgres.qslnfhnnzsfmtnochnce password=Vetapp1234@. dbname=postgres sslmode=require default_query_exec_mode=simple_protocol"
)

// dryRun, when true, suppresses every write (TRUNCATE, INSERT) and
// turns the tool into a row-count diff report.
var dryRun bool

func main() {
	var (
		fullSync = flag.Bool("full", false, "TRUNCATE Supabase + full re-copy from MySQL (default: incremental)")
		dr       = flag.Bool("dry-run", false, "Read-only mode — report per-table counts, do not write")
		assumeYes = flag.Bool("yes", false, "Skip the interactive confirmation prompt for --full")
	)
	flag.Parse()
	dryRun = *dr

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

	if *fullSync && !dryRun {
		if !*assumeYes {
			if !confirm("Type 'yes' to TRUNCATE every Supabase table listed above and re-copy from MySQL: ") {
				log.Println("Aborted by operator.")
				return
			}
		}
		log.Println("\n=== Truncating Supabase tables ===")
		truncateAll(pg, tables)
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
	} else {
		// Incremental: find missing IDs
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
		batch = append(batch, extractValues(dest))

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

// insertBatch inserts multiple rows in a single INSERT statement.
// In dry-run mode it counts the rows it *would* insert and returns,
// so the per-table progress numbers stay informative.
func insertBatch(pg *sql.DB, table string, cols []string, batch [][]interface{}) (int, int) {
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

	query := fmt.Sprintf(
		"INSERT INTO \"%s\" (%s) VALUES %s",
		table, pgQuoteCols(cols), strings.Join(valueClauses, ", "),
	)

	_, err := pg.Exec(query, allVals...)
	if err != nil {
		// If batch fails, fall back to row-by-row to skip bad rows
		ok := 0
		bad := 0
		single := fmt.Sprintf(
			"INSERT INTO \"%s\" (%s) VALUES (%s)",
			table, pgQuoteCols(cols), pgPlaceholders(nCols),
		)
		for _, row := range batch {
			if _, err := pg.Exec(single, row...); err != nil {
				bad++
			} else {
				ok++
			}
		}
		return ok, bad
	}
	return len(batch), 0
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
		d[i] = new(sql.NullString)
	}
	return d
}

func extractValues(dest []interface{}) []interface{} {
	v := make([]interface{}, len(dest))
	for i, d := range dest {
		ns := d.(*sql.NullString)
		if ns.Valid {
			v[i] = ns.String
		} else {
			v[i] = nil
		}
	}
	return v
}
