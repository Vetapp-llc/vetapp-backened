package itest

import (
	"fmt"
	"net/http"
	"testing"

	"vetapp-backend/internal/models"
)

// Clinic search finds a pet the clinic treated but did not register
// (e.g. registered by its owner in the app), by pet or owner name; a
// clinic that never saw the pet still cannot find it by name.
func TestSearchCoversTreatedPetsAndPetNames(t *testing.T) {
	reset(t)
	walkIn := models.Pet{ID: 90010, UUID: "01001001001", Name: "ბობი", Pet: "ძაღლი", Status: 1, FirstName: "ნინო ბერიძე"}
	must(t, db.Create(&walkIn).Error)
	insertProc(t, models.Procedure{UUID: fmt.Sprint(walkIn.ID), TP: 102, SK: clinicA, Price: "20", Date: "2026-10-19"})

	var page struct {
		Total int64
		Data  []struct {
			ID         string `json:"id"`
			PersonalID string `json:"personalId"`
		}
	}
	for _, q := range []string{"ბობი", "ნინო"} {
		r := call(t, vetA, "GET", "/api/pets?search="+q, nil)
		expect(t, r, http.StatusOK)
		r.json(t, &page)
		if page.Total != 1 || page.Data[0].ID != fmt.Sprint(walkIn.ID) {
			t.Fatalf("pets search %q: %+v", q, page)
		}
		r = call(t, vetA, "GET", "/api/owners?search="+q, nil)
		expect(t, r, http.StatusOK)
		r.json(t, &page)
		if page.Total != 1 || page.Data[0].PersonalID != walkIn.UUID {
			t.Fatalf("owners search %q: %+v", q, page)
		}
		// Clinic B never treated it.
		r = call(t, vetB, "GET", "/api/pets?search="+q, nil)
		r.json(t, &page)
		if page.Total != 0 {
			t.Fatalf("clinic B found %q", q)
		}
	}
	// Owner search by pet name, for a pet registered here.
	r := call(t, vetA, "GET", "/api/owners?search=იოში", nil)
	r.json(t, &page)
	if page.Total != 1 || page.Data[0].PersonalID != ownerID {
		t.Fatalf("owner by pet name: %+v", page)
	}
}

func TestRangeStatsFiltersByPeriodAndType(t *testing.T) {
	reset(t)
	for _, p := range []models.Procedure{
		{TP: 102, TPName: "კარდიოლოგია", Price: "20", Date: "2026-10-19"},
		{TP: 109, TPName: "რადიოლოგია", Price: "30", Date: "2026-10-19"},
		{TP: 102, TPName: "კარდიოლოგია", Price: "25.5", Date: "2026-10-01"},
		{TP: 102, TPName: "კარდიოლოგია", Price: "99", Date: "2026-08-01"}, // outside
	} {
		p.UUID, p.SK = fmt.Sprint(petA), clinicA
		insertProc(t, p)
	}
	insertProc(t, models.Procedure{UUID: fmt.Sprint(petB), TP: 102, SK: clinicB, Price: "7", Date: "2026-10-19"})

	var s struct {
		Count      int
		Total      string
		Procedures []struct {
			TP    string
			Count int
			Total string
		}
		Types     []struct{ TP string }
		Breakdown []struct {
			Period string
			Count  int
		}
		ByMonth bool
	}
	r := call(t, vetA, "GET", "/api/stats/clinic/range?from=2026-09-20&to=2026-10-19", nil)
	expect(t, r, http.StatusOK)
	r.json(t, &s)
	if s.Count != 3 || s.Total != "75.5" || len(s.Types) != 2 || len(s.Breakdown) != 2 || s.ByMonth {
		t.Fatalf("all types: %+v", s)
	}
	r = call(t, vetA, "GET", "/api/stats/clinic/range?from=2026-09-20&to=2026-10-19&tp=102", nil)
	r.json(t, &s)
	if s.Count != 2 || s.Total != "45.5" || len(s.Procedures) != 1 || len(s.Types) != 2 {
		t.Fatalf("cardiology only: %+v", s)
	}
	r = call(t, vetA, "GET", "/api/stats/clinic/range?from=2025-10-20&to=2026-10-19", nil)
	r.json(t, &s)
	if !s.ByMonth || s.Count != 4 || len(s.Breakdown) != 2 || s.Breakdown[0].Period != "2026-08" {
		t.Fatalf("year by month: %+v", s)
	}
	expect(t, call(t, vetA, "GET", "/api/stats/clinic/range?from=2026-10-19&to=2026-10-01", nil), http.StatusBadRequest)
	expect(t, call(t, vetA, "GET", "/api/stats/clinic/range?from=2026-10-01&to=2026-10-19&clinic="+clinicB, nil), http.StatusForbidden)
}

