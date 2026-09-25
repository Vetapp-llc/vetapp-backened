package itest

import (
	"fmt"
	"net/http"
	"testing"

	"vetapp-backend/internal/models"
)

// Codex review #1/#2: public sign-up.
func TestRegisterIsOwnerOnlyAndPersonalIDUnique(t *testing.T) {
	reset(t)
	r := call(t, 0, "POST", "/api/auth/register", map[string]interface{}{
		"first_name": "x", "last_name": "55555555555", "email": "evil@test.ge", "password": "pw", "group_id": 4, "zip": clinicA,
	})
	expect(t, r, http.StatusCreated)
	var u models.User
	must(t, db.Where("email = ?", "evil@test.ge").First(&u).Error)
	if u.GroupID != models.RoleOwner || u.Zip != "" {
		t.Fatalf("registered with group %d zip %q, want owner with no clinic", u.GroupID, u.Zip)
	}
	// The personal ID of an existing owner cannot be registered again.
	expect(t, call(t, 0, "POST", "/api/auth/register", map[string]interface{}{
		"first_name": "x", "last_name": " " + ownerID, "email": "thief@test.ge", "password": "pw",
	}), http.StatusConflict)
}

// Codex review #4: a disabled account's token stops working at once.
func TestDisabledAccountTokenRejected(t *testing.T) {
	reset(t)
	tok := token(t, vetA2)
	req := func() int {
		r, _ := http.NewRequest("GET", srv.URL+"/api/staff", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(r)
		must(t, err)
		res.Body.Close()
		return res.StatusCode
	}
	if c := req(); c != http.StatusOK {
		t.Fatalf("before removal: %d", c)
	}
	expect(t, call(t, vetA, "DELETE", fmt.Sprintf("/api/staff/%d", vetA2), nil), http.StatusOK)
	if c := req(); c != http.StatusUnauthorized {
		t.Fatalf("removed vet's token: status %d, want 401", c)
	}
}

// Codex review #3: appointments on a guessed pet id.
func TestAppointmentOnForeignPetNeedsProof(t *testing.T) {
	reset(t)
	body := map[string]interface{}{"uuid": fmt.Sprint(petB), "date": "2026-12-01", "tpname": "x"}
	expect(t, call(t, vetA, "POST", "/api/appointments", body), http.StatusNotFound)
	body["owner"] = ownerID
	expect(t, call(t, vetA, "POST", "/api/appointments", body), http.StatusCreated)
}

// Exact owner lookup lists the owner's pets at every clinic.
func TestOwnerLookupAcrossClinics(t *testing.T) {
	reset(t)
	r := call(t, vetA, "GET", "/api/owners/"+ownerID, nil)
	expect(t, r, http.StatusOK)
	var o struct{ Pets []struct{ ID string } }
	r.json(t, &o)
	if len(o.Pets) != 2 {
		t.Fatalf("pets = %d, want 2 (both clinics)", len(o.Pets))
	}
	// …and the pet modal can open the foreign pet with that proof.
	expect(t, call(t, vetA, "GET", fmt.Sprintf("/api/pets/%d?owner_id=%s", petB, ownerID), nil), http.StatusOK)
	expect(t, call(t, vetA, "GET", fmt.Sprintf("/api/pets/%d?owner_id=%s", petB, "11111111111"), nil), http.StatusNotFound)
}

// Codex review #5: the certificate carries no internal fields.
func TestCertificateHasNoInternalFields(t *testing.T) {
	reset(t)
	insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 1, SK: clinicB, Vac: "კომპლექსური ვაქცინა", VacN: "X", Koment: "SECRET NOTE", Price: "999", Phone: "1", Date: "2026-01-01"})
	r := call(t, vetA, "GET", fmt.Sprintf("/api/pets/%d/certificate", petA), nil)
	expect(t, r, http.StatusOK)
	// (the owner's phone is printed on the certificate; the record's
	// paid flag, also called phone, must not appear inside a treatment)
	for _, leak := range []string{"SECRET NOTE", "999", `"koment"`, `"price"`, `"company"`, `"sk"`} {
		if contains(string(r.Body), leak) {
			t.Errorf("certificate leaks %s: %s", leak, r.Body)
		}
	}
}

