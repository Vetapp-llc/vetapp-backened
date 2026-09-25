package itest

import (
	"fmt"
	"net/http"
	"testing"

	"vetapp-backend/internal/models"
)

func TestProcedureCreateDerivesFieldsAndStartsUnpaid(t *testing.T) {
	reset(t)
	r := call(t, vetA, "POST", "/api/procedures", map[string]interface{}{
		"uuid": fmt.Sprint(petA), "tp": 1, "date": "2026-09-24", "date2": "2027-09-24",
		"vac": "კომპლექსური ვაქცინა", "vacn": "Nobivac DHPPi+L", "price": "40",
		"phone": "1", "pname": "forged", "owner": "00000000000", "ownern": "forged",
	})
	expect(t, r, http.StatusCreated)
	var p models.Procedure
	r.json(t, &p)
	if p.Phone != "0" {
		t.Errorf("new procedure paid flag = %q, want 0", p.Phone)
	}
	if p.PName != "იოში" || p.Owner != ownerID || p.OwnerN != "დავით აბაიაძე" || p.PetSpecies != "ძაღლი" {
		t.Errorf("denormalised fields not taken from pet: %+v", p)
	}
	if p.SK != clinicA || p.VetName != fmt.Sprint(vetA) {
		t.Errorf("sk/vetname = %q/%q", p.SK, p.VetName)
	}
	if p.Date3 != "2027,08,24" {
		t.Errorf("date3 = %q, want 2027,08,24", p.Date3)
	}
	if p.TPName != "ვაქცინაცია" {
		t.Errorf("tpname = %q", p.TPName)
	}
}

func TestProcedureCreateRejectsBadInput(t *testing.T) {
	reset(t)
	cases := []map[string]interface{}{
		{"uuid": fmt.Sprint(petA), "tp": 4},                       // orphan tp
		{"uuid": fmt.Sprint(petA), "tp": 1, "date": "24.09.2026"}, // bad date
		{"uuid": fmt.Sprint(petA), "tp": 1, "date2": "soon"},
		{"uuid": fmt.Sprint(petA), "tp": 1, "vetname": fmt.Sprint(vetB)}, // vet of another clinic
		{"uuid": fmt.Sprint(petA), "tp": 115},                            // chip number missing
	}
	for i, body := range cases {
		if r := call(t, vetA, "POST", "/api/procedures", body); r.Code != http.StatusBadRequest {
			t.Errorf("case %d: status %d, want 400; %s", i, r.Code, r.Body)
		}
	}
	expect(t, call(t, vetA, "POST", "/api/procedures", map[string]interface{}{"uuid": "999999", "tp": 1}), http.StatusNotFound)
	// Admins have no clinic to record under.
	expect(t, call(t, admin, "POST", "/api/procedures", map[string]interface{}{"uuid": fmt.Sprint(petA), "tp": 1}), http.StatusBadRequest)
}

func TestProcedureCreateAttributesChosenVet(t *testing.T) {
	reset(t)
	r := call(t, vetA, "POST", "/api/procedures", map[string]interface{}{"uuid": fmt.Sprint(petA), "tp": 108, "vetname": fmt.Sprint(vetA2)})
	expect(t, r, http.StatusCreated)
	var p models.Procedure
	r.json(t, &p)
	if p.VetName != fmt.Sprint(vetA2) {
		t.Errorf("vetname = %q, want %d", p.VetName, vetA2)
	}
}

// Any clinic may treat a pet registered elsewhere (PHP parity) once it
// proves the exact lookup by naming the owner's personal ID; a bare,
// guessable pet id is refused. The record belongs to the clinic that
// wrote it, and that clinic can then open the pet.
func TestProcedureOnForeignPet(t *testing.T) {
	reset(t)
	expect(t, call(t, vetA, "POST", "/api/procedures", map[string]interface{}{"uuid": fmt.Sprint(petB), "tp": 108}), http.StatusNotFound)
	expect(t, call(t, vetA, "POST", "/api/procedures", map[string]interface{}{"uuid": fmt.Sprint(petB), "tp": 115, "chip": "1", "owner": "00000000000"}), http.StatusNotFound)
	var pet models.Pet
	must(t, db.First(&pet, petB).Error)
	if pet.Chip != "" {
		t.Fatalf("chip overwritten without access: %q", pet.Chip)
	}

	r := call(t, vetA, "POST", "/api/procedures", map[string]interface{}{"uuid": fmt.Sprint(petB), "tp": 108, "owner": ownerID})
	expect(t, r, http.StatusCreated)
	var p models.Procedure
	r.json(t, &p)
	if p.SK != clinicA {
		t.Errorf("sk = %q, want %s", p.SK, clinicA)
	}
	expect(t, call(t, vetA, "GET", fmt.Sprintf("/api/pets/%d", petB), nil), http.StatusOK)
}

