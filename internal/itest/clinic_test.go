package itest

import (
	"fmt"
	"net/http"
	"testing"

	"vetapp-backend/internal/models"
)

// ---- payments ----

func TestPaymentMarksOnlyThisPetsUnpaidItems(t *testing.T) {
	reset(t)
	mine := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 1, SK: clinicA, Phone: "0", Price: "40", Date: "2026-09-24"})
	other := insertProc(t, models.Procedure{UUID: fmt.Sprint(petB), TP: 1, SK: clinicB, Phone: "0", Price: "10", Date: "2026-09-24"})

	// Another clinic's item in the list: the whole payment is refused.
	r := call(t, vetA, "POST", "/api/payments/record", map[string]interface{}{
		"uuid": fmt.Sprint(petA), "date": "2026-09-24", "method": "card", "amount": "50",
		"procedure_ids": []uint{mine.ID, other.ID},
	})
	expect(t, r, http.StatusConflict)
	var n int64
	db.Model(&models.Payment{}).Count(&n)
	if n != 0 {
		t.Fatalf("refused payment left %d paymethod rows", n)
	}

	expect(t, call(t, vetA, "POST", "/api/payments/record", map[string]interface{}{
		"uuid": fmt.Sprint(petA), "date": "2026-09-24", "method": "card", "amount": "40",
		"procedure_ids": []uint{mine.ID},
	}), http.StatusCreated)
	var got models.Procedure
	must(t, db.First(&got, mine.ID).Error)
	if got.Phone != "1" || got.Company != models.PayMethodCard {
		t.Errorf("paid item = phone %q company %q", got.Phone, got.Company)
	}
	var otherGot models.Procedure
	must(t, db.First(&otherGot, other.ID).Error)
	if otherGot.Phone != "0" {
		t.Error("another clinic's item was marked paid")
	}

	// Paying the same item again is a double charge.
	expect(t, call(t, vetA, "POST", "/api/payments/record", map[string]interface{}{
		"uuid": fmt.Sprint(petA), "method": "cash", "amount": "40", "procedure_ids": []uint{mine.ID},
	}), http.StatusConflict)
}

func TestPaymentWithoutIDsPaysThatDaysItems(t *testing.T) {
	reset(t)
	a := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 1, SK: clinicA, Phone: "0", Price: "40", Date: "2026-09-24"})
	b := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 12, SK: clinicA, Phone: "0", Price: "15", Date: "2026-09-23"})
	expect(t, call(t, vetA, "POST", "/api/payments/record", map[string]interface{}{
		"uuid": fmt.Sprint(petA), "date": "2026-09-24", "method": "cash", "amount": "40",
	}), http.StatusCreated)
	var got models.Procedure
	must(t, db.First(&got, a.ID).Error)
	if got.Phone != "1" || got.Company != models.PayMethodCash {
		t.Errorf("same-day item not paid: %+v", got)
	}
	var bGot models.Procedure
	must(t, db.First(&bGot, b.ID).Error)
	if bGot.Phone != "0" {
		t.Error("other day's item was paid")
	}
}

func TestPaymentValidation(t *testing.T) {
	reset(t)
	for _, amount := range []string{"", "abc", "-5", "40 ლარი"} {
		r := call(t, vetA, "POST", "/api/payments/record", map[string]interface{}{"uuid": fmt.Sprint(petA), "method": "card", "amount": amount})
		if r.Code != http.StatusBadRequest {
			t.Errorf("amount %q: %d", amount, r.Code)
		}
	}
	expect(t, call(t, vetA, "GET", "/api/payments/daily?date=2026-09-24&clinic="+clinicB, nil), http.StatusForbidden)
	expect(t, call(t, vetA, "GET", "/api/payments/history?clinic="+clinicB, nil), http.StatusForbidden)
}

// ---- shop ----

