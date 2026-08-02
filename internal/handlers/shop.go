package handlers

import (
	"encoding/json"
	"net/http"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// ShopHandler handles retail sales endpoints.
type ShopHandler struct {
	db *gorm.DB
}

// NewShopHandler creates a new ShopHandler.
func NewShopHandler(db *gorm.DB) *ShopHandler {
	return &ShopHandler{db: db}
}

// --- Request/Response types ---

// CreateShopRequest is the request body for adding a sale.
//
// `method` and `comment` mirror the two columns the legacy PHP sale
// form writes (`pay` and `coment`). Without them a sale recorded from
// the new frontend would be missing the payment method that the
// clinic's revenue reports group by, and the quantity note staff rely
// on ("8 ტაბლეტი"). Both are optional so existing clients keep working.
type CreateShopRequest struct {
	Name    string `json:"name" validate:"required"`
	Price   string `json:"price" validate:"required"`
	Date    string `json:"date" validate:"required"`
	Method  string `json:"method" validate:"omitempty,oneof=card cash"`
	Comment string `json:"comment"`
}

// ShopResponse is the API response for a sale.
//
// `vetname` is gone: the shop table has no staff column (see
// models.Shop), so the field could only ever return an empty string.
type ShopResponse struct {
	ID      uint   `json:"id" validate:"required"`
	Name    string `json:"name" validate:"required"`
	Price   string `json:"price" validate:"required"`
	Date    string `json:"date" validate:"required"`
	Method  string `json:"method"`
	Comment string `json:"comment"`
}

func shopToResponse(s models.Shop) ShopResponse {
	return ShopResponse{
		ID: s.ID, Name: s.Name, Price: s.Price, Date: s.Date,
		Method: s.Method, Comment: s.Comment,
	}
}

// --- Handlers ---

// List returns sales filtered by date range.
// @Summary List sales
// @Tags shop
// @Produce json
// @Security BearerAuth
// @Param clinic query string false "Clinic code"
// @Param date_from query string false "Start date (YYYY-MM-DD)"
// @Param date_to query string false "End date (YYYY-MM-DD)"
// @Param page query int false "Page number (default 1)"
// @Param pageSize query int false "Rows per page (default 50, max 200)"
// @Success 200 {object} PaginatedResponse
// @Failure 500 {object} ErrorResponse
// @Router /shop [get]
func (h *ShopHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	query := h.db.Model(&models.Shop{})

	clinic := r.URL.Query().Get("clinic")
	if clinic == "" {
		clinic = claims.Zip
	}
	// Raw column name — the shop table's clinic column is `zip`.
	query = query.Where("zip = ?", clinic)

	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		query = query.Where("date >= ?", dateFrom)
	}
	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		query = query.Where("date <= ?", dateTo)
	}

	// Paginated: 9,260 rows for the busiest clinic without a bound.
	page := ParsePageParams(r)

	var total int64
	query.Count(&total)

	var sales []models.Shop
	if err := page.Paginate(query).Order("date DESC, id DESC").Find(&sales).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch sales"})
		return
	}

	items := make([]ShopResponse, len(sales))
	for i, s := range sales {
		items[i] = shopToResponse(s)
	}

	writeJSON(w, http.StatusOK, NewPaginatedResponse(items, total, page))
}

// Create adds a new sale.
// @Summary Add sale
// @Tags shop
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreateShopRequest true "Sale data"
// @Success 201 {object} ShopResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /shop [post]
func (h *ShopHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var req CreateShopRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Same "card"/"cash" → Georgian translation as payments; the two
	// tables share the `pay` vocabulary and the clinic reports union
	// across them.
	sale := models.Shop{
		Name:    req.Name,
		Price:   req.Price,
		Date:    req.Date,
		SK:      claims.Zip,
		Method:  models.NormalizePayMethod(req.Method),
		Comment: req.Comment,
	}

	if err := h.db.Create(&sale).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create sale"})
		return
	}

	writeJSON(w, http.StatusCreated, shopToResponse(sale))
}

// Update edits an existing sale.
// @Summary Update sale
// @Tags shop
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Sale ID"
// @Param body body object true "Fields to update"
// @Success 200 {object} ShopResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /shop/{id} [put]
func (h *ShopHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var sale models.Shop
	if err := h.db.First(&sale, id).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "sale not found"})
		return
	}

	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	delete(updates, "id")
	delete(updates, "sk")

	if err := h.db.Model(&sale).Updates(updates).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update sale"})
		return
	}

	h.db.First(&sale, id)
	writeJSON(w, http.StatusOK, shopToResponse(sale))
}

// Delete removes a sale.
// @Summary Delete sale
// @Tags shop
// @Produce json
// @Security BearerAuth
// @Param id path int true "Sale ID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /shop/{id} [delete]
func (h *ShopHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var sale models.Shop
	if err := h.db.First(&sale, id).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "sale not found"})
		return
	}

	if err := h.db.Delete(&sale).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete sale"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "sale deleted"})
}
