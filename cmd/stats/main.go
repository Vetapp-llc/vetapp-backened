// stats prints high-level counts about the production VetApp data
// (clinics, users by role, pets, procedures, payments, etc.).
//
// Run: `go run ./cmd/stats`
package main

import (
	"fmt"
	"log"
	"time"

	"vetapp-backend/internal/config"
	"vetapp-backend/internal/database"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	db, err := database.Connect(cfg)
	if err != nil {
		log.Fatalf("db: %v", err)
	}

	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════════════════")
	fmt.Println("  VetApp — Production Data Snapshot")
	fmt.Printf("  %s\n", time.Now().Format("2006-01-02 15:04:05 MST"))
	fmt.Println("══════════════════════════════════════════════════════════════")

	// ---------- Users by role ----------
	type roleRow struct {
		GroupID int
		Total   int64
	}
	var roles []roleRow
	db.Raw(`SELECT group_id, COUNT(*) AS total FROM memberlogin_members GROUP BY group_id ORDER BY group_id`).Scan(&roles)

	roleName := map[int]string{1: "Owners (mobile users)", 2: "Vets / clinic staff", 4: "Super-admins"}
	var ownerCount, vetCount, adminCount, totalUsers int64
	fmt.Println("\n┌─ Users by role")
	for _, r := range roles {
		name := roleName[r.GroupID]
		if name == "" {
			name = fmt.Sprintf("group_id=%d", r.GroupID)
		}
		fmt.Printf("│   %-30s %6d\n", name, r.Total)
		totalUsers += r.Total
		switch r.GroupID {
		case 1:
			ownerCount = r.Total
		case 2:
			vetCount = r.Total
		case 4:
			adminCount = r.Total
		}
	}
	fmt.Printf("│   %-30s %6d\n", "TOTAL", totalUsers)
	_ = adminCount

	// ---------- Clinics (distinct zip on vet/admin staff) ----------
	var clinicCount int64
	db.Raw(`SELECT COUNT(DISTINCT zip) FROM memberlogin_members WHERE zip <> '' AND group_id IN (2,4)`).Scan(&clinicCount)
	fmt.Println("\n┌─ Clinics")
	fmt.Printf("│   %-30s %6d\n", "Distinct clinic codes", clinicCount)

	type clinicRow struct {
		Zip         string
		CompanyName string
		Staff       int64
	}
	var topClinics []clinicRow
	db.Raw(`
		SELECT zip,
		       COALESCE(MAX(company_name), '') AS company_name,
		       COUNT(*) AS staff
		FROM memberlogin_members
		WHERE group_id IN (2,4) AND zip <> ''
		GROUP BY zip
		ORDER BY staff DESC
		LIMIT 10
	`).Scan(&topClinics)
	fmt.Println("│")
	fmt.Println("│   Top 10 clinics by staff size:")
	for _, c := range topClinics {
		name := c.CompanyName
		if name == "" {
			name = "(no name)"
		}
		fmt.Printf("│     %-9s  %-40s %3d staff\n", c.Zip, truncate(name, 40), c.Staff)
	}

	// ---------- Pets ----------
	var petCount, petsWithChip, petsWithBirth int64
	db.Raw(`SELECT COUNT(*) FROM pets`).Scan(&petCount)
	db.Raw(`SELECT COUNT(*) FROM pets WHERE chip <> ''`).Scan(&petsWithChip)
	db.Raw(`SELECT COUNT(*) FROM pets WHERE date <> ''`).Scan(&petsWithBirth)

	type speciesRow struct {
		Pet   string
		Total int64
	}
	var bySpecies []speciesRow
	db.Raw(`SELECT pet, COUNT(*) AS total FROM pets WHERE pet <> '' GROUP BY pet ORDER BY total DESC LIMIT 10`).Scan(&bySpecies)

	fmt.Println("\n┌─ Pets")
	fmt.Printf("│   %-30s %6d\n", "Total pets", petCount)
	fmt.Printf("│   %-30s %6d\n", "With microchip", petsWithChip)
	fmt.Printf("│   %-30s %6d\n", "With birth date", petsWithBirth)
	fmt.Println("│")
	fmt.Println("│   By species (top 10):")
	for _, s := range bySpecies {
		fmt.Printf("│     %-30s %6d\n", truncate(s.Pet, 30), s.Total)
	}

	// ---------- Procedures (medical records) ----------
	var procCount int64
	db.Raw(`SELECT COUNT(*) FROM vaccination`).Scan(&procCount)
	type procTypeRow struct {
		TP    int
		Total int64
	}
	var byType []procTypeRow
	db.Raw(`SELECT tp, COUNT(*) AS total FROM vaccination GROUP BY tp ORDER BY total DESC LIMIT 10`).Scan(&byType)

	fmt.Println("\n┌─ Medical records (vaccination table)")
	fmt.Printf("│   %-30s %6d\n", "Total records", procCount)
	fmt.Println("│")
	fmt.Println("│   By procedure type (top 10):")
	procName := map[int]string{
		1: "Vaccination", 2: "Test", 3: "Dehelminization", 4: "Ectoparasite",
		5: "Surgery", 6: "Dental", 7: "X-Ray", 8: "Ultrasound", 9: "ECG",
		10: "Endoscopy", 100: "Sterilization", 101: "Rabies vaccine",
		102: "Microchipping", 103: "Euthanasia", 104: "Laboratory",
		108: "Consultation", 109: "Manipulation",
	}
	for _, t := range byType {
		name := procName[t.TP]
		if name == "" {
			name = "(other)"
		}
		fmt.Printf("│     tp=%-4d  %-25s %6d\n", t.TP, name, t.Total)
	}

	// ---------- Payments / revenue (clinic POS) ----------
	// Legacy MySQL column name is "sum" (a SQL reserved word). Filter to
	// rows that look like positive numbers since the table contains junk
	// like "-" and empty strings.
	var payCount int64
	var paySumStr string
	db.Raw(`SELECT COUNT(*) FROM paymethod`).Scan(&payCount)
	db.Raw(`
		SELECT COALESCE(SUM("sum"::numeric), 0)::text
		FROM paymethod
		WHERE "sum" ~ '^[0-9]+(\.[0-9]+)?$'
	`).Scan(&paySumStr)

	fmt.Println("\n┌─ Clinic payments (paymethod)")
	fmt.Printf("│   %-30s %6d\n", "Transactions", payCount)
	fmt.Printf("│   %-30s %s GEL\n", "Lifetime gross revenue", paySumStr)

	// ---------- Subscriptions (mobile owner payments) ----------
	var subCount, subSuccessCount int64
	var subSumStr string
	db.Raw(`SELECT COUNT(*) FROM payments_ipay`).Scan(&subCount)
	db.Raw(`SELECT COUNT(*) FROM payments_ipay WHERE status = 'success'`).Scan(&subSuccessCount)
	// Schema note: column is "price" (numeric), not "amount". The Go
	// model in internal/models/subscription.go calls it Amount but the
	// table comes from a legacy migration where the column is `price`.
	db.Raw(`
		SELECT COALESCE(SUM(price), 0)::text
		FROM payments_ipay
		WHERE status = 'success'
	`).Scan(&subSumStr)

	fmt.Println("\n┌─ Mobile subscriptions (payments_ipay)")
	fmt.Printf("│   %-30s %6d\n", "Total payment attempts", subCount)
	fmt.Printf("│   %-30s %6d\n", "Successful payments", subSuccessCount)
	fmt.Printf("│   %-30s %s GEL\n", "Lifetime subscription revenue", subSumStr)

	// ---------- Appointments ----------
	var apptCount int64
	db.Raw(`SELECT COUNT(*) FROM operationdate`).Scan(&apptCount)
	fmt.Println("\n┌─ Appointments (operationdate)")
	fmt.Printf("│   %-30s %6d\n", "Total appointments", apptCount)

	// ---------- Active recently ----------
	var activeOwners30, activeStaff30 int64
	db.Raw(`SELECT COUNT(*) FROM memberlogin_members WHERE group_id = 1 AND last_login >= NOW() - INTERVAL '30 days'`).Scan(&activeOwners30)
	db.Raw(`SELECT COUNT(*) FROM memberlogin_members WHERE group_id IN (2,4) AND last_login >= NOW() - INTERVAL '30 days'`).Scan(&activeStaff30)

	fmt.Println("\n┌─ Activity (last 30 days)")
	fmt.Printf("│   %-30s %6d\n", "Active owners", activeOwners30)
	fmt.Printf("│   %-30s %6d\n", "Active staff", activeStaff30)

	// ---------- Resume-friendly summary ----------
	fmt.Println("\n══════════════════════════════════════════════════════════════")
	fmt.Println("  Resume-ready highlights")
	fmt.Println("══════════════════════════════════════════════════════════════")
	fmt.Printf("  • %d veterinary clinics on the platform\n", clinicCount)
	fmt.Printf("  • %d clinic staff (vets + admins) using daily\n", vetCount+adminCount)
	fmt.Printf("  • %d pet owners with mobile-app accounts\n", ownerCount)
	fmt.Printf("  • %d registered pets in the database\n", petCount)
	fmt.Printf("  • %d medical records (procedures, vaccinations, tests)\n", procCount)
	fmt.Printf("  • %d in-clinic POS transactions\n", payCount)
	fmt.Printf("  • %d mobile-app subscription payments\n", subCount)
	fmt.Printf("  • %d appointments booked\n", apptCount)
	fmt.Println()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
