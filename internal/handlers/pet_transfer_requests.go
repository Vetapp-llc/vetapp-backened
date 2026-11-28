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
	"gorm.io/gorm/clause"
)

var (
	errTransferPending = errors.New("pet already has a pending request")
	errTransferClosed  = errors.New("transfer request is already closed")
)

type petTransferRequest struct {
	ID              uint
	PetID           uint
	SenderUserID    uint
	RecipientUserID uint
	FromPersonalID  string
	ToPersonalID    string
	Status          string
	CreatedAt       time.Time
}

type PetTransferInboxItem struct {
	ID            uint      `json:"id"`
	PetName       string    `json:"pet_name"`
	SenderName    string    `json:"sender_name"`
	RecipientName string    `json:"recipient_name"`
	Incoming      bool      `json:"incoming"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

type PetTransferDecision struct {
	Action string `json:"action" validate:"required,oneof=accept decline cancel"`
}

func lockOwnedPet(tx *gorm.DB, petID uint, ownerID string) error {
	var pet models.Pet
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND TRIM(uuid) = ?", petID, ownerID).
		Where(notHiddenForOwner, ownerID).Take(&pet).Error
}

func writeTransferError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet or transfer request not found"})
	case errors.Is(err, errTransferPending):
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: err.Error(), Code: "transfer_pending"})
	case errors.Is(err, errTransferClosed):
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: err.Error(), Code: "transfer_closed"})
	default:
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update transfer request"})
	}
}

// ListPetTransfers is the owner's in-app inbox and sent transfer history.
// @Summary List transfer requests for the signed-in owner
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Success 200 {array} PetTransferInboxItem
// @Router /owner/pet-transfers [get]
func (h *OwnerPortalHandler) ListPetTransfers(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetClaims(r).UserID
	items := []PetTransferInboxItem{}
	err := h.db.Table("pet_transfer_requests AS pr").
		Select(`pr.id, COALESCE(pets.name, '') AS pet_name,
			COALESCE(sender.first_name, '') AS sender_name, COALESCE(recipient.first_name, '') AS recipient_name,
			(pr.recipient_user_id = ?) AS incoming, pr.status, pr.created_at`, userID).
		Joins("LEFT JOIN pets ON pets.id = pr.pet_id").
		Joins("LEFT JOIN memberlogin_members AS sender ON sender.id = pr.sender_user_id").
		Joins("LEFT JOIN memberlogin_members AS recipient ON recipient.id = pr.recipient_user_id").
		Where("pr.sender_user_id = ? OR pr.recipient_user_id = ?", userID, userID).
		Order("CASE WHEN pr.status = 'pending' THEN 0 ELSE 1 END, pr.id DESC").
		Limit(100).Scan(&items).Error
	if err != nil {
		writeTransferError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// DecidePetTransfer resolves one request; the recipient alone approves or declines.
// @Summary Accept, decline or cancel a pet transfer request
// @Tags owner
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Transfer request ID"
// @Param body body PetTransferDecision true "Decision"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /owner/pet-transfers/{id} [post]
func (h *OwnerPortalHandler) DecidePetTransfer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeTransferError(w, gorm.ErrRecordNotFound)
		return
	}
	var decision PetTransferDecision
	if err := decodeAndValidate(r, &decision); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	status := "accepted"
	party := "recipient_user_id"
	if decision.Action == "decline" {
		status = "declined"
	} else if decision.Action == "cancel" {
		status = "cancelled"
		party = "sender_user_id"
	}
	userID := middleware.GetClaims(r).UserID
	unavailable := false
	err = h.db.Transaction(func(tx *gorm.DB) error {
		var request petTransferRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", id).Where(party+" = ?", userID).Take(&request).Error; err != nil {
			return err
		}
		if request.Status == status {
			return nil
		}
		if request.Status != "pending" {
			return errTransferClosed
		}
		if status == "accepted" {
			if err := acceptPetTransfer(tx, request); err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				status, unavailable = "cancelled", true
			}
		}
		return tx.Model(&petTransferRequest{}).Where("id = ?", id).
			Updates(map[string]interface{}{"status": status, "resolved_at": time.Now()}).Error
	})
	if err != nil {
		writeTransferError(w, err)
		return
	}
	if unavailable {
		writeTransferError(w, errTransferClosed)
		return
	}
	writeJSON(w, http.StatusOK, MessageResponse{Message: status})
}

func acceptPetTransfer(tx *gorm.DB, request petTransferRequest) error {
	var recipient models.User
	if err := tx.Where("id = ? AND TRIM(last_name) = ? AND group_id = ? AND status = 'T'",
		request.RecipientUserID, request.ToPersonalID, models.RoleOwner).Take(&recipient).Error; err != nil {
		return err
	}
	if err := lockOwnedPet(tx, request.PetID, request.FromPersonalID); err != nil {
		return err
	}
	if err := tx.Model(&models.Pet{}).Where("id = ?", request.PetID).Updates(map[string]interface{}{
		"uuid": request.ToPersonalID, "phone": recipient.Phone, "email": recipient.Email, "first_name": strings.TrimSpace(recipient.FirstName),
	}).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.HomeProcedure{}).Where("puuid = ?", strconv.Itoa(int(request.PetID))).
		Update("date2", request.ToPersonalID).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM owner_hidden_pets WHERE pet_id = ? AND owner_personal_id = ?",
		request.PetID, request.ToPersonalID).Error; err != nil {
		return err
	}
	return tx.Exec("INSERT INTO pet_transfers (pet_id, from_personal_id, to_personal_id) VALUES (?, ?, ?)",
		request.PetID, request.FromPersonalID, request.ToPersonalID).Error
}
