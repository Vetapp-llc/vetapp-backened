package itest

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"vetapp-backend/internal/models"
)

func TestAdminMembersAndDisable(t *testing.T) {
	reset(t)
	expect(t, call(t, vetA, "GET", "/api/admin/members", nil), http.StatusForbidden)

	r := call(t, admin, "GET", "/api/admin/members?group=1", nil)
	expect(t, r, http.StatusOK)
	var page struct {
		Total int64
		Data  []struct {
			ID         uint
			PersonalID string `json:"personal_id"`
			PetCount   int64  `json:"pet_count"`
		}
	}
	r.json(t, &page)
	if page.Total != 1 || page.Data[0].PersonalID != ownerID || page.Data[0].PetCount != 2 {
		t.Fatalf("owners = %+v", page)
	}
	r = call(t, admin, "GET", "/api/admin/members?group=2&search="+url.QueryEscape("ვეტი ა"), nil)
	r.json(t, &page)
	if page.Total != 2 {
		t.Errorf("vet search total = %d, want 2", page.Total)
	}

	expect(t, call(t, admin, "DELETE", fmt.Sprintf("/api/admin/members/%d", admin), nil), http.StatusBadRequest)
	expect(t, call(t, admin, "DELETE", fmt.Sprintf("/api/admin/members/%d", owner), nil), http.StatusOK)
	var u models.User
	must(t, db.First(&u, owner).Error)
	if u.Status != "F" || u.Email != "13131313owner@test" {
		t.Errorf("disabled owner = %+v", u)
	}
	// The pets are untouched.
	var n int64
	db.Model(&models.Pet{}).Where("uuid = ?", ownerID).Count(&n)
	if n != 2 {
		t.Errorf("owner's pets = %d, want 2", n)
	}
}

func TestAdminTransactions(t *testing.T) {
	reset(t)
	must(t, db.Exec(`INSERT INTO payments_ipay (pet_id, price, currency, status, provider, order_id, created_at)
		VALUES (?, 26.99, 'GEL', 'success', 'ipay', 'ord-1', now())`, petA).Error)
	r := call(t, admin, "GET", "/api/admin/transactions", nil)
	expect(t, r, http.StatusOK)
	var page struct {
		Total int64
		Data  []struct {
			PetName string `json:"pet_name"`
			Price   string
			OrderID string `json:"order_id"`
		}
	}
	r.json(t, &page)
	if page.Total != 1 || page.Data[0].PetName != "იოში" || page.Data[0].Price != "26.99" || page.Data[0].OrderID != "ord-1" {
		t.Errorf("transactions = %+v", page)
	}
	expect(t, call(t, vetA, "GET", "/api/admin/transactions", nil), http.StatusForbidden)
}

func TestPromoListsOwnClinicSignups(t *testing.T) {
	reset(t)
	// The promo code is the vet's own `website` value, not the clinic code.
	must(t, db.Model(&models.User{}).Where("id = ?", vetA).Update("website", "vetapp777").Error)
	must(t, db.Model(&models.User{}).Where("id = ?", owner).Update("website", "vetapp777").Error)
	r := call(t, vetA, "GET", "/api/promo", nil)
	expect(t, r, http.StatusOK)
	var page struct{ Total int64 }
	r.json(t, &page)
	if page.Total != 1 {
		t.Errorf("clinic A promo = %d, want 1", page.Total)
	}
	r = call(t, vetB, "GET", "/api/promo", nil)
	r.json(t, &page)
	if page.Total != 0 {
		t.Errorf("clinic B promo = %d, want 0", page.Total)
	}
	expect(t, call(t, vetB, "GET", "/api/promo?clinic="+clinicA, nil), http.StatusForbidden)
}
