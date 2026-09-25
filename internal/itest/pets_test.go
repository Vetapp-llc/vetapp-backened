package itest

import (
	"fmt"
	"net/http"
	"testing"

	"vetapp-backend/internal/models"
)

func TestPetLookupCrossClinicByExactKeyOnly(t *testing.T) {
	reset(t)
	must(t, db.Model(&models.Pet{}).Where("id = ?", petB).Update("chip", "268000000099999").Error)

	// Browsing: own clinic only.
	r := call(t, vetA, "GET", "/api/pets", nil)
	expect(t, r, http.StatusOK)
	var page struct{ Total int64 }
	r.json(t, &page)
	if page.Total != 1 {
		t.Errorf("browse total = %d, want 1 (own clinic)", page.Total)
	}
	r = call(t, vetA, "GET", "/api/pets?search=კატა", nil)
	r.json(t, &page)
	if page.Total != 0 {
		t.Errorf("free-text search leaked another clinic's pet")
	}

	// Exact owner ID / chip: every clinic, like vet/search2.php and search3.php.
	r = call(t, vetA, "GET", "/api/pets?owner_id="+ownerID, nil)
	r.json(t, &page)
	if page.Total != 2 {
		t.Errorf("owner lookup total = %d, want 2", page.Total)
	}
	r = call(t, vetA, "GET", "/api/pets?chip=268000000099999", nil)
	r.json(t, &page)
	if page.Total != 1 {
		t.Errorf("chip lookup total = %d, want 1", page.Total)
	}
}

func TestPetAccessFollowsTreatment(t *testing.T) {
	reset(t)
	path := fmt.Sprintf("/api/pets/%d", petB)
	expect(t, call(t, vetA, "GET", path, nil), http.StatusNotFound)
	expect(t, call(t, vetA, "GET", path+"/history", nil), http.StatusNotFound)
	expect(t, call(t, vetA, "GET", path+"/certificate", nil), http.StatusNotFound)
	expect(t, call(t, vetA, "PUT", path, map[string]string{"name": "x"}), http.StatusNotFound)

	// Clinic B's record for the pet exists; after clinic A treats it,
	// A can open it but sees only its own records.
	insertProc(t, models.Procedure{UUID: fmt.Sprint(petB), TP: 1, SK: clinicB, Phone: "1", Date: "2026-01-01"})
	expect(t, call(t, vetA, "POST", "/api/procedures", map[string]interface{}{"uuid": fmt.Sprint(petB), "tp": 108, "owner": ownerID}), http.StatusCreated)

	r := call(t, vetA, "GET", path, nil)
	expect(t, r, http.StatusOK)
	var detail struct {
		MedicalRecords []struct{ ProcedureType string }
	}
	r.json(t, &detail)
	if len(detail.MedicalRecords) != 1 || detail.MedicalRecords[0].ProcedureType != "108" {
		t.Errorf("records = %+v, want only clinic A's consultation", detail.MedicalRecords)
	}
	r = call(t, vetA, "GET", path+"/history", nil)
	var hist []struct{}
	r.json(t, &hist)
	if len(hist) != 1 {
		t.Errorf("history has %d records, want 1", len(hist))
	}
	r = call(t, admin, "GET", path+"/history", nil)
	r.json(t, &hist)
	if len(hist) != 2 {
		t.Errorf("admin history has %d records, want 2", len(hist))
	}
}

func TestPetCreateStartsUnregistered(t *testing.T) {
	reset(t)
	r := call(t, vetA, "POST", "/api/pets", map[string]interface{}{
		"uuid": ownerID, "name": "ბობი", "pet": "dog", "sex": "female", "status": 1, "code": "0000",
	})
	expect(t, r, http.StatusCreated)
	var created struct{ ID string }
	r.json(t, &created)
	var pet models.Pet
	must(t, db.Where("id = ?", created.ID).First(&pet).Error)
	if pet.Status != 2 || pet.Code != "1313" || pet.Vet != clinicA {
		t.Errorf("created pet status/code/vet = %d/%q/%q", pet.Status, pet.Code, pet.Vet)
	}
	// Stored as PHP stores them: ძუ is female.
	if pet.Pet != "ძაღლი" || pet.Sex != "ძუ" {
		t.Errorf("species/sex = %q/%q, want ძაღლი/ძუ", pet.Pet, pet.Sex)
	}
}