// Codex review #6: paying with nothing unpaid records nothing.
func TestPaymentWithNothingToPayRefused(t *testing.T) {
	reset(t)
	insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 1, SK: clinicA, Phone: "0", Price: "10", Date: "2026-09-24"})
	body := map[string]interface{}{"uuid": fmt.Sprint(petA), "date": "2026-09-24", "method": "cash", "amount": "10"}
	expect(t, call(t, vetA, "POST", "/api/payments/record", body), http.StatusCreated)
	expect(t, call(t, vetA, "POST", "/api/payments/record", body), http.StatusConflict)
	var n int64
	db.Model(&models.Payment{}).Count(&n)
	if n != 1 {
		t.Fatalf("%d payments, want 1", n)
	}
}

// Codex review #8: a retried request with the same key is not repeated.
func TestIdempotentProcedureAndPayment(t *testing.T) {
	reset(t)
	post := func(path, key string, body interface{}) resp {
		return callWithHeader(t, vetA, "POST", path, body, "Idempotency-Key", key)
	}
	var a, b models.Procedure
	r := post("/api/procedures", "proc-1", map[string]interface{}{"uuid": fmt.Sprint(petA), "tp": 108, "price": "20"})
	expect(t, r, http.StatusCreated)
	r.json(t, &a)
	r = post("/api/procedures", "proc-1", map[string]interface{}{"uuid": fmt.Sprint(petA), "tp": 108, "price": "20"})
	expect(t, r, http.StatusCreated)
	r.json(t, &b)
	if a.ID != b.ID {
		t.Fatalf("retry created a second procedure: %d vs %d", a.ID, b.ID)
	}
	pay := map[string]interface{}{"uuid": fmt.Sprint(petA), "method": "card", "amount": "20", "procedure_ids": []uint{a.ID}}
	var p1, p2 struct{ ID uint }
	r = post("/api/payments/record", "pay-1", pay)
	expect(t, r, http.StatusCreated)
	r.json(t, &p1)
	r = post("/api/payments/record", "pay-1", pay) // lost response, retried
	expect(t, r, http.StatusCreated)
	r.json(t, &p2)
	if p1.ID != p2.ID {
		t.Fatalf("retry charged twice: %d vs %d", p1.ID, p2.ID)
	}
	// Same key on a different route is refused.
	expect(t, post("/api/procedures", "pay-1", map[string]interface{}{"uuid": fmt.Sprint(petA), "tp": 108}), http.StatusConflict)
}

// Codex review #7: a paid record's price stays put.
func TestPriceChangeRefusedOnceSettled(t *testing.T) {
	reset(t)
	p := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 1, SK: clinicA, Phone: "0", Price: "10", Date: "2026-09-24"})
	// Payment lands after the handler read the row but before it writes:
	// simulated by paying first, then editing with the old unpaid view.
	must(t, db.Model(&models.Procedure{}).Where("id = ?", p.ID).Update("phone", "1").Error)
	expect(t, call(t, vetA, "PUT", fmt.Sprintf("/api/procedures/%d", p.ID), map[string]string{"price": "1"}), http.StatusConflict)
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// Re-review #2: a disabled owner's ID still owns pets; simultaneous
// sign-ups with one ID cannot both succeed.
func TestRegisterBlocksDisabledOwnerIDAndRaces(t *testing.T) {
	reset(t)
	must(t, db.Model(&models.User{}).Where("id = ?", owner).Update("status", "F").Error)
	expect(t, call(t, 0, "POST", "/api/auth/register", map[string]interface{}{
		"first_name": "x", "last_name": ownerID, "email": "again@test.ge", "password": "pw",
	}), http.StatusConflict)

	i := 0
	codes := raceN(t, 10, func() resp {
		i++
		return call(t, 0, "POST", "/api/auth/register", map[string]interface{}{
			"first_name": "x", "last_name": "77777777777", "email": fmt.Sprintf("race%d-%p@test.ge", i, &i), "password": "pw",
		})
	})
	if codes[http.StatusCreated] != 1 {
		t.Fatalf("status counts = %v, want exactly one 201", codes)
	}
}

