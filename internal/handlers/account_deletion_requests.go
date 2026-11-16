package handlers

import (
	"net/http"
	"strconv"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

const deletionRequestsTable = "vetapp_private.account_deletion_requests"

// No passwords or tokens are retained. The contact snapshot lets staff complete
// both stores and contact the owner even after removing the login. Completion
// clears that snapshot; only the receipt and dates remain.
type accountDeletionRequestRow struct {
	ID          uint       `json:"id"`
	UserID      uint       `json:"user_id"`
	Name        string     `json:"name"`
	Email       string     `json:"email"`
	PersonalID  string     `json:"personal_id"`
	RequestedAt time.Time  `json:"requested_at"`
	DueAt       time.Time  `json:"due_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

func (r accountDeletionRequestRow) receipt() map[string]any {
	return map[string]any{"id": r.ID, "requested_at": r.RequestedAt, "due_at": r.DueAt, "status": "pending"}
}

// AccountDeletionRequest returns only the signed-in owner's receipt.
func (h *AuthHandler) AccountDeletionRequest(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil || claims.GroupID != models.RoleOwner {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "owner account required"})
		return
	}
	var row accountDeletionRequestRow
	err := h.db.Table(deletionRequestsTable).Where("user_id = ? AND completed_at IS NULL", claims.UserID).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		writeJSON(w, http.StatusOK, map[string]any{"request": nil})
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not load deletion request"})
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"request": row.receipt()})
	}
}

// RequestAccountDeletion initiates deletion in-app. It deliberately does not
// claim immediate erasure: staff must remove both active systems within 7 days.
func (h *AuthHandler) RequestAccountDeletion(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil || claims.GroupID != models.RoleOwner {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "owner account required"})
		return
	}
	if !deleteAccountLimiter.Allow("request:" + strconv.FormatUint(uint64(claims.UserID), 10)) {
		w.Header().Set("Retry-After", "900")
		writeJSON(w, http.StatusTooManyRequests, ErrorResponse{Error: "please try again later"})
		return
	}
	var req DeleteAccountRequest
	if err := decodeAndValidate(r, &req); err != nil || !req.Confirm {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "password and explicit confirmation required"})
		return
	}
	var user models.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}
	valid := user.PasswordHash != "" && h.authService.VerifyBcrypt(user.PasswordHash, req.CurrentPassword)
	if !valid && len(user.Password) > 0 {
		valid = h.authService.VerifyLegacyAES(user.Password, req.CurrentPassword)
	}
	if !valid {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "current password is incorrect", Code: codeWrongPassword})
		return
	}
	var row accountDeletionRequestRow
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO vetapp_private.account_deletion_requests
			(user_id, name, email, personal_id) VALUES (?, ?, ?, ?) ON CONFLICT (user_id) DO NOTHING`,
			user.ID, user.FirstName, user.Email, user.LastName).Error; err != nil {
			return err
		}
		return tx.Table(deletionRequestsTable).Where("user_id = ?", user.ID).Take(&row).Error
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not save deletion request; please try again"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"request": row.receipt()})
}

// AccountDeletionRequests is restricted to the existing super-admin role.
func (h *AdminHandler) AccountDeletionRequests(w http.ResponseWriter, r *http.Request) {
	rows := []accountDeletionRequestRow{}
	if err := h.db.Table(deletionRequestsTable).Where("completed_at IS NULL").Order("due_at ASC").Find(&rows).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not load deletion requests"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": rows})
}

// CompleteAccountDeletion records staff's confirmation, never performs erasure.
// A disabled login is insufficient: the new-system account must be gone, and
// staff must explicitly attest to legacy erasure, session revocation and notice.
func (h *AdminHandler) CompleteAccountDeletion(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id == 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request"})
		return
	}
	var req struct {
		LegacyErased     bool `json:"legacy_erased"`
		NewDataErased    bool `json:"new_data_erased"`
		SessionsRevoked  bool `json:"sessions_revoked"`
		ConfirmationSent bool `json:"confirmation_sent"`
	}
	if err := decodeAndValidate(r, &req); err != nil || !req.LegacyErased || !req.NewDataErased || !req.SessionsRevoked || !req.ConfirmationSent {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "confirm erasure in both systems, revoked sessions and owner notification"})
		return
	}
	var row accountDeletionRequestRow
	if err := h.db.Table(deletionRequestsTable).Where("id = ? AND completed_at IS NULL", id).Take(&row).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pending request not found"})
		return
	}
	var remaining int64
	if err := h.db.Model(&models.User{}).Where("id = ? OR (group_id = ? AND (LOWER(email) = LOWER(?) OR (last_name <> '' AND last_name = ?)))", row.UserID, models.RoleOwner, row.Email, row.PersonalID).Count(&remaining).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not verify account removal"})
		return
	}
	if remaining > 0 {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "the account still exists in the new system; complete erasure before closing this request"})
		return
	}
	claims := middleware.GetClaims(r)
	res := h.db.Table(deletionRequestsTable).Where("id = ? AND completed_at IS NULL", id).
		Updates(map[string]any{"completed_at": time.Now().UTC(), "completed_by": claims.UserID, "name": "", "email": "", "personal_id": "", "user_id": nil})
	if res.Error != nil || res.RowsAffected != 1 {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "request changed; refresh and try again"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"completed": true})
}
