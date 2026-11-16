package itest

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"vetapp-backend/internal/config"
	"vetapp-backend/internal/models"
	"vetapp-backend/internal/services"
)

func deletionPassword(t *testing.T) string {
	t.Helper()
	// The disposable test database is the only account store in this test.
	t.Setenv("OWNER_ACCOUNT_DELETION_ENABLED", "1")
	const password = "disposable-test-password"
	hash, err := auth.HashPassword(password)
	must(t, err)
	must(t, db.Model(&models.User{}).Where("id = ?", owner).Update("password_hash", hash).Error)
	return password
}

func TestAccountDeletionDisabledUntilErasureSystemsReady(t *testing.T) {
	reset(t)
	t.Setenv("OWNER_ACCOUNT_DELETION_ENABLED", "")
	expect(t, call(t, owner, "DELETE", "/api/auth/me", map[string]any{"current_password": "unused", "confirm": true}), http.StatusServiceUnavailable)
	res := call(t, owner, "GET", "/api/auth/me", nil)
	expect(t, res, http.StatusOK)
	var profile struct {
		Available bool `json:"account_deletion_available"`
	}
	res.json(t, &profile)
	if profile.Available {
		t.Fatal("unconfigured deletion advertised as available")
	}
}

func TestAccountDeletionRequiresOwnerPasswordAndConfirmation(t *testing.T) {
	reset(t)
	password := deletionPassword(t)
	for _, tc := range []struct {
		user   uint
		body   map[string]any
		status int
	}{
		{0, map[string]any{"current_password": password, "confirm": true}, http.StatusUnauthorized},
		{vetA, map[string]any{"current_password": password, "confirm": true}, http.StatusForbidden},
		{owner, map[string]any{"current_password": password}, http.StatusBadRequest},
		{owner, map[string]any{"current_password": "wrong", "confirm": true}, http.StatusUnauthorized},
	} {
		res := call(t, tc.user, "DELETE", "/api/auth/me", tc.body)
		expect(t, res, tc.status)
	}
	var count int64
	must(t, db.Model(&models.User{}).Where("id = ?", owner).Count(&count).Error)
	if count != 1 {
		t.Fatal("failed confirmation removed the account")
	}
}

func TestAccountDeletionErasesOwnerDataAndPreservesClinicRecords(t *testing.T) {
	reset(t)
	password := deletionPassword(t)
	oldToken := token(t, owner)
	must(t, db.Exec(`TRUNCATE vetapp_private.file_deletions`).Error)
	must(t, db.Create(&models.Pet{ID: 90003, UUID: ownerID, Name: "Owner-only pet"}).Error)
	must(t, db.Create(&models.Procedure{ID: 92001, UUID: "90001", Owner: ownerID, VetName: "0", Vac: "owner note"}).Error)
	must(t, db.Create(&models.Procedure{ID: 92002, UUID: "90001", Owner: ownerID, OwnerN: "Owner", Address: "Private", Pn: ownerID, VetName: "9001", Vac: "clinic vaccination"}).Error)
	must(t, db.Create(&models.ProcedureFile{ProcedureID: 92001, PetID: "90001", ObjectKey: "90001/owner-upload", FileName: "test.pdf", UploadedBy: owner}).Error)
	must(t, db.Create(&models.ProcedureFile{ProcedureID: 92002, PetID: "90001", ObjectKey: "90001/clinic-upload", FileName: "clinic.pdf", UploadedBy: vetA}).Error)
	must(t, db.Create(&models.HomeProcedure{ID: 93001, OwnerID: ownerID, PetID: "90001", Name: "tablet"}).Error)
	must(t, db.Create(&models.HomeProcedureDone{HomeProcedureID: "93001"}).Error)
	must(t, db.Exec(`INSERT INTO pet_share_tokens (token, pet_id, kind, expires_at) VALUES ('test-share-token', ?, 'link', NOW() + INTERVAL '1 day')`, petA).Error)
	res := call(t, owner, "DELETE", "/api/auth/me", map[string]any{"current_password": password, "confirm": true})
	expect(t, res, http.StatusOK)
	for _, table := range []string{"memberlogin_members", "vaccination", "procedure_files", "homepro", "pro", "pet_share_tokens"} {
		var count int64
		query := "SELECT COUNT(*) FROM " + table
		switch table {
		case "memberlogin_members":
			query += " WHERE id = 9004"
		case "vaccination":
			query += " WHERE id = 92001"
		case "procedure_files":
			query += " WHERE uploaded_by = 9004"
		}
		must(t, db.Raw(query).Scan(&count).Error)
		if count != 0 {
			t.Fatalf("owner data remains in %s: %d", table, count)
		}
	}
	var medical models.Procedure
	must(t, db.First(&medical, 92002).Error)
	if medical.Owner != "" || medical.OwnerN != "" || medical.Pn != "" || medical.Address != "" || medical.Vac != "clinic vaccination" {
		t.Fatal("clinic record not correctly unlinked")
	}
	var pet models.Pet
	must(t, db.First(&pet, petA).Error)
	if pet.UUID != "" || pet.Phone != "" || pet.Email != "" || pet.FirstName != "" {
		t.Fatal("pet retains owner identity")
	}
	var count int64
	must(t, db.Model(&models.Pet{}).Where("id = 90003").Count(&count).Error)
	if count != 0 {
		t.Fatal("owner-only pet profile retained")
	}
	must(t, db.Raw(`SELECT COUNT(*) FROM vetapp_private.file_deletions WHERE object_key = '90001/owner-upload'`).Scan(&count).Error)
	if count != 1 {
		t.Fatal("attachment deletion not durably queued")
	}
	must(t, db.Model(&models.ProcedureFile{}).Where("object_key = '90001/clinic-upload'").Count(&count).Error)
	if count != 1 {
		t.Fatal("clinic attachment removed")
	}
	expect(t, callWithHeader(t, 0, "GET", "/api/auth/me", nil, "Authorization", "Bearer "+oldToken), http.StatusUnauthorized)
	expect(t, call(t, vetB, "GET", "/api/auth/me", nil), http.StatusOK)
}

