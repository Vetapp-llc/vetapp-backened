package main

import (
	"database/sql"
	"fmt"
	"log"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	mysqlDSN = "vetappge_kobula131:chombe1981@tcp(91.239.207.27:3306)/vetappge_login"
	pgDSN    = "host=aws-1-eu-central-1.pooler.supabase.com port=6543 user=postgres.qslnfhnnzsfmtnochnce password=Vetapp1234@. dbname=postgres sslmode=require default_query_exec_mode=simple_protocol"
)

func count(db *sql.DB, query string) int {
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		return -1
	}
	return n
}

func main() {
	my, err := sql.Open("mysql", mysqlDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer my.Close()

	pg, err := sql.Open("pgx", pgDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer pg.Close()

	// Get all MySQL tables
	rows, err := my.Query("SHOW TABLES")
	if err != nil {
		log.Fatal(err)
	}
	var myTables []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		myTables = append(myTables, t)
	}
	rows.Close()

	fmt.Printf("%-40s %8s %8s %8s\n", "TABLE", "MySQL", "Supabase", "DIFF")
	fmt.Println("------------------------------------------------------------------------")
	allGood := true
	for _, t := range myTables {
		myCount := count(my, "SELECT COUNT(*) FROM `"+t+"`")
		pgCount := count(pg, "SELECT COUNT(*) FROM \""+t+"\"")
		if pgCount == -1 {
			fmt.Printf("%-40s %8d %8s %8s\n", t, myCount, "MISSING", "—")
			allGood = false
			continue
		}
		diff := myCount - pgCount
		status := ""
		if diff != 0 {
			status = " <--"
			allGood = false
		}
		fmt.Printf("%-40s %8d %8d %8d%s\n", t, myCount, pgCount, diff, status)
	}

	// Also check Supabase tables not in MySQL
	fmt.Println("\n--- Supabase-only tables (not in MySQL) ---")
	pgRows, err := pg.Query("SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename")
	if err != nil {
		log.Fatal(err)
	}
	mySet := make(map[string]bool)
	for _, t := range myTables {
		mySet[t] = true
	}
	for pgRows.Next() {
		var t string
		pgRows.Scan(&t)
		if !mySet[t] {
			pgCount := count(pg, "SELECT COUNT(*) FROM \""+t+"\"")
			fmt.Printf("  %-38s %8d rows\n", t, pgCount)
		}
	}
	pgRows.Close()

	fmt.Println("------------------------------------------------------------------------")
	if allGood {
		fmt.Println("All tables in sync!")
	} else {
		fmt.Println("Some tables need attention!")
	}
}
