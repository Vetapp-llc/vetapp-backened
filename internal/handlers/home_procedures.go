package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// Home procedures — treatments the owner gives at home over a number of
// days, with a log of each time it was done (owner/homep.php,
// owner/addhomeprocedure.php, calendar2/). Owner app only.

// maxHomeProcedureDays is the longest period the PHP form offers.
const maxHomeProcedureDays = 27

// HomeProcedureItem is one home procedure with its completion log.
type HomeProcedureItem struct {
	ID        uint     `json:"id" validate:"required"`
	Name      string   `json:"name" validate:"required"`
	StartDate string   `json:"start_date" validate:"required"`
	Days      int      `json:"days" validate:"required"`
	EndDate   string   `json:"end_date" validate:"required"`
	Active    bool     `json:"active" validate:"required"`
	Done      []string `json:"done" validate:"required"` // "YYYY-MM-DD HH:MM", newest first
}

// CreateHomeProcedureRequest adds a home procedure.
type CreateHomeProcedureRequest struct {
	Name      string `json:"name" validate:"required"`
	StartDate string `json:"start_date"` // defaults to today
	Days      int    `json:"days" validate:"required,min=1,max=27"`
}

// ownerPet loads a pet owned by the calling owner.
func (h *OwnerPortalHandler) ownerPet(r *http.Request) (models.Pet, bool) {
	var pet models.Pet
	personalID := ownerPersonalID(r)
	if personalID == "" {
		return pet, false
	}
	err := h.db.Where("id = ? AND uuid = ?", chi.URLParam(r, "id"), personalID).First(&pet).Error
	return pet, err == nil
}

func homeItem(hp models.HomeProcedure, done []string, today string) HomeProcedureItem {
	days, err := strconv.Atoi(strings.TrimSpace(hp.PeriodCode))
	if err != nil || days < 0 {
		days = 0
	}
	days++ // stored zero-based
	it := HomeProcedureItem{ID: hp.ID, Name: hp.Name, StartDate: hp.StartDate, Days: days, Done: done}
	if start, err := time.Parse("2006-01-02", hp.StartDate); err == nil {
		it.EndDate = start.AddDate(0, 0, days-1).Format("2006-01-02")
		it.Active = hp.StartDate <= today && today <= it.EndDate
	}
	if it.Done == nil {
		it.Done = []string{}
	}
	return it
}

// HomeProcedures lists a pet's home procedures, newest first.
// @Summary List home procedures
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Success 200 {array} HomeProcedureItem
// @Failure 404 {object} ErrorResponse
// @Router /owner/pets/{id}/home-procedures [get]
func (h *OwnerPortalHandler) HomeProcedures(w http.ResponseWriter, r *http.Request) {
	pet, ok := h.ownerPet(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}
	var list []models.HomeProcedure
	h.db.Where("puuid = ?", strconv.Itoa(int(pet.ID))).Order("id DESC").Find(&list)

	// All completion marks for the page in one query.
	ids := make([]string, len(list))
	for i, hp := range list {
		ids[i] = strconv.Itoa(int(hp.ID))
	}
	doneBy := map[string][]string{}
	if len(ids) > 0 {
		var marks []models.HomeProcedureDone
		h.db.Where("pruuid IN ?", ids).Order("id DESC").Find(&marks)
		for _, m := range marks {
			doneBy[m.HomeProcedureID] = append(doneBy[m.HomeProcedureID], m.DoneAt)
		}
	}

	today := todayGeorgia()
	items := make([]HomeProcedureItem, len(list))
	for i, hp := range list {
		items[i] = homeItem(hp, doneBy[strconv.Itoa(int(hp.ID))], today)
	}
	writeJSON(w, http.StatusOK, items)
}

// CreateHomeProcedure adds a home procedure for the owner's pet.
// @Summary Add home procedure
// @Tags owner
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param body body CreateHomeProcedureRequest true "Procedure"
// @Success 201 {object} HomeProcedureItem
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /owner/pets/{id}/home-procedures [post]
func (h *OwnerPortalHandler) CreateHomeProcedure(w http.ResponseWriter, r *http.Request) {
	pet, ok := h.ownerPet(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}
	var req CreateHomeProcedureRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "name is required"})
		return
	}
	if req.StartDate == "" {
		req.StartDate = todayGeorgia()
	}
	if !isISODate(req.StartDate) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "start_date must be YYYY-MM-DD"})
		return
	}
	hp := models.HomeProcedure{
		PetID: strconv.Itoa(int(pet.ID)), StartDate: req.StartDate, OwnerID: pet.UUID,
		PeriodCode: strconv.Itoa(req.Days - 1), Name: req.Name,
	}
	if err := h.db.Create(&hp).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to save"})
		return
	}
	writeJSON(w, http.StatusCreated, homeItem(hp, nil, todayGeorgia()))
}

// findHomeProcedure loads a home procedure of the owner's pet.
func (h *OwnerPortalHandler) findHomeProcedure(r *http.Request) (models.HomeProcedure, bool) {
	var hp models.HomeProcedure
	pet, ok := h.ownerPet(r)
	if !ok {
		return hp, false
	}
	err := h.db.Where("id = ? AND puuid = ?", chi.URLParam(r, "hpId"), strconv.Itoa(int(pet.ID))).First(&hp).Error
	return hp, err == nil
}

// MarkHomeProcedureDone records that the owner did the procedure now.
// @Summary Mark home procedure done
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param hpId path int true "Home procedure ID"
// @Success 201 {object} HomeProcedureItem
// @Failure 404 {object} ErrorResponse
// @Router /owner/pets/{id}/home-procedures/{hpId}/done [post]
func (h *OwnerPortalHandler) MarkHomeProcedureDone(w http.ResponseWriter, r *http.Request) {
	hp, ok := h.findHomeProcedure(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "home procedure not found"})
		return
	}
	mark := models.HomeProcedureDone{
		HomeProcedureID: strconv.Itoa(int(hp.ID)),
		DoneAt:          time.Now().In(georgia).Format("2006-01-02 15:04"),
	}
	if err := h.db.Create(&mark).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to save"})
		return
	}
	var marks []string
	h.db.Model(&models.HomeProcedureDone{}).Where("pruuid = ?", mark.HomeProcedureID).Order("id DESC").Pluck("gaketebuli", &marks)
	writeJSON(w, http.StatusCreated, homeItem(hp, marks, todayGeorgia()))
}

// DeleteHomeProcedure removes a home procedure and its completion log.
// @Summary Delete home procedure
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param hpId path int true "Home procedure ID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Router /owner/pets/{id}/home-procedures/{hpId} [delete]
func (h *OwnerPortalHandler) DeleteHomeProcedure(w http.ResponseWriter, r *http.Request) {
	hp, ok := h.findHomeProcedure(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "home procedure not found"})
		return
	}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("pruuid = ?", strconv.Itoa(int(hp.ID))).Delete(&models.HomeProcedureDone{}).Error; err != nil {
			return err
		}
		return tx.Delete(&hp).Error
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete"})
		return
	}
	writeJSON(w, http.StatusOK, MessageResponse{Message: "deleted"})
}
