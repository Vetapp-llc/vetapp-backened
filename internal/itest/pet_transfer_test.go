package itest

import (
	"fmt"
	"net/http"
	"testing"

	"vetapp-backend/internal/models"
)

const (
	owner2   uint = 9010
	owner2ID      = "09999999999"
)

// addOwner2 creates a second registered owner with a known password.
func addOwner2(t *testing.T) {
	t.Helper()
	hash, err := auth.HashPassword("secret-pass-2")
	must(t, err)
	must(t, db.Create(&models.User{ID: owner2, FirstName: "ნინო ბერიძე", LastName: owner2ID, Email: "o2@test.ge",
		Phone: "577000000", GroupID: models.RoleOwner, Status: "T", PasswordHash: hash}).Error)
}

func TestOwnerTransferPet(t *testing.T) {
	reset(t)
	addOwner2(t)

	r := call(t, owner, "GET", "/api/owner/transfer-recipient?personal_id="+owner2ID, nil)
	expect(t, r, http.StatusOK)
	var rec struct {
		PersonalID string `json:"personal_id"`
		Name       string `json:"name"`
	}
	r.json(t, &rec)
	if rec.PersonalID != owner2ID || rec.Name != "ნინო ბერიძე" {
		t.Errorf("recipient = %+v", rec)
	}

	var e struct{ Code string }
	r = call(t, owner, "GET", "/api/owner/transfer-recipient?personal_id=12345678901", nil)
	expect(t, r, http.StatusNotFound)
	r.json(t, &e)
	if e.Code != "recipient_not_found" {
		t.Errorf("code = %q", e.Code)
	}
	// Vets are not transfer recipients.
	expect(t, call(t, owner, "GET", "/api/owner/transfer-recipient?personal_id=11111111111", nil), http.StatusNotFound)
	r = call(t, owner, "GET", "/api/owner/transfer-recipient?personal_id="+ownerID, nil)
	expect(t, r, http.StatusBadRequest)
	r.json(t, &e)
	if e.Code != "transfer_to_self" {
		t.Errorf("code = %q", e.Code)
	}
	expect(t, call(t, owner, "GET", "/api/owner/transfer-recipient?personal_id=", nil), http.StatusBadRequest)

	// A home procedure follows the pet.
	expect(t, call(t, owner, "POST", fmt.Sprintf("/api/owner/pets/%d/home-procedures", petA),
		map[string]interface{}{"name": "წამალი", "days": 3}), http.StatusCreated)

	transfer := fmt.Sprintf("/api/owner/pets/%d/transfer", petA)
	expect(t, call(t, owner, "POST", transfer, map[string]string{"personal_id": "12345678901"}), http.StatusNotFound)
	expect(t, call(t, owner, "POST", transfer, map[string]string{"personal_id": ownerID}), http.StatusBadRequest)
	requestBody := map[string]string{"personal_id": owner2ID}
	r = call(t, owner, "POST", transfer, requestBody)
	expect(t, r, http.StatusAccepted)
	var requested struct {
		Message       string `json:"message"`
		RecipientName string `json:"recipient_name"`
		RequestID     uint   `json:"request_id"`
	}
	r.json(t, &requested)
	if requested.Message != "pending" || requested.RecipientName != "ნინო ბერიძე" || requested.RequestID == 0 {
		t.Fatalf("request = %+v", requested)
	}
	decisionPath := fmt.Sprintf("/api/owner/pet-transfers/%d", requested.RequestID)
	retried := call(t, owner, "POST", transfer, requestBody)
	expect(t, retried, http.StatusAccepted)
	var same struct {
		RequestID uint `json:"request_id"`
	}
	retried.json(t, &same)
	if same.RequestID != requested.RequestID {
		t.Fatalf("repeated request changed ID: %+v", same)
	}

	petPath := fmt.Sprintf("/api/owner/pets/%d", petA)
	expect(t, call(t, owner, "GET", petPath, nil), http.StatusOK)
	expect(t, call(t, owner2, "GET", petPath, nil), http.StatusNotFound)
	r = call(t, owner2, "GET", "/api/owner/pet-transfers", nil)
	expect(t, r, http.StatusOK)
	var inbox []struct {
		ID       uint
		Incoming bool
		Status   string
		PetName  string `json:"pet_name"`
	}
	r.json(t, &inbox)
	if len(inbox) != 1 || inbox[0].ID != requested.RequestID || !inbox[0].Incoming ||
		inbox[0].Status != "pending" || inbox[0].PetName == "" {
		t.Fatalf("recipient inbox = %+v", inbox)
	}
	expect(t, call(t, vetA, "GET", "/api/owner/pet-transfers", nil), http.StatusForbidden)
	accept := map[string]string{"action": "accept"}
	expect(t, call(t, owner, "POST", decisionPath, accept), http.StatusNotFound)
	expect(t, call(t, owner2, "POST", decisionPath, map[string]string{"action": "cancel"}), http.StatusNotFound)
	expect(t, call(t, owner2, "POST", decisionPath, accept), http.StatusOK)
	expect(t, call(t, owner2, "POST", decisionPath, accept), http.StatusOK)
	expect(t, call(t, owner2, "POST", decisionPath, map[string]string{"action": "decline"}), http.StatusConflict)

	var pet models.Pet
	must(t, db.First(&pet, petA).Error)
	if pet.UUID != owner2ID || pet.Phone != "577000000" || pet.Email != "o2@test.ge" || pet.FirstName != "ნინო ბერიძე" {
		t.Errorf("pet after transfer = %+v", pet)
	}
	var hp models.HomeProcedure
	must(t, db.Where("puuid = ?", fmt.Sprint(petA)).First(&hp).Error)
	if hp.OwnerID != owner2ID {
		t.Errorf("home procedure owner = %q", hp.OwnerID)
	}
	var n int64
	db.Raw(`SELECT COUNT(*) FROM pet_transfers WHERE pet_id = ? AND from_personal_id = ? AND to_personal_id = ?`,
		petA, ownerID, owner2ID).Scan(&n)
	if n != 1 {
		t.Errorf("audit rows = %d", n)
	}

	expect(t, call(t, owner, "GET", fmt.Sprintf("/api/owner/pets/%d", petA), nil), http.StatusNotFound)
	expect(t, call(t, owner2, "GET", fmt.Sprintf("/api/owner/pets/%d", petA), nil), http.StatusOK)
	// The old owner can't transfer it again.
	expect(t, call(t, owner, "POST", transfer, map[string]string{"personal_id": owner2ID}), http.StatusNotFound)
}

