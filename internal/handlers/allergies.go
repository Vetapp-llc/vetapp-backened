package handlers

import (
	"net/http"

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
	UUID string `json:"uuid" validate:"required"` // Pet ID
	Name string `json:"name" validate:"required"` // Allergy name
	Date string `json:"date"`                     // Date recorded
}

// AllergyResponse is the API response for an allergy record.
type AllergyResponse struct {
	ID   uint   `json:"id"`
	UUID string `json:"uuid"`
	Name string `json:"name"`
	Date string `json:"date"`
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

	var allergies []models.Allergy
	if err := h.db.Where("uuid = ?", petID).Order("id DESC").Find(&allergies).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch allergies"})
		return
	}

	items := make([]AllergyResponse, len(allergies))
	for i, a := range allergies {
		items[i] = AllergyResponse{ID: a.ID, UUID: a.UUID, Name: a.Name, Date: a.Date}
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

	allergy := models.Allergy{
		UUID: req.UUID,
		Name: req.Name,
		Date: req.Date,
		SK:   claims.Zip,
	}

	if err := h.db.Create(&allergy).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create allergy"})
		return
	}

	writeJSON(w, http.StatusCreated, AllergyResponse{
		ID: allergy.ID, UUID: allergy.UUID, Name: allergy.Name, Date: allergy.Date,
	})
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

	var allergy models.Allergy
	if err := h.db.First(&allergy, id).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "allergy not found"})
		return
	}

	if err := h.db.Delete(&allergy).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete allergy"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "allergy deleted"})
}