func TestShopCRUDScopedWithTotals(t *testing.T) {
	reset(t)
	r := call(t, vetA, "POST", "/api/shop", map[string]string{"name": "Royal Canin 2kg", "price": "55.50", "date": "2026-09-24", "method": "card", "comment": "2 ც"})
	expect(t, r, http.StatusCreated)
	var sale struct{ ID uint }
	r.json(t, &sale)
	expect(t, call(t, vetA, "POST", "/api/shop", map[string]string{"name": "Collar", "price": "20", "date": "2026-09-24", "method": "cash"}), http.StatusCreated)
	expect(t, call(t, vetB, "POST", "/api/shop", map[string]string{"name": "B sale", "price": "999", "date": "2026-09-24", "method": "cash"}), http.StatusCreated)
	expect(t, call(t, vetA, "POST", "/api/shop", map[string]string{"name": "Bad", "price": "20 ლარი", "date": "2026-09-24"}), http.StatusBadRequest)

	r = call(t, vetA, "GET", "/api/shop?date_from=2026-09-24&date_to=2026-09-24", nil)
	expect(t, r, http.StatusOK)
	var list struct {
		Total  int64
		Totals struct{ Card, Cash, Total string }
	}
	r.json(t, &list)
	if list.Total != 2 || list.Totals.Card != "55.50" || list.Totals.Cash != "20" || list.Totals.Total != "75.50" {
		t.Errorf("list = %+v", list)
	}

	path := fmt.Sprintf("/api/shop/%d", sale.ID)
	expect(t, call(t, vetB, "PUT", path, map[string]string{"price": "1"}), http.StatusNotFound)
	expect(t, call(t, vetB, "DELETE", path, nil), http.StatusNotFound)
	// Changing the method used to 500 (JSON name passed as a column name);
	// a `zip` key used to move the sale to another clinic.
	expect(t, call(t, vetA, "PUT", path, map[string]string{"method": "cash", "zip": clinicB, "sk": clinicB}), http.StatusOK)
	var got models.Shop
	must(t, db.First(&got, sale.ID).Error)
	if got.Method != models.PayMethodCash || got.SK != clinicA {
		t.Errorf("after update: %+v", got)
	}
	expect(t, call(t, vetA, "DELETE", path, nil), http.StatusOK)
}

// ---- prices ----

func TestPricesScopedAndFreeText(t *testing.T) {
	reset(t)
	r := call(t, vetA, "POST", "/api/prices", map[string]string{"name": "კონსულტაცია", "price": "15/30"})
	expect(t, r, http.StatusCreated)
	var p struct{ ID uint }
	r.json(t, &p)
	path := fmt.Sprintf("/api/prices/%d", p.ID)
	expect(t, call(t, vetB, "PUT", path, map[string]string{"price": "0"}), http.StatusNotFound)
	expect(t, call(t, vetB, "DELETE", path, nil), http.StatusNotFound)
	expect(t, call(t, vetA, "GET", "/api/prices?clinic="+clinicB, nil), http.StatusForbidden)
	expect(t, call(t, vetA, "PUT", path, map[string]string{"price": "20", "zip": clinicB}), http.StatusOK)
	var got models.Price
	must(t, db.First(&got, p.ID).Error)
	if got.Price != "20" || got.SK != clinicA {
		t.Errorf("after update: %+v", got)
	}
	expect(t, call(t, vetA, "PUT", path, map[string]string{"name": ""}), http.StatusBadRequest)
}

// ---- allergies ----

func TestAllergiesReadLegacyColumnsAndScope(t *testing.T) {
	reset(t)
	// A row as PHP writes it: the allergy in `vac`, the note in `ser`.
	must(t, db.Exec(`INSERT INTO eals (uuid, date, vac, ser, sk) VALUES (?, '2025-01-01', 'ალერგია ნობივაკზე', 'გამონაყარი', ?)`, fmt.Sprint(petA), clinicB).Error)

	r := call(t, vetA, "GET", fmt.Sprintf("/api/allergies?pet_id=%d", petA), nil)
	expect(t, r, http.StatusOK)
	var items []struct {
		ID            uint
		Name, Comment string
		Mine          bool
	}
	r.json(t, &items)
	if len(items) != 1 || items[0].Name != "ალერგია ნობივაკზე" || items[0].Comment != "გამონაყარი" || items[0].Mine {
		t.Fatalf("items = %+v", items)
	}
	// Clinic A may not delete clinic B's entry.
	expect(t, call(t, vetA, "DELETE", fmt.Sprintf("/api/allergies/%d", items[0].ID), nil), http.StatusNotFound)

	// A clinic with no link to the pet cannot read or add.
	expect(t, call(t, vetB, "GET", fmt.Sprintf("/api/allergies?pet_id=%d", petA), nil), http.StatusNotFound)
	expect(t, call(t, vetB, "POST", "/api/allergies", map[string]string{"uuid": fmt.Sprint(petA), "name": "x"}), http.StatusNotFound)

	r = call(t, vetA, "POST", "/api/allergies", map[string]string{"uuid": fmt.Sprint(petA), "name": "პენიცილინი", "comment": "შოკი"})
	expect(t, r, http.StatusCreated)
	var row models.Allergy
	must(t, db.Where("vac = ?", "პენიცილინი").First(&row).Error)
	if row.Comment != "შოკი" || row.SK != clinicA || row.Date == "" {
		t.Errorf("stored = %+v", row)
	}
}

