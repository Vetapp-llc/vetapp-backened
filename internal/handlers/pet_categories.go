package handlers

import (
	"vetapp-backend/internal/models"

	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// buildPetCategories returns the per-type record counts for a pet
// (procedure types + the synthetic tp=999 allergy bucket).
//
// Owner pet detail and the public profile rendered the same data with
// duplicated 50-line snippets — this helper consolidates the two paths
// and runs the procedure-tp aggregation and the allergy count in
// parallel goroutines instead of sequentially.
//
// Returns the procedure categories first (ordered as the DB returns
// them, descending by id is not guaranteed). The allergy bucket, if
// any, is appended last so callers don't have to special-case the tp=999
// row when rendering.
func buildPetCategories(db *gorm.DB, petID string) []ProcedureCategoryCount {
	type tpCount struct {
		TP    int
		Count int
	}

	var counts []tpCount
	var allergyCount int64

	g := new(errgroup.Group)
	g.Go(func() error {
		return db.Model(&models.Procedure{}).
			Select("tp as tp, COUNT(*) as count").
			Where("uuid = ?", petID).
			Group("tp").
			Scan(&counts).Error
	})
	g.Go(func() error {
		return db.Model(&models.Allergy{}).
			Where("uuid = ?", petID).
			Count(&allergyCount).Error
	})
	// Errors from either query degrade gracefully — we just return
	// fewer categories rather than 500ing the whole pet detail page.
	_ = g.Wait()

	categories := make([]ProcedureCategoryCount, 0, len(counts)+1)
	for _, c := range counts {
		name := procedureTypeNames[c.TP]
		if name == "" {
			name = "სხვა"
		}
		categories = append(categories, ProcedureCategoryCount{
			TP:    c.TP,
			Name:  name,
			Count: c.Count,
		})
	}
	if allergyCount > 0 {
		categories = append(categories, ProcedureCategoryCount{
			TP:    999,
			Name:  "ალერგია / დაავადება",
			Count: int(allergyCount),
		})
	}
	return categories
}
