package itest

import (
	"fmt"
	"net/http"
	"testing"

	"vetapp-backend/internal/models"
)

func TestHomeProcedures(t *testing.T) {
	reset(t)
	must(t, db.Create(&models.User{ID: 9010, FirstName: "სხვა", LastName: "99999999999", Email: "o2@test.ge", GroupID: models.RoleOwner, Status: "T"}).Error)

	base := fmt.Sprintf("/api/owner/pets/%d/home-procedures", petA)
	r := call(t, owner, "POST", base, map[string]interface{}{"name": "წამალი ორჯერ დღეში", "start_date": "2026-09-20", "days": 5})
	expect(t, r, http.StatusCreated)
	var hp struct {
		ID        uint
		Days      int
		EndDate   string `json:"end_date"`
		StartDate string `json:"start_date"`
	}
	r.json(t, &hp)
	if hp.Days != 5 || hp.EndDate != "2026-09-24" {
		t.Errorf("created = %+v", hp)
	}
	var row models.HomeProcedure
	must(t, db.First(&row, hp.ID).Error)
	if row.PeriodCode != "4" || row.OwnerID != ownerID || row.PetID != fmt.Sprint(petA) {
		t.Errorf("stored in legacy shape? %+v", row)
	}

	expect(t, call(t, owner, "POST", base, map[string]interface{}{"name": "x", "days": 28}), http.StatusBadRequest)
	expect(t, call(t, owner, "POST", base, map[string]interface{}{"name": " ", "days": 3}), http.StatusBadRequest)

	expect(t, call(t, owner, "POST", fmt.Sprintf("%s/%d/done", base, hp.ID), nil), http.StatusCreated)
	r = call(t, owner, "GET", base, nil)
	expect(t, r, http.StatusOK)
	var list []struct{ Done []string }
	r.json(t, &list)
	if len(list) != 1 || len(list[0].Done) != 1 {
		t.Errorf("list = %+v", list)
	}

	// Another owner cannot see or touch it.
	expect(t, call(t, 9010, "GET", base, nil), http.StatusNotFound)
	expect(t, call(t, 9010, "POST", fmt.Sprintf("%s/%d/done", base, hp.ID), nil), http.StatusNotFound)
	expect(t, call(t, 9010, "DELETE", fmt.Sprintf("%s/%d", base, hp.ID), nil), http.StatusNotFound)
	// Vets are not owners.
	expect(t, call(t, vetA, "GET", base, nil), http.StatusForbidden)

	expect(t, call(t, owner, "DELETE", fmt.Sprintf("%s/%d", base, hp.ID), nil), http.StatusOK)
	var n int64
	db.Model(&models.HomeProcedureDone{}).Count(&n)
	if n != 0 {
		t.Errorf("done marks left behind: %d", n)
	}
}

func TestOwnerPetUpdateAllowlist(t *testing.T) {
	reset(t)
	path := fmt.Sprintf("/api/owner/pets/%d", petA)
	expect(t, call(t, owner, "PUT", path, map[string]interface{}{
		"name": "იოშიკო", "chip": "268000000077777", "status": 1, "birth2": "2099-01-01", "cast": "x", "uuid": "0", "vet": clinicB,
	}), http.StatusOK)
	var pet models.Pet
	must(t, db.First(&pet, petA).Error)
	if pet.Name != "იოშიკო" || pet.Chip != "268000000077777" {
		t.Errorf("allowed fields not applied: %+v", pet)
	}
	if pet.Birth2 != "" || pet.Cast != "" || pet.UUID != ownerID || pet.Vet != clinicA {
		t.Errorf("protected fields changed: %+v", pet)
	}
	expect(t, call(t, owner, "PUT", path, map[string]interface{}{"date": "tomorrow"}), http.StatusBadRequest)
	expect(t, call(t, owner, "PUT", fmt.Sprintf("/api/owner/pets/%d", petB), map[string]string{"name": "x"}), http.StatusOK)
}