func TestMicrochipAndSterilisationUpdatePet(t *testing.T) {
	reset(t)
	expect(t, call(t, vetA, "POST", "/api/procedures", map[string]interface{}{
		"uuid": fmt.Sprint(petA), "tp": 115, "date": "2026-09-20", "chip": "268000000012345",
	}), http.StatusCreated)
	expect(t, call(t, vetA, "POST", "/api/procedures", map[string]interface{}{
		"uuid": fmt.Sprint(petA), "tp": 110, "date": "2026-09-21", "vac": "კასტრაცია",
	}), http.StatusCreated)
	var pet models.Pet
	must(t, db.First(&pet, petA).Error)
	if pet.Chip != "268000000012345" || pet.ChipDate != "2026-09-20" {
		t.Errorf("chip = %q / %q", pet.Chip, pet.ChipDate)
	}
	if pet.Cast != "კასტრაცია" || pet.CastDate != "2026-09-21" {
		t.Errorf("cast = %q / %q", pet.Cast, pet.CastDate)
	}
	var chipProc models.Procedure
	must(t, db.Where("tp = ?", "115").First(&chipProc).Error)
	if chipProc.Coment != "268000000012345" {
		t.Errorf("chip not kept in coment: %q", chipProc.Coment)
	}
}

func TestProcedureByIDIsClinicScoped(t *testing.T) {
	reset(t)
	other := insertProc(t, models.Procedure{UUID: fmt.Sprint(petB), TP: 1, SK: clinicB, Phone: "0", Price: "10", Date: "2026-09-24"})
	path := fmt.Sprintf("/api/procedures/%d", other.ID)
	expect(t, call(t, vetA, "GET", path, nil), http.StatusNotFound)
	expect(t, call(t, vetA, "PUT", path, map[string]string{"coment": "x"}), http.StatusNotFound)
	expect(t, call(t, vetA, "DELETE", path, nil), http.StatusNotFound)
	expect(t, call(t, vetB, "GET", path, nil), http.StatusOK)
	expect(t, call(t, admin, "GET", path, nil), http.StatusOK)
}

func TestProcedureListIgnoresClinicOverrideForVets(t *testing.T) {
	reset(t)
	insertProc(t, models.Procedure{UUID: fmt.Sprint(petB), TP: 1, SK: clinicB, Phone: "0", Date: "2026-09-24"})
	expect(t, call(t, vetA, "GET", "/api/procedures?clinic="+clinicB, nil), http.StatusForbidden)
	r := call(t, admin, "GET", "/api/procedures?clinic="+clinicB, nil)
	expect(t, r, http.StatusOK)
	var page struct{ Total int64 }
	r.json(t, &page)
	if page.Total != 1 {
		t.Errorf("admin override total = %d, want 1", page.Total)
	}
}

func TestProcedureUpdateOnlyEditableFields(t *testing.T) {
	reset(t)
	p := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 1, SK: clinicA, Phone: "0", Price: "10", Date: "2026-09-24", VetName: fmt.Sprint(vetA)})
	r := call(t, vetA, "PUT", fmt.Sprintf("/api/procedures/%d", p.ID), map[string]interface{}{
		"coment": "updated", "date2": "2027-01-10",
		"phone": "1", "uuid": fmt.Sprint(petB), "sk": clinicB, "tp": 12, "owner": "x",
	})
	expect(t, r, http.StatusOK)
	var got models.Procedure
	must(t, db.First(&got, p.ID).Error)
	if got.Coment != "updated" || got.Date2 != "2027-01-10" || got.Date3 != "2026,12,10" {
		t.Errorf("editable fields not applied: %+v", got)
	}
	if got.Phone != "0" || got.UUID != fmt.Sprint(petA) || got.SK != clinicA || got.TP != 1 || got.Owner != "" {
		t.Errorf("protected fields changed: %+v", got)
	}
	if got.Name == "" {
		t.Error("edit log (name) not stamped")
	}
	expect(t, call(t, vetA, "PUT", fmt.Sprintf("/api/procedures/%d", p.ID), map[string]string{"vetname": fmt.Sprint(vetB)}), http.StatusBadRequest)
	expect(t, call(t, vetA, "PUT", fmt.Sprintf("/api/procedures/%d", p.ID), map[string]interface{}{"price": 5}), http.StatusBadRequest)
}

func TestPaidProcedureIsLocked(t *testing.T) {
	reset(t)
	p := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 1, SK: clinicA, Phone: "1", Price: "10", Date: "2026-09-24"})
	path := fmt.Sprintf("/api/procedures/%d", p.ID)
	expect(t, call(t, vetA, "DELETE", path, nil), http.StatusConflict)
	expect(t, call(t, vetA, "PUT", path, map[string]string{"price": "1"}), http.StatusConflict)
	expect(t, call(t, vetA, "PUT", path, map[string]string{"coment": "note"}), http.StatusOK)

	unpaid := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 1, SK: clinicA, Phone: "0", Date: "2026-09-24"})
	expect(t, call(t, vetA, "DELETE", fmt.Sprintf("/api/procedures/%d", unpaid.ID), nil), http.StatusOK)
}