func TestStockRealizeAndReturn(t *testing.T) {
	reset(t)
	var p struct {
		ID        uint
		Quantity  int
		UnitPrice string
	}
	r := call(t, vetA, "POST", "/api/shop/products", map[string]interface{}{"name": "Drontal", "quantity": 7, "unitPrice": "12.50"})
	expect(t, r, http.StatusCreated)
	r.json(t, &p)
	if p.UnitPrice != "12.5" || p.Quantity != 7 {
		t.Fatalf("created %+v", p)
	}
	expect(t, call(t, vetA, "POST", "/api/shop/products", map[string]interface{}{"name": "x", "quantity": -1, "unitPrice": "1"}), http.StatusBadRequest)

	// Another clinic can neither see nor sell it.
	var list []struct{ ID uint }
	call(t, vetB, "GET", "/api/shop/products", nil).json(t, &list)
	if len(list) != 0 {
		t.Fatal("clinic B sees clinic A's stock")
	}
	expect(t, call(t, vetB, "POST", "/api/shop/realize", map[string]interface{}{"productId": p.ID, "quantity": 1, "date": "2026-10-20"}), http.StatusNotFound)

	var sale struct {
		ID        uint
		Name      string
		Price     string
		Comment   string
		ProductID *uint
		Quantity  int
	}
	r = call(t, vetA, "POST", "/api/shop/realize", map[string]interface{}{"productId": p.ID, "quantity": 3, "date": "2026-10-20", "method": "card"})
	expect(t, r, http.StatusCreated)
	r.json(t, &sale)
	if sale.Name != "Drontal" || sale.Price != "37.5" || sale.Comment != "3 ცალი" || sale.ProductID == nil || *sale.ProductID != p.ID {
		t.Fatalf("sale %+v", sale)
	}
	var got models.ShopProduct
	must(t, db.First(&got, p.ID).Error)
	if got.Quantity != 4 {
		t.Fatalf("stock after sale = %d, want 4", got.Quantity)
	}
	// More than in stock: refused, nothing changes.
	expect(t, call(t, vetA, "POST", "/api/shop/realize", map[string]interface{}{"productId": p.ID, "quantity": 5, "date": "2026-10-20"}), http.StatusConflict)
	var sales int64
	db.Model(&models.Shop{}).Count(&sales)
	if sales != 1 {
		t.Fatalf("%d sales after a refused one", sales)
	}

	// The sales list says it came from stock; deleting it returns the units.
	var page struct {
		Data []struct {
			ID        uint
			ProductID *uint
			Quantity  int
		}
	}
	call(t, vetA, "GET", "/api/shop?date_from=2026-10-20&date_to=2026-10-20", nil).json(t, &page)
	if len(page.Data) != 1 || page.Data[0].ProductID == nil || page.Data[0].Quantity != 3 {
		t.Fatalf("sales list %+v", page)
	}
	expect(t, call(t, vetA, "DELETE", fmt.Sprintf("/api/shop/%d", sale.ID), nil), http.StatusOK)
	must(t, db.First(&got, p.ID).Error)
	if got.Quantity != 7 {
		t.Fatalf("stock after deleting the sale = %d, want 7", got.Quantity)
	}
	expect(t, call(t, vetB, "DELETE", fmt.Sprintf("/api/shop/products/%d", p.ID), nil), http.StatusNotFound)
	expect(t, call(t, vetA, "DELETE", fmt.Sprintf("/api/shop/products/%d", p.ID), nil), http.StatusOK)
}

func TestStaffCreateRequiresPhone(t *testing.T) {
	reset(t)
	body := map[string]string{"first_name": "ნინო", "last_name": "01001001002", "email": "nino-vet@test.ge", "password": "secret1"}
	expect(t, call(t, vetA, "POST", "/api/staff", body), http.StatusBadRequest)
	body["phone"] = "599123456"
	expect(t, call(t, vetA, "POST", "/api/staff", body), http.StatusCreated)
}