// ---- appointments ----

func TestAppointmentsUseAppointmentDayAndPerVetSlots(t *testing.T) {
	reset(t)
	r := call(t, vetA, "POST", "/api/appointments", map[string]interface{}{
		"uuid": fmt.Sprint(petA), "date": "2026-10-05", "time": "08:00", "tpname": "ქირურგია", "price": "300",
	})
	expect(t, r, http.StatusCreated)
	var a struct {
		ID                           uint
		Date, BookedOn, PName, Owner string
	}
	r.json(t, &a)
	if a.Date != "2026-10-05" || a.PName != "იოში" || a.Owner != ownerID {
		t.Errorf("created = %+v", a)
	}
	var row models.Appointment
	must(t, db.First(&row, a.ID).Error)
	if row.Date != "2026-10-05" || row.BookedOn == "2026-10-05" || row.Date3 != "2026,09,05" {
		t.Errorf("stored date2/date/date3 = %q/%q/%q", row.Date, row.BookedOn, row.Date3)
	}

	// Same vet, same slot: refused. Another vet: fine.
	expect(t, call(t, vetA, "POST", "/api/appointments", map[string]interface{}{"pname": "walk-in", "date": "2026-10-05", "time": "08:00", "tpname": "x"}), http.StatusConflict)
	expect(t, call(t, vetA, "POST", "/api/appointments", map[string]interface{}{"pname": "walk-in", "date": "2026-10-05", "time": "08:00", "tpname": "x", "vetname": fmt.Sprint(vetA2)}), http.StatusCreated)
	expect(t, call(t, vetA, "POST", "/api/appointments", map[string]interface{}{"pname": "x", "date": "2026-10-05", "time": "07:00", "tpname": "x"}), http.StatusBadRequest)

	r = call(t, vetA, "GET", "/api/appointments/slots?date=2026-10-05", nil)
	expect(t, r, http.StatusOK)
	var slots []struct {
		Time      string
		Available bool
	}
	r.json(t, &slots)
	if len(slots) != 27 || slots[0].Time != "08:00" || slots[0].Available || !slots[1].Available || slots[26].Time != "21:00" {
		t.Errorf("slots = %+v", slots)
	}

	r = call(t, vetA, "GET", "/api/appointments?date_from=2026-10-05&date_to=2026-10-05", nil)
	expect(t, r, http.StatusOK)
	var page struct{ Total int64 }
	r.json(t, &page)
	if page.Total != 2 {
		t.Errorf("listed %d, want 2", page.Total)
	}

	path := fmt.Sprintf("/api/appointments/%d", a.ID)
	expect(t, call(t, vetB, "PUT", path, map[string]string{"koment": "x"}), http.StatusNotFound)
	expect(t, call(t, vetB, "DELETE", path, nil), http.StatusNotFound)
	expect(t, call(t, vetA, "PUT", path+"/slot", map[string]string{"time": "09:30"}), http.StatusOK)
	expect(t, call(t, vetA, "DELETE", path, nil), http.StatusOK)

	// Owner app shows the appointment day, not the booking day.
	expect(t, call(t, vetA, "POST", "/api/appointments", map[string]interface{}{"uuid": fmt.Sprint(petA), "date": "2027-01-15", "time": "10:00", "tpname": "კონსულტაცია"}), http.StatusCreated)
	r = call(t, owner, "GET", "/api/owner/visits", nil)
	expect(t, r, http.StatusOK)
	var visits []struct {
		Date     string
		Upcoming bool
	}
	r.json(t, &visits)
	if len(visits) != 1 || visits[0].Date != "2027-01-15" || !visits[0].Upcoming {
		t.Errorf("owner visits = %+v", visits)
	}
}

