package handlers

import (
	"net/http"
	"strings"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// AllergyHandler handles allergy endpoints.
type AllergyHandler struct {
	db *gorm.DB
}

// NewAllergyHandler creates a new AllergyHandler.
func NewAllergyHandler(db *gorm.DB) *AllergyHandler {
	return &AllergyHandler{db: db}
}

// --- Request types ---

// CreateAllergyRequest is the request body for adding an allergy.
type CreateAllergyRequest struct {
	UUID    string `json:"uuid" validate:"required"` // Pet ID
	Name    string `json:"name" validate:"required"` // Allergy name
	Comment string `json:"comment"`                  // Free-text note
	Date    string `json:"date"`                     // Date recorded; defaults to today
}

// AllergyResponse is the API response for an allergy record.
type AllergyResponse struct {
	ID      uint   `json:"id" validate:"required"`
	UUID    string `json:"uuid" validate:"required"`
	Name    string `json:"name" validate:"required"`
	Comment string `json:"comment" validate:"required"`
	Date    string `json:"date" validate:"required"`
	// Mine is true when the caller's clinic recorded it (and may delete it).
	Mine bool `json:"mine" validate:"required"`
}

func allergyToResponse(a models.Allergy, clinic string) AllergyResponse {
	return AllergyResponse{
		ID: a.ID, UUID: a.UUID, Name: cleanText(a.Name), Comment: cleanText(a.Comment),
		Date: a.Date, Mine: clinic != "" && a.SK == clinic,
	}
}

// --- Handlers ---

// List returns allergies for a pet.
// @Summary List allergies
// @Tags allergies
// @Produce json
// @Security BearerAuth
// @Param pet_id query string true "Pet ID"
// @Success 200 {array} AllergyResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /allergies [get]
func (h *AllergyHandler) List(w http.ResponseWriter, r *http.Request) {
	petID := r.URL.Query().Get("pet_id")
	if petID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "pet_id is required"})
		return
	}

	// Allergies are safety information, so a clinic that may open the pet
	// sees every clinic's entries (and the owner's), as vet/veals.php did.
	if !canAccessPet(h.db, r, petID) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var allergies []models.Allergy
	if err := h.db.Where("uuid = ?", petID).Order("date DESC, id DESC").Find(&allergies).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch allergies"})
		return
	}

	zip := middleware.GetClaims(r).Zip
	items := make([]AllergyResponse, len(allergies))
	for i, a := range allergies {
		items[i] = allergyToResponse(a, zip)
	}

	writeJSON(w, http.StatusOK, items)
}

// Create adds a new allergy record.
// @Summary Add allergy
// @Tags allergies
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreateAllergyRequest true "Allergy data"
// @Success 201 {object} AllergyResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /allergies [post]
func (h *AllergyHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var req CreateAllergyRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "name is required"})
		return
	}
	if req.Date == "" {
		req.Date = todayGeorgia()
	}
	if !isISODate(req.Date) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date must be YYYY-MM-DD"})
		return
	}
	if claims.Zip == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "this account has no clinic"})
		return
	}
	if !canAccessPet(h.db, r, req.UUID) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	allergy := models.Allergy{
		UUID:    req.UUID,
		Name:    strings.TrimSpace(req.Name),
		Comment: req.Comment,
		Date:    req.Date,
		SK:      claims.Zip,
	}

	if err := h.db.Create(&allergy).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create allergy"})
		return
	}

	writeJSON(w, http.StatusCreated, allergyToResponse(allergy, claims.Zip))
}

// Delete removes an allergy record.
// @Summary Delete allergy
// @Tags allergies
// @Produce json
// @Security BearerAuth
// @Param id path int true "Allergy ID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /allergies/{id} [delete]
func (h *AllergyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Only the clinic that recorded an entry may remove it.
	var allergy models.Allergy
	if err := ownedByCaller(h.db, r, "sk").Where("id = ?", id).First(&allergy).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "allergy not found"})
		return
	}

	if err := h.db.Delete(&allergy).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete allergy"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "allergy deleted"})
}
