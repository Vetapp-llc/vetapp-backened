package itest

import (
	"net/http"
	"testing"
	"vetapp-backend/internal/handlers"
	"vetapp-backend/internal/models"
)

func TestSubscriptionPackagesExposeServerProductMapping(t *testing.T) {
	reset(t)
	must(t, db.Exec(`DELETE FROM mprice`).Error)
	for _, pkg := range []models.Package{
		{ID: 1, Name: "3 months", Price: "14.99", Duration: 90, AppleProductID: "com.vetapp.sub.3month"},
		{ID: 2, Name: "Card only", Price: "1.00", Duration: 1},
	} {
		must(t, db.Create(&pkg).Error)
	}
	res := call(t, 0, "GET", "/api/subscriptions/packages", nil)
	expect(t, res, http.StatusOK)
	var packages []handlers.PackageResponse
	res.json(t, &packages)
	if len(packages) != 2 {
		t.Fatalf("expected both card and App Store plans, got %d", len(packages))
	}
	for _, pkg := range packages {
		if pkg.ID == 1 && pkg.AppleProductID != "com.vetapp.sub.3month" {
			t.Fatal("server product mapping lost")
		}
		if pkg.ID == 2 && pkg.AppleProductID != "" {
			t.Fatal("invented an App Store product for card-only plan")
		}
	}
}