// ---- staff ----

func TestStaffManagedByClinicWithSoftRemoval(t *testing.T) {
	reset(t)
	r := call(t, vetA, "GET", "/api/staff", nil)
	expect(t, r, http.StatusOK)
	var staff []struct{ ID uint }
	r.json(t, &staff)
	if len(staff) != 2 {
		t.Fatalf("clinic A staff = %d, want 2", len(staff))
	}
	expect(t, call(t, vetA, "GET", "/api/staff?clinic="+clinicB, nil), http.StatusForbidden)

	r = call(t, vetA, "POST", "/api/staff", map[string]string{"first_name": "ახალი", "last_name": "33333333333", "email": "New@Test.ge", "password": "secret1"})
	expect(t, r, http.StatusCreated)
	var created struct{ ID uint }
	r.json(t, &created)
	expect(t, call(t, vetA, "POST", "/api/staff", map[string]string{"first_name": "x", "last_name": "1", "email": "new@test.ge", "password": "secret1"}), http.StatusConflict)

	// New vet can log in.
	expect(t, call(t, 0, "POST", "/api/auth/login", map[string]string{"email": "new@test.ge", "password": "secret1"}), http.StatusOK)

	// password_hash / status / zip are not editable.
	expect(t, call(t, vetA, "PUT", fmt.Sprintf("/api/staff/%d", created.ID), map[string]string{
		"phone": "555", "password_hash": "x", "status": "F", "zip": clinicB,
	}), http.StatusOK)
	var u models.User
	must(t, db.First(&u, created.ID).Error)
	if u.Phone != "555" || u.Status != "T" || u.Zip != clinicA || u.PasswordHash == "x" {
		t.Errorf("after update: %+v", u)
	}
	expect(t, call(t, vetB, "PUT", fmt.Sprintf("/api/staff/%d", created.ID), map[string]string{"phone": "1"}), http.StatusNotFound)
	expect(t, call(t, vetA, "DELETE", fmt.Sprintf("/api/staff/%d", vetA), nil), http.StatusBadRequest)

	// Removal keeps the row, like vet/delatevet.php, and blocks login.
	expect(t, call(t, vetA, "DELETE", fmt.Sprintf("/api/staff/%d", created.ID), nil), http.StatusOK)
	must(t, db.First(&u, created.ID).Error)
	if u.Email != "13131313new@test.ge" || u.Zip != "8888888888" || u.Status != "F" {
		t.Errorf("after removal: %+v", u)
	}
	expect(t, call(t, 0, "POST", "/api/auth/login", map[string]string{"email": "new@test.ge", "password": "secret1"}), http.StatusUnauthorized)
}

// ---- login ----

func TestDisabledAccountCannotSignIn(t *testing.T) {
	reset(t)
	hash, _ := auth.HashPassword("pw123456")
	// Two rows share an address; the lower id is disabled (account 797/845 shape).
	must(t, db.Create(&models.User{ID: 9101, Email: "dup@test.ge", GroupID: models.RoleVet, Zip: clinicA, Status: "F", PasswordHash: hash}).Error)
	must(t, db.Create(&models.User{ID: 9102, Email: "dup@test.ge", GroupID: models.RoleVet, Zip: clinicA, Status: "T", PasswordHash: hash}).Error)
	r := call(t, 0, "POST", "/api/auth/login", map[string]string{"email": "dup@test.ge", "password": "pw123456"})
	expect(t, r, http.StatusOK)
	var tok struct {
		RefreshToken string `json:"refresh_token"`
	}
	r.json(t, &tok)
	claims, err := auth.ValidateAccessToken(func() string {
		var x struct {
			AccessToken string `json:"access_token"`
		}
		r.json(t, &x)
		return x.AccessToken
	}())
	must(t, err)
	if claims.UserID != 9102 {
		t.Errorf("signed in as %d, want the active row 9102", claims.UserID)
	}

	must(t, db.Model(&models.User{}).Where("id = ?", 9102).Update("status", "F").Error)
	expect(t, call(t, 0, "POST", "/api/auth/login", map[string]string{"email": "dup@test.ge", "password": "pw123456"}), http.StatusUnauthorized)
}