// Re-review new bugs: a key reused with another body, and a replay of a
// procedure deleted since.
func TestIdempotencyMismatchAndDeletedReplay(t *testing.T) {
	reset(t)
	post := func(path, key string, body interface{}) resp {
		return callWithHeader(t, vetA, "POST", path, body, "Idempotency-Key", key)
	}
	var p models.Procedure
	r := post("/api/procedures", "k1", map[string]interface{}{"uuid": fmt.Sprint(petA), "tp": 108, "price": "20"})
	expect(t, r, http.StatusCreated)
	r.json(t, &p)
	// Same key, different body: refused, not replayed.
	expect(t, post("/api/procedures", "k1", map[string]interface{}{"uuid": fmt.Sprint(petA), "tp": 108, "price": "99"}), http.StatusConflict)

	pay := func(amount string) resp {
		return post("/api/payments/record", "pay-k", map[string]interface{}{"uuid": fmt.Sprint(petA), "method": "cash", "amount": amount, "procedure_ids": []uint{p.ID}})
	}
	expect(t, pay("20"), http.StatusCreated)
	expect(t, pay("18"), http.StatusConflict)

	// Replay after the record was deleted: an honest conflict, not a fake 201.
	r = post("/api/procedures", "k2", map[string]interface{}{"uuid": fmt.Sprint(petA), "tp": 108})
	expect(t, r, http.StatusCreated)
	var p2 models.Procedure
	r.json(t, &p2)
	expect(t, call(t, vetA, "DELETE", fmt.Sprintf("/api/procedures/%d", p2.ID), nil), http.StatusOK)
	expect(t, post("/api/procedures", "k2", map[string]interface{}{"uuid": fmt.Sprint(petA), "tp": 108}), http.StatusConflict)
}

// Re-review #7: on a paid record, other fields stay editable when the
// submitted price equals the stored one; a different price is refused.
func TestPaidRecordPriceGuardIsAtomic(t *testing.T) {
	reset(t)
	p := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 1, SK: clinicA, Phone: "1", Price: "10", Date: "2026-09-24"})
	path := fmt.Sprintf("/api/procedures/%d", p.ID)
	expect(t, call(t, vetA, "PUT", path, map[string]string{"price": "10", "coment": "ok"}), http.StatusOK)
	expect(t, call(t, vetA, "PUT", path, map[string]string{"price": "11"}), http.StatusConflict)
}

// UI report #2/#6: the dashboard owner search (GET /owners?search=) must
// find an owner by exact personal ID across clinics and count every pet;
// a fragment still only browses the caller's clinic.
func TestOwnerSearchExactIDCrossClinic(t *testing.T) {
	reset(t)
	must(t, db.Model(&models.Pet{}).Where("id = ?", petA).Update("vet", clinicB).Error) // both pets now at clinic B
	r := call(t, vetA, "GET", "/api/owners?search="+ownerID, nil)
	expect(t, r, http.StatusOK)
	var page struct {
		Total int64
		Data  []struct {
			PersonalID string `json:"personalId"`
			PetCount   int    `json:"petCount"`
		}
	}
	r.json(t, &page)
	if page.Total != 1 || len(page.Data) != 1 || page.Data[0].PetCount != 2 {
		t.Fatalf("exact search = %+v, want the owner with 2 pets", page)
	}
	// A fragment must not reach clinic B's owners.
	r = call(t, vetA, "GET", "/api/owners?search="+ownerID[:6], nil)
	r.json(t, &page)
	if page.Total != 0 {
		t.Fatalf("fragment search leaked another clinic's owner: %+v", page)
	}
}