func TestPetUpdateAllowlist(t *testing.T) {
	reset(t)
	r := call(t, vetA, "PUT", fmt.Sprintf("/api/pets/%d", petA), map[string]interface{}{
		"name": "იოში 2", "chip": "268000000011111", "chipd": "2026-09-01",
		"status": 5, "birth2": "2099-01-01", "vet": clinicB, "code": "0000",
	})
	expect(t, r, http.StatusOK)
	var pet models.Pet
	must(t, db.First(&pet, petA).Error)
	if pet.Name != "იოში 2" || pet.Chip != "268000000011111" || pet.ChipDate != "2026-09-01" {
		t.Errorf("editable fields not applied: %+v", pet)
	}
	if pet.Status != 1 || pet.Birth2 != "" || pet.Vet != clinicA || pet.Code != "" {
		t.Errorf("protected fields changed: status %d birth2 %q vet %q code %q", pet.Status, pet.Birth2, pet.Vet, pet.Code)
	}
	expect(t, call(t, vetA, "PUT", fmt.Sprintf("/api/pets/%d", petA), map[string]string{"chipd": "yesterday"}), http.StatusBadRequest)
	expect(t, call(t, vetA, "DELETE", fmt.Sprintf("/api/pets/%d", petA), nil), http.StatusForbidden)
}

func TestCertificatePicksLatestOfEachKind(t *testing.T) {
	reset(t)
	pid := fmt.Sprint(petA)
	insertProc(t, models.Procedure{UUID: pid, TP: 1, SK: clinicB, Vac: "ცოფის საწინააღმდეგო ვაქცინა", VacN: "Rabisin", Ser: "OLD", Date: "2025-01-01"})
	insertProc(t, models.Procedure{UUID: pid, TP: 1, SK: clinicB, Vac: "ცოფის საწინააღმდეგო ვაქცინა", VacN: "Nobivac Rabies", Ser: "NEW", Date: "2026-01-01"})
	insertProc(t, models.Procedure{UUID: pid, TP: 1, SK: clinicA, Vac: "კომპლექსური ვაქცინა", VacN: "Eurican DHPPi2-L", Date: "2026-02-01"})
	insertProc(t, models.Procedure{UUID: pid, TP: 12, SK: clinicA, Deh: "Drontal", Date: "2026-03-01"})
	insertProc(t, models.Procedure{UUID: pid, TP: 101, SK: clinicA, Vac: "სტომატოლოგია", Date: "2026-04-01"}) // dentistry: not on a certificate

	r := call(t, vetA, "GET", fmt.Sprintf("/api/pets/%d/certificate", petA), nil)
	expect(t, r, http.StatusOK)
	var c struct {
		Rabies, Complex, Dehelminization, Ectoparasite *models.Procedure
		Owner                                          struct {
			PersonalID string `json:"personal_id"`
		}
	}
	r.json(t, &c)
	if c.Rabies == nil || c.Rabies.Ser != "NEW" {
		t.Errorf("rabies = %+v", c.Rabies)
	}
	if c.Complex == nil || c.Complex.VacN != "Eurican DHPPi2-L" {
		t.Errorf("complex = %+v", c.Complex)
	}
	if c.Dehelminization == nil || c.Dehelminization.Deh != "Drontal" {
		t.Errorf("dehel = %+v", c.Dehelminization)
	}
	if c.Ectoparasite != nil {
		t.Errorf("ecto should be empty, got %+v", c.Ectoparasite)
	}
	if c.Owner.PersonalID != ownerID {
		t.Errorf("owner = %+v", c.Owner)
	}
}

// Legacy breeds outside the list must not block editing other fields.
func TestPetUpdateKeepsLegacyBreed(t *testing.T) {
	reset(t)
	must(t, db.Model(&models.Pet{}).Where("id = ?", petA).Update("variety", "მეტისი ხ").Error)
	expect(t, call(t, vetA, "PUT", fmt.Sprintf("/api/pets/%d", petA), map[string]string{
		"color": "შავი", "pet": "ძაღლი", "variety": "მეტისი ხ",
	}), http.StatusOK)
	// Changing to an unknown breed is still refused.
	expect(t, call(t, vetA, "PUT", fmt.Sprintf("/api/pets/%d", petA), map[string]string{"variety": "not-a-breed"}), http.StatusBadRequest)
}

// Legacy owner IDs and chips carry stray spaces; lookups must still find them.
func TestPetLookupIgnoresLegacyWhitespace(t *testing.T) {
	reset(t)
	must(t, db.Model(&models.Pet{}).Where("id = ?", petB).Updates(map[string]interface{}{"uuid": " 99988877766", "chip": "268000000055555 "}).Error)
	for _, q := range []string{"owner_id=99988877766", "chip=268000000055555"} {
		r := call(t, vetA, "GET", "/api/pets?"+q, nil)
		expect(t, r, http.StatusOK)
		var page struct{ Total int64 }
		r.json(t, &page)
		if page.Total != 1 {
			t.Errorf("%s: total = %d, want 1", q, page.Total)
		}
	}
}
