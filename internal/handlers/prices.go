package handlers

import (
	"net/http"
	"strings"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// PriceHandler handles clinic price endpoints.
type PriceHandler struct {
	db *gorm.DB
}

// NewPriceHandler creates a new PriceHandler.
func NewPriceHandler(db *gorm.DB) *PriceHandler {
	return &PriceHandler{db: db}
}

// --- Request/Response types ---

// CreatePriceRequest is the request body for adding a price.
type CreatePriceRequest struct {
	Name  string `json:"name" validate:"required"`
	Price string `json:"price" validate:"required"`
}

// PriceResponse is the API response for a price entry.
type PriceResponse struct {
	ID    uint   `json:"id" validate:"required"`
	Name  string `json:"name" validate:"required"`
	Price string `json:"price" validate:"required"`
}

func priceToResponse(p models.Price) PriceResponse {
	return PriceResponse{ID: p.ID, Name: p.Name, Price: p.Price}
}

// --- Handlers ---

// List returns prices for a clinic.
// @Summary List prices
// @Tags prices
// @Produce json
// @Security BearerAuth
// @Param clinic query string false "Clinic code"
// @Success 200 {array} PriceResponse
// @Failure 500 {object} ErrorResponse
// @Router /prices [get]
func (h *PriceHandler) List(w http.ResponseWriter, r *http.Request) {
	clinic, ok := resolveClinic(w, r)
	if !ok {
		return
	}

	var prices []models.Price
	if err := h.db.Where("zip = ?", clinic).Order("name ASC").Find(&prices).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch prices"})
		return
	}

	items := make([]PriceResponse, len(prices))
	for i, p := range prices {
		items[i] = priceToResponse(p)
	}

	writeJSON(w, http.StatusOK, items)
}

// Create adds a new price entry.
// @Summary Add price
// @Tags prices
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreatePriceRequest true "Price data"
// @Success 201 {object} PriceResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /prices [post]
func (h *PriceHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var req CreatePriceRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	if msg := validatePrice(req.Name, req.Price); msg != "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: msg})
		return
	}
	if claims.Zip == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "this account has no clinic"})
		return
	}

	price := models.Price{
		Name:  req.Name,
		Price: req.Price,
		SK:    claims.Zip,
	}

	if err := h.db.Create(&price).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create price"})
		return
	}

	writeJSON(w, http.StatusCreated, priceToResponse(price))
}

// Update edits an existing price.
// @Summary Update price
// @Tags prices
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Price ID"
// @Param body body object true "Fields to update"
// @Success 200 {object} PriceResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /prices/{id} [put]
func (h *PriceHandler) Update(w http.ResponseWriter, r *http.Request) {
	var price models.Price
	if err := ownedByCaller(h.db, r, "zip").Where("id = ?", chi.URLParam(r, "id")).First(&price).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "price not found"})
		return
	}

	var req UpdatePriceRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if req.Name != nil {
		price.Name = *req.Name
	}
	if req.Price != nil {
		price.Price = *req.Price
	}
	if msg := validatePrice(price.Name, price.Price); msg != "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: msg})
		return
	}

	if err := h.db.Model(&price).Select("name", "price").Updates(&price).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update price"})
		return
	}
	writeJSON(w, http.StatusOK, priceToResponse(price))
}

// UpdatePriceRequest is a partial update; omitted fields keep their value.
type UpdatePriceRequest struct {
	Name  *string `json:"name"`
	Price *string `json:"price"`
}

// validatePrice checks a price-list entry. The price is free text on
// purpose: clinics list ranges such as "15/125" and "130+".
func validatePrice(name, price string) string {
	switch {
	case strings.TrimSpace(name) == "":
		return "name is required"
	case strings.TrimSpace(price) == "":
		return "price is required"
	case len(name) > 500 || len(price) > 100:
		return "name or price is too long"
	}
	return ""
}

// Delete removes a price entry.
// @Summary Delete price
// @Tags prices
// @Produce json
// @Security BearerAuth
// @Param id path int true "Price ID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /prices/{id} [delete]
func (h *PriceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var price models.Price
	if err := ownedByCaller(h.db, r, "zip").Where("id = ?", id).First(&price).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "price not found"})
		return
	}

	if err := h.db.Delete(&price).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete price"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "price deleted"})
}
