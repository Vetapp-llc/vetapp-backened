package main

import (
	"database/sql"
	"fmt"
	"log"

	_ "github.com/go-sql-driver/mysql"
)

const mysqlDSN = "vetappge_kobula131:chombe1981@tcp(91.239.207.27:3306)/vetappge_login"

func main() {
	my, err := sql.Open("mysql", mysqlDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer my.Close()

	// Vetex staff
	fmt.Println("=== Vetex staff (zip=405284393) ===")
	rows, _ := my.Query("SELECT id, first_name, last_name, email, group_id FROM memberlogin_members WHERE zip = '405284393'")
	for rows.Next() {
		var id, gid int
		var fn, ln, email sql.NullString
		rows.Scan(&id, &fn, &ln, &email, &gid)
		role := "owner"
		if gid == 2 {
			role = "vet"
		}
		if gid == 4 {
			role = "admin"
		}
		fmt.Printf("  ID=%-5d %-25s %-15s %-35s %s\n", id, fn.String, ln.String, email.String, role)
	}
	rows.Close()

	// Last 20 payments
	fmt.Println("\n=== Last 20 Vetex payments ===")
	fmt.Printf("%-8s %-12s %-10s %-10s %-6s\n", "ID", "DATE", "PET_ID", "SUM(GEL)", "PAY")
	fmt.Println("------------------------------------------------")
	rows, _ = my.Query("SELECT id, date, uuid, `sum`, pay FROM paymethod WHERE zip = '405284393' ORDER BY id DESC LIMIT 20")
	type payment struct {
		id     int
		date   string
		petID  string
		sum    string
		pay    string
	}
	var payments []payment
	for rows.Next() {
		var p payment
		var date, uuid, sum, pay sql.NullString
		rows.Scan(&p.id, &date, &uuid, &sum, &pay)
		p.date = date.String
		p.petID = uuid.String
		p.sum = sum.String
		p.pay = "card"
		if pay.String == "ნაღდი ანგარიშწორება" {
			p.pay = "cash"
		}
		payments = append(payments, p)
		fmt.Printf("%-8d %-12s %-10s %-10s %-6s\n", p.id, p.date, p.petID, p.sum, p.pay)
	}
	rows.Close()

	// Look up each pet individually (no slow JOIN)
	fmt.Println("\n=== Pet + owner details for those payments ===")
	fmt.Printf("%-8s %-10s %-20s %-25s %-15s\n", "PAY_ID", "SUM", "PET_NAME", "OWNER", "PHONE")
	fmt.Println("------------------------------------------------------------------------")
	for _, p := range payments {
		var petName, owner, phone sql.NullString
		err := my.QueryRow("SELECT name, first_name, phone FROM pets WHERE id = ?", p.petID).Scan(&petName, &owner, &phone)
		if err != nil {
			fmt.Printf("%-8d %-10s %-20s %-25s %-15s\n", p.id, p.sum, "(not found)", "", "")
			continue
		}
		fmt.Printf("%-8d %-10s %-20s %-25s %-15s\n", p.id, p.sum, petName.String, owner.String, phone.String)
	}

	// Today's payments
	fmt.Println("\n=== Vetex today (2026-04-09) ===")
	var cnt int
	var total sql.NullFloat64
	my.QueryRow("SELECT COUNT(*), SUM(CAST(`sum` AS DECIMAL(10,2))) FROM paymethod WHERE zip = '405284393' AND date = '2026-04-09'").Scan(&cnt, &total)
	fmt.Printf("  Payments: %d, Total: %.0f GEL\n", cnt, total.Float64)

	// Last 7 days
	fmt.Println("\n=== Vetex last 7 days ===")
	rows, _ = my.Query("SELECT date, COUNT(*), SUM(CAST(`sum` AS DECIMAL(10,2))) FROM paymethod WHERE zip = '405284393' AND date >= '2026-04-03' GROUP BY date ORDER BY date DESC")
	for rows.Next() {
		var date string
		var c int
		var t float64
		rows.Scan(&date, &c, &t)
		fmt.Printf("  %s: %d payments, %.0f GEL\n", date, c, t)
	}
	rows.Close()
}