func TestAccountDeletionDoesNotEraseAnotherLegacyOwner(t *testing.T) {
	reset(t)
	password := deletionPassword(t)
	must(t, db.Create(&models.User{ID: 9006, LastName: ownerID, Email: "legacy@test", GroupID: models.RoleOwner, Status: "T"}).Error)
	expect(t, call(t, owner, "DELETE", "/api/auth/me", map[string]any{"current_password": password, "confirm": true}), http.StatusConflict)
	var count int64
	must(t, db.Model(&models.Pet{}).Where("uuid = ?", ownerID).Count(&count).Error)
	if count != 2 {
		t.Fatal("shared identity's pets were changed")
	}
}

func TestAccountDeletionRollsBackWhenAnyErasureFails(t *testing.T) {
	reset(t)
	password := deletionPassword(t)
	must(t, db.Exec(`TRUNCATE vetapp_private.file_deletions`).Error)
	must(t, db.Exec(`CREATE OR REPLACE FUNCTION fail_test_account_delete() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'test failure'; END $$ LANGUAGE plpgsql;
		CREATE TRIGGER fail_test_account_delete BEFORE DELETE ON memberlogin_members FOR EACH ROW EXECUTE FUNCTION fail_test_account_delete();`).Error)
	t.Cleanup(func() {
		db.Exec(`DROP TRIGGER IF EXISTS fail_test_account_delete ON memberlogin_members; DROP FUNCTION IF EXISTS fail_test_account_delete();`)
	})
	expect(t, call(t, owner, "DELETE", "/api/auth/me", map[string]any{"current_password": password, "confirm": true}), http.StatusInternalServerError)
	var pet models.Pet
	must(t, db.First(&pet, petA).Error)
	if pet.UUID != ownerID {
		t.Fatal("partial erasure survived rollback")
	}
	expect(t, call(t, owner, "GET", "/api/auth/me", nil), http.StatusOK)
}

func TestAccountDeletionFileQueueRetriesStorageFailures(t *testing.T) {
	must(t, db.Exec(`TRUNCATE vetapp_private.file_deletions`).Error)
	must(t, db.Exec(`INSERT INTO vetapp_private.file_deletions (object_key) VALUES ('test/retry.pdf')`).Error)
	status := http.StatusServiceUnavailable
	storageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Error("unexpected storage operation")
		}
		w.WriteHeader(status)
	}))
	defer storageServer.Close()
	storage := services.NewStorageService(&config.Config{SupabaseURL: storageServer.URL, SupabaseServiceKey: "disposable", SupabaseBucket: "test"})
	if services.DrainFileDeletions(db, storage) == nil {
		t.Fatal("storage outage must be observable")
	}
	var count int64
	must(t, db.Raw(`SELECT COUNT(*) FROM vetapp_private.file_deletions`).Scan(&count).Error)
	if count != 1 {
		t.Fatal("failed file deletion was lost")
	}
	status = http.StatusNotFound // A prior attempt may already have removed it.
	must(t, services.DrainFileDeletions(db, storage))
	must(t, db.Raw(`SELECT COUNT(*) FROM vetapp_private.file_deletions`).Scan(&count).Error)
	if count != 0 {
		t.Fatal("successful retry was not acknowledged")
	}
}
