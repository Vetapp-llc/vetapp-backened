package handlers

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var deleteAccountLimiter = middleware.NewRateLimiter(5, 15*time.Minute)
var errDeletionIdentityShared = errors.New("personal ID shared by another owner account")

// Enable only after the legacy account database and its dependent data
// have been retired or included in the erasure process. The one-way
// MySQL sync alone does not erase the source copy.
func ownerAccountDeletionEnabled() bool {
	return os.Getenv("OWNER_ACCOUNT_DELETION_ENABLED") == "1"
}

type DeleteAccountRequest struct {
	CurrentPassword string `json:"current_password" validate:"required,max=200"`
	Confirm         bool   `json:"confirm"`
}

// DeleteAccount permanently removes an owner's login and owner-created
// content. Clinic-authored medical and financial records remain with the
// clinic, with the account's denormalized contact/identity fields cleared.
// File removal is durable: a storage outage leaves a private retry item.
// @Summary Delete the signed-in owner account
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body DeleteAccountRequest true "Current password and explicit confirmation"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /auth/me [delete]
func (h *AuthHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}
	if claims.GroupID != models.RoleOwner {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "only owner accounts can be deleted here"})
		return
	}
	if !ownerAccountDeletionEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "account deletion is not configured", Code: "deletion_unavailable"})
		return
	}
	if !deleteAccountLimiter.Allow(strconv.FormatUint(uint64(claims.UserID), 10)) {
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
	err := h.db.Transaction(func(tx *gorm.DB) error { return eraseOwner(tx, user) })
	if err != nil {
		if errors.Is(err, errDeletionIdentityShared) {
			writeJSON(w, http.StatusConflict, ErrorResponse{Error: "shared legacy identity requires support assistance", Code: "deletion_identity_shared"})
		} else {
			middleware.RequestLogger(r).Error("account_deletion_failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "account could not be deleted; please try again"})
		}
		return
	}
	middleware.InvalidateAccount(user.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func eraseOwner(tx *gorm.DB, user models.User) error {
	identity := strings.TrimSpace(user.LastName)
	// Coordinate with registration and stop a concurrent password change
	// from being silently accepted after the confirmation check.
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('register|' || ?))`, identity).Error; err != nil {
		return err
	}
	var current models.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, user.ID).Error; err != nil {
		return err
	}
	if current.PasswordHash != user.PasswordHash || string(current.Password) != string(user.Password) || !current.IsOwner() {
		return errors.New("account changed during deletion")
	}
	if identity != "" {
		var other int64
		if err := tx.Model(&models.User{}).Where("TRIM(last_name) = ? AND group_id = ? AND id <> ?", identity, models.RoleOwner, user.ID).Count(&other).Error; err != nil {
			return err
		}
		if other > 0 {
			return errDeletionIdentityShared
		}
		// Lock pets before their dependent records, matching ordinary pet
		// mutations; the legacy sync's change triggers preserve tombstones.
		var pets []models.Pet
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("TRIM(uuid) = ?", identity).Find(&pets).Error; err != nil {
			return err
		}
		ids := make([]string, 0, len(pets))
		for _, pet := range pets {
			ids = append(ids, strconv.FormatUint(uint64(pet.ID), 10))
		}
		if len(ids) > 0 {
			stmts := []struct {
				sql  string
				args []any
			}{
				{`DELETE FROM pet_share_tokens WHERE pet_id::text IN ?`, []any{ids}},
				{`INSERT INTO vetapp_private.file_deletions (object_key) SELECT object_key FROM procedure_files WHERE uploaded_by = ? OR procedure_id IN (SELECT id FROM vaccination WHERE uuid IN ? AND COALESCE(TRIM(vetname), '') IN ('', '0')) ON CONFLICT DO NOTHING`, []any{user.ID, ids}},
				{`DELETE FROM procedure_files WHERE uploaded_by = ? OR procedure_id IN (SELECT id FROM vaccination WHERE uuid IN ? AND COALESCE(TRIM(vetname), '') IN ('', '0'))`, []any{user.ID, ids}},
				{`DELETE FROM vaccination WHERE uuid IN ? AND COALESCE(TRIM(vetname), '') IN ('', '0')`, []any{ids}},
				{`UPDATE vaccination SET owner = '', ownern = '', pn = '', address = '' WHERE uuid IN ?`, []any{ids}},
				{`DELETE FROM eals WHERE uuid IN ? AND COALESCE(TRIM(sk), '') IN ('', '0')`, []any{ids}},
				{`DELETE FROM pro WHERE pruuid IN (SELECT id::text FROM homepro WHERE puuid IN ? OR date2 = ?)`, []any{ids, identity}},
				{`DELETE FROM homepro WHERE puuid IN ? OR date2 = ?`, []any{ids, identity}},
				{`UPDATE operationdate SET owner = '', ownern = '' WHERE uuid IN ? OR owner = ?`, []any{ids, identity}},
				// A pet never registered with a clinic has no independent
				// clinic record to retain. Remove its owner-created profile.
				{`DELETE FROM pets WHERE id::text IN ? AND COALESCE(TRIM(vet), '') IN ('', '0') AND NOT EXISTS (SELECT 1 FROM vaccination v WHERE v.uuid = pets.id::text) AND NOT EXISTS (SELECT 1 FROM operationdate o WHERE o.uuid = pets.id::text AND COALESCE(TRIM(o.sk), '') NOT IN ('', '0'))`, []any{ids}},
				{`UPDATE pets SET uuid = '', first_name = '', phone = '', email = '', code = '' WHERE id::text IN ?`, []any{ids}},
			}
			for _, s := range stmts {
				if err := tx.Exec(s.sql, s.args...).Error; err != nil {
					return err
				}
			}
		}
		// Old orphaned home records can outlive their pet row.
		if err := tx.Exec(`DELETE FROM pro WHERE pruuid IN (SELECT id::text FROM homepro WHERE date2 = ?)`, identity).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM homepro WHERE date2 = ?`, identity).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE operationdate SET owner = '', ownern = '' WHERE owner = ?`, identity).Error; err != nil {
			return err
		}
	}
	// Include uploads even when a pet has since changed owner.
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO vetapp_private.file_deletions (object_key) SELECT object_key FROM procedure_files WHERE uploaded_by = ? ON CONFLICT DO NOTHING`, []any{user.ID}},
		{`DELETE FROM procedure_files WHERE uploaded_by = ?`, []any{user.ID}},
		{`DELETE FROM email_verification_tokens WHERE user_id = ?`, []any{user.ID}},
		{`DELETE FROM idempotency_keys WHERE user_id = ?`, []any{user.ID}},
	} {
		if err := tx.Exec(s.sql, s.args...).Error; err != nil {
			return err
		}
	}
	if user.Phone != "" {
		var sharing int64
		if err := tx.Model(&models.User{}).Where("phone = ? AND id <> ?", user.Phone, user.ID).Count(&sharing).Error; err != nil {
			return err
		}
		if sharing == 0 {
			if err := tx.Where("phone = ?", user.Phone).Delete(&models.OTP{}).Error; err != nil {
				return err
			}
		}
	}
	return tx.Delete(&models.User{}, user.ID).Error
}