func TestOwnerTransferDeclineAndCancel(t *testing.T) {
	reset(t)
	addOwner2(t)
	petPath := fmt.Sprintf("/api/owner/pets/%d", petA)
	for _, next := range []struct {
		user   uint
		action string
	}{
		{owner2, "decline"},
		{owner, "cancel"},
	} {
		r := call(t, owner, "POST", petPath+"/transfer", map[string]string{"personal_id": owner2ID})
		expect(t, r, http.StatusAccepted)
		var request struct {
			ID uint `json:"request_id"`
		}
		r.json(t, &request)
		path := fmt.Sprintf("/api/owner/pet-transfers/%d", request.ID)
		expect(t, call(t, next.user, "POST", path, map[string]string{"action": next.action}), http.StatusOK)
		expect(t, call(t, owner2, "POST", path, map[string]string{"action": "accept"}), http.StatusConflict)
		expect(t, call(t, owner, "GET", petPath, nil), http.StatusOK)
		expect(t, call(t, owner2, "GET", petPath, nil), http.StatusNotFound)
	}
}

func TestOwnerTransferPushRegistration(t *testing.T) {
	reset(t)
	addOwner2(t)
	path := "/api/owner/push-devices"
	token := "ExpoPushToken[synthetic_owner_device]"
	body := map[string]string{"token": token}
	expect(t, call(t, owner, "PUT", path, map[string]string{"token": "bad"}), http.StatusBadRequest)
	expect(t, call(t, owner, "PUT", path, body), http.StatusOK)
	expect(t, call(t, owner2, "PUT", path, body), http.StatusOK)
	expect(t, call(t, owner, "DELETE", path, body), http.StatusOK)
	var userID uint
	must(t, db.Raw("SELECT user_id FROM owner_push_devices WHERE token = ?", token).Scan(&userID).Error)
	if userID != owner2 {
		t.Fatalf("shared device is registered to %d, want %d", userID, owner2)
	}
	expect(t, call(t, owner2, "DELETE", path, body), http.StatusOK)
	var count int64
	must(t, db.Table("owner_push_devices").Where("token = ?", token).Count(&count).Error)
	if count != 0 {
		t.Fatal("device still registered after logout")
	}
}
