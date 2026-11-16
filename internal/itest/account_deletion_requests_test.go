package itest

import (
	"fmt"
	"net/http"
	"testing"
	"time"
	"vetapp-backend/internal/models"
)

func TestAccountDeletionRequestWorkflow(t *testing.T) {
	reset(t)
	password := deletionPassword(t)
	path := "/api/auth/account-deletion-request"
	adminPath := "/api/admin/account-deletion-requests"
	body := map[string]any{"current_password": password, "confirm": true}
	expect(t, call(t, 0, "POST", path, body), http.StatusUnauthorized)
	expect(t, call(t, vetA, "POST", path, body), http.StatusForbidden)
	expect(t, call(t, owner, "POST", path, map[string]any{"current_password": "wrong", "confirm": true}), http.StatusUnauthorized)
	expect(t, call(t, owner, "POST", path, map[string]any{"current_password": password}), http.StatusBadRequest)
	res := call(t, owner, "POST", path, body)
	expect(t, res, http.StatusAccepted)
	var received struct {
		Request struct {
			ID          uint      `json:"id"`
			RequestedAt time.Time `json:"requested_at"`
			DueAt       time.Time `json:"due_at"`
		} `json:"request"`
	}
	res.json(t, &received)
	if received.Request.ID == 0 || received.Request.DueAt.Sub(received.Request.RequestedAt) != 7*24*time.Hour {
		t.Fatal("missing durable seven-day receipt")
	}
	repeat := call(t, owner, "POST", path, body)
	expect(t, repeat, http.StatusAccepted)
	var again struct {
		Request struct {
			ID    uint      `json:"id"`
			DueAt time.Time `json:"due_at"`
		} `json:"request"`
	}
	repeat.json(t, &again)
	if again.Request.ID != received.Request.ID || !again.Request.DueAt.Equal(received.Request.DueAt) {
		t.Fatal("duplicate request changed the deadline")
	}
	expect(t, call(t, owner, "GET", path, nil), http.StatusOK)
	expect(t, call(t, owner, "GET", adminPath, nil), http.StatusForbidden)
	expect(t, call(t, vetA, "GET", adminPath, nil), http.StatusForbidden)
	list := call(t, admin, "GET", adminPath, nil)
	expect(t, list, http.StatusOK)
	var listed struct {
		Requests []struct {
			ID    uint   `json:"id"`
			Email string `json:"email"`
		} `json:"requests"`
	}
	list.json(t, &listed)
	if len(listed.Requests) != 1 || listed.Requests[0].Email != "owner@test" {
		t.Fatal("staff cannot fulfill request")
	}
	complete := fmt.Sprintf("%s/%d/complete", adminPath, received.Request.ID)
	checks := map[string]any{"legacy_erased": true, "new_data_erased": true, "sessions_revoked": true, "confirmation_sent": true}
	expect(t, call(t, owner, "POST", complete, checks), http.StatusForbidden)
	expect(t, call(t, admin, "POST", complete, map[string]any{}), http.StatusBadRequest)
	expect(t, call(t, admin, "POST", complete, checks), http.StatusConflict)
	var remains int64
	must(t, db.Model(&models.User{}).Where("id = ?", owner).Count(&remains).Error)
	if remains != 1 {
		t.Fatal("request or completion erased account")
	}
	// Disposable fixture only: model staff having finished erasure elsewhere.
	must(t, db.Delete(&models.User{}, owner).Error)
	expect(t, call(t, admin, "POST", complete, checks), http.StatusOK)
	var retained struct {
		Email       string
		Name        string
		PersonalID  string
		UserID      *uint
		CompletedAt *time.Time
	}
	must(t, db.Table("vetapp_private.account_deletion_requests").Where("id = ?", received.Request.ID).Take(&retained).Error)
	if retained.Email != "" || retained.Name != "" || retained.PersonalID != "" || retained.UserID != nil || retained.CompletedAt == nil {
		t.Fatal("completion retained contact snapshot")
	}
}
