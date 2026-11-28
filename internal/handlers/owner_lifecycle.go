package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

const (
	codeRecipientNotFound = "recipient_not_found"
	codeTransferToSelf    = "transfer_to_self"
)

// notHiddenForOwner excludes pets the owner removed from the app. Takes
// the owner's personal ID; use in a query on the `pets` table.
const notHiddenForOwner = `NOT EXISTS (SELECT 1 FROM owner_hidden_pets ohp
	WHERE ohp.pet_id = pets.id AND ohp.owner_personal_id = ?)`

// recipientLookupLimiter caps personal-ID lookups per owner, so the
// transfer screen can't be used to walk personal IDs for names.
var recipientLookupLimiter = middleware.NewRateLimiter(30, time.Hour)

// TransferRecipientResponse names the owner a pet would be handed to.
type TransferRecipientResponse struct {
	PersonalID string `json:"personal_id" validate:"required"`
	Name       string `json:"name" validate:"required"`
}

// TransferPetRequest is the body of a pet transfer.
type TransferPetRequest struct {
	PersonalID string `json:"personal_id" validate:"required"`
}

// TransferPetResponse confirms a pending transfer request.
type TransferPetResponse struct {
	Message       string `json:"message" validate:"required"`
	RecipientName string `json:"recipient_name" validate:"required"`
	RequestID     uint   `json:"request_id"`
}

// findTransferRecipient applies the transfer rules to a personal ID and
// writes the error response itself when they fail.
func (h *OwnerPortalHandler) findTransferRecipient(w http.ResponseWriter, r *http.Request, callerID, raw string) (models.User, bool) {
	var u models.User
	target := strings.TrimSpace(raw)
	if target == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "personal_id is required"})
		return u, false
	}
	if target == callerID {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "cannot transfer to yourself", Code: codeTransferToSelf})
		return u, false
	}
	err := h.db.WithContext(r.Context()).Select("id", "first_name", "last_name").
		Where("TRIM(last_name) = ? AND group_id = ? AND status = ?", target, models.RoleOwner, "T").
		Order("last_login DESC NULLS LAST, id DESC").Take(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "recipient not found", Code: codeRecipientNotFound})
		return u, false
	}
	if err != nil {
		middleware.RequestLogger(r).Error("transfer_recipient_lookup_failed")
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not look up transfer recipient"})
		return u, false
	}
	return u, true
}

// allowRecipientLookup applies the per-owner lookup rate limit.
func allowRecipientLookup(w http.ResponseWriter, r *http.Request) bool {
	key := ownerPersonalID(r)
	if claims := middleware.GetClaims(r); claims != nil {
		key = strconv.Itoa(int(claims.UserID))
	}
	if recipientLookupLimiter.Allow(key) {
		return true
	}
	wait := int(recipientLookupLimiter.RetryAfter(key).Seconds()) + 1
	writeJSON(w, http.StatusTooManyRequests, ErrorResponse{Error: "too many lookups", Code: codeRateLimited, RetryAfter: wait})
	return false
}

// TransferRecipient looks up the registered owner a pet would be handed
// to, so the app can show their name before the transfer is confirmed.
// @Summary Look up a pet transfer recipient by personal ID
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param personal_id query string true "Recipient personal ID"
// @Success 200 {object} TransferRecipientResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 429 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /owner/transfer-recipient [get]
func (h *OwnerPortalHandler) TransferRecipient(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	callerID := ownerPersonalID(r)
	if callerID == "" {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}
	if !allowRecipientLookup(w, r) {
		return
	}
	u, ok := h.findTransferRecipient(w, r, callerID, r.URL.Query().Get("personal_id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, TransferRecipientResponse{
		PersonalID: strings.TrimSpace(u.LastName),
		Name:       strings.TrimSpace(u.FirstName),
	})
}

// TransferPet records a request. Only the recipient can accept it.
// @Summary Request transfer of a pet to another owner
// @Tags owner
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param body body TransferPetRequest true "Recipient"
// @Success 202 {object} TransferPetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 429 {object} ErrorResponse
// @Router /owner/pets/{id}/transfer [post]
func (h *OwnerPortalHandler) TransferPet(w http.ResponseWriter, r *http.Request) {
	callerID := ownerPersonalID(r)
	pet, err := h.verifyPetOwnership(chi.URLParam(r, "id"), callerID)
	if err != nil || callerID == "" {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}
	var req TransferPetRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if !allowRecipientLookup(w, r) {
		return
	}
	recipient, ok := h.findTransferRecipient(w, r, callerID, req.PersonalID)
	if !ok {
		return
	}

	request := petTransferRequest{
		PetID: pet.ID, SenderUserID: middleware.GetClaims(r).UserID, RecipientUserID: recipient.ID,
		FromPersonalID: callerID, ToPersonalID: strings.TrimSpace(recipient.LastName),
	}
	created := false
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := lockOwnedPet(tx, request.PetID, callerID); err != nil {
			return err
		}
		var pending petTransferRequest
		err := tx.Where("pet_id = ? AND status = 'pending'", request.PetID).Take(&pending).Error
		if err == nil {
			if pending.SenderUserID != request.SenderUserID || pending.RecipientUserID != request.RecipientUserID {
				return errTransferPending
			}
			request = pending
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		request.Status = "pending"
		created = true
		return tx.Create(&request).Error
	})
	if err != nil {
		writeTransferError(w, err)
		return
	}
	if created {
		h.notifyTransferRecipient(r, recipient.ID)
	}
	writeJSON(w, http.StatusAccepted, TransferPetResponse{
		Message: "pending", RecipientName: strings.TrimSpace(recipient.FirstName), RequestID: request.ID,
	})
}
