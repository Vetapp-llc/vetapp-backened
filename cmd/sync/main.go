// Sync tool: full copy from MySQL (cPanel) → Supabase (PostgreSQL).
// Run: go run ./cmd/sync
//
// How it works:
//   1. Truncates ALL Supabase tables
//   2. Copies ALL rows from every MySQL table
//   3. For memberlogin_members, re-encrypts passwords (MySQL salt → Supabase salt)
//
// MySQL is the source of truth. Supabase becomes an exact copy.

package main

import (
	"crypto/aes"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	mysqlDSN  = "vetappge_kobula131:chombe1981@tcp(91.239.207.27:3306)/vetappge_login"
	mysqlSalt = "RZ8HU1EB"
	pgSalt    = "DW3Z07FI"
	pgDSN     = "host=aws-1-eu-central-1.pooler.supabase.com port=6543 user=postgres.qslnfhnnzsfmtnochnce password=Vetapp1234@. dbname=postgres sslmode=require default_query_exec_mode=simple_protocol"
)

func main() {
	// --full flag: truncate + full re-copy. Default: incremental (missing rows only).
	fullSync := len(os.Args) > 1 && os.Args[1] == "--full"

	if fullSync {
		log.Println("=== MySQL → Supabase FULL Sync ===")
	} else {
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

	if fullSync {
		log.Println("\n=== Truncating Supabase tables ===")
		truncateAll(pg, tables)
		for _, t := range []string{"memberlogin_sms_codes"} {
			if _, err := pg.Exec(fmt.Sprintf("TRUNCATE TABLE \"%s\" CASCADE", t)); err != nil {
				log.Printf("  skip %s: %v", t, err)
			} else {
				log.Printf("  truncated %s", t)
			}
		}
	}

	// Sync all tables
	log.Println("\n=== Syncing data ===")
	for _, table := range tables {
		if table == "memberlogin_members" {
			syncMembers(my, pg, fullSync)
			continue
		}
		if table == "memberlogin_plugin_log" {
			log.Printf("--- %s --- skipped (not in Supabase)", table)
			continue
		}
		syncTable(my, pg, table, fullSync)
	}

	log.Println("\n=== Sync complete ===")
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
func insertBatch(pg *sql.DB, table string, cols []string, batch [][]interface{}) (int, int) {
	if len(batch) == 0 {
		return 0, 0
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
