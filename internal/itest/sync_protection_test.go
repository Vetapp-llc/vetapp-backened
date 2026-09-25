package itest

import (
	"fmt"
	"net/http"
	"testing"

	"vetapp-backend/internal/models"
)

type change struct {
	TableName string
	RowID     int64
	Op        string
}

func changes(t *testing.T) []change {
	t.Helper()
	var out []change
	must(t, db.Raw(`SELECT table_name, row_id, op FROM app_changes ORDER BY table_name, row_id`).Scan(&out).Error)
	return out
}

// Migration 016: app edits/deletes of MySQL-origin rows are logged so the
// sync keeps them; app-created rows live above 1e9; sync writes and login
// bookkeeping are not edits.
func TestAppChangesAreLoggedForTheSync(t *testing.T) {
	reset(t)
	legacy := insertProc(t, models.Procedure{ID: 5000, UUID: fmt.Sprint(petA), TP: 1, SK: clinicA, Phone: "0", Price: "10", Date: "2026-01-01"})
	unpaid := insertProc(t, models.Procedure{ID: 5001, UUID: fmt.Sprint(petA), TP: 1, SK: clinicA, Phone: "0", Price: "10", Date: "2026-01-01"})
	must(t, db.Exec(`TRUNCATE app_changes`).Error) // the two rows above stand for synced MySQL rows

	expect(t, call(t, vetA, "PUT", fmt.Sprintf("/api/procedures/%d", legacy.ID), map[string]string{"coment": "edited in app"}), http.StatusOK)
	expect(t, call(t, vetA, "DELETE", fmt.Sprintf("/api/procedures/%d", unpaid.ID), nil), http.StatusOK)

	r := call(t, vetA, "POST", "/api/procedures", map[string]interface{}{"uuid": fmt.Sprint(petA), "tp": 108})
	expect(t, r, http.StatusCreated)
	var created models.Procedure
	r.json(t, &created)
	if created.ID < 1_000_000_000 {
		t.Errorf("app-created id %d is below the app range", created.ID)
	}

	// Signing in stamps bookkeeping columns only: not an edit.
	hash, _ := auth.HashPassword("pw123456")
	must(t, db.Model(&models.User{}).Where("id = ?", vetB).Updates(map[string]interface{}{"password_hash": hash, "email": "vet-b@test.ge"}).Error)
	must(t, db.Exec(`DELETE FROM app_changes WHERE table_name = 'memberlogin_members'`).Error)
	expect(t, call(t, 0, "POST", "/api/auth/login", map[string]string{"email": "vet-b@test.ge", "password": "pw123456"}), http.StatusOK)

	// A write marked as the sync's own is not an app change either.
	tx := db.Begin()
	must(t, tx.Exec(`SET LOCAL vetapp.sync = 'on'`).Error)
	must(t, tx.Exec(`UPDATE vaccination SET price = '99' WHERE id = ?`, legacy.ID).Error)
	must(t, tx.Commit().Error)

	got := changes(t)
	want := []change{{"vaccination", 5000, "update"}, {"vaccination", 5001, "delete"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("app_changes = %+v, want %+v", got, want)
	}
}
