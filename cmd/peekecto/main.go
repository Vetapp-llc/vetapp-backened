package main

import (
	"fmt"
	"vetapp-backend/internal/config"
	"vetapp-backend/internal/database"
)

func main() {
	cfg, _ := config.Load()
	db, _ := database.Connect(cfg)

	// For each tp value, count records where the 4-slot pattern looks
	// "ecto-like" (i.e. at least one of vac1/vac2/vac3 is filled with
	// a non-numeric brand-looking string). That helps tell us where
	// the actual ectoparasite data lives.
	type row struct {
		Tp                          string
		Total                       int64
		WithVac1                    int64
		WithVac2                    int64
		WithVac3                    int64
	}
	var rows []row
	db.Raw(`
		SELECT tp,
		       COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE vac1 IS NOT NULL AND vac1 <> '') AS with_vac1,
		       COUNT(*) FILTER (WHERE vac2 IS NOT NULL AND vac2 <> '') AS with_vac2,
		       COUNT(*) FILTER (WHERE vac3 IS NOT NULL AND vac3 <> '') AS with_vac3
		FROM vaccination
		GROUP BY tp
		HAVING COUNT(*) FILTER (WHERE vac1 IS NOT NULL AND vac1 <> '') +
		       COUNT(*) FILTER (WHERE vac2 IS NOT NULL AND vac2 <> '') +
		       COUNT(*) FILTER (WHERE vac3 IS NOT NULL AND vac3 <> '') > 0
		ORDER BY (COUNT(*) FILTER (WHERE vac1 IS NOT NULL AND vac1 <> '') +
		          COUNT(*) FILTER (WHERE vac2 IS NOT NULL AND vac2 <> '') +
		          COUNT(*) FILTER (WHERE vac3 IS NOT NULL AND vac3 <> '')) DESC
		LIMIT 10
	`).Scan(&rows)

	fmt.Println("Which tp values use the multi-slot vac1/2/3 pattern?")
	fmt.Printf("  %-6s %8s %10s %10s %10s\n", "tp", "total", "with_vac1", "with_vac2", "with_vac3")
	for _, r := range rows {
		fmt.Printf("  %-6s %8d %10d %10d %10d\n", r.Tp, r.Total, r.WithVac1, r.WithVac2, r.WithVac3)
	}

	// Sample non-empty values from vac1/vac2/vac3 across ALL tp values
	for _, col := range []string{"vac1", "vac2", "vac3"} {
		fmt.Printf("\nTop 10 values for `%s` across all procedures:\n", col)
		type vrow struct {
			Tp    string
			Val   string
			Total int64
		}
		var vrows []vrow
		db.Raw(fmt.Sprintf(`
			SELECT tp, %s AS val, COUNT(*) AS total
			FROM vaccination
			WHERE %s IS NOT NULL AND %s <> ''
			GROUP BY tp, %s
			ORDER BY total DESC
			LIMIT 10
		`, col, col, col, col)).Scan(&vrows)
		for _, r := range vrows {
			fmt.Printf("  tp=%-4s  %-50s %d\n", r.Tp, truncate(r.Val, 50), r.Total)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
