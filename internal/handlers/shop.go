package handlers

import (
	"net/http"
	"strings"

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
	clinic, ok := resolveClinic(w, r)
	if !ok {
		return
	}
	// Raw column name — the shop table's clinic column is `zip`.
	query := h.db.Model(&models.Shop{}).Where("zip = ?", clinic)
	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		query = query.Where("date >= ?", dateFrom)
	}
	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		query = query.Where("date <= ?", dateTo)
	}

	// Card / cash / grand totals for the whole filtered range — the
	// footer of vet/shop.php. Computed over every matching row, not just
	// the page being returned.
	var totals SalesTotals
	query.Session(&gorm.Session{}).Select(
		`COALESCE(SUM(CASE WHEN pay = ? AND `+numericGuard("price")+` THEN price::numeric ELSE 0 END), 0)::text AS card,
		 COALESCE(SUM(CASE WHEN pay = ? AND `+numericGuard("price")+` THEN price::numeric ELSE 0 END), 0)::text AS cash,
		 COALESCE(SUM(CASE WHEN `+numericGuard("price")+` THEN price::numeric ELSE 0 END), 0)::text AS total,
		 COUNT(*) AS count`,
		models.PayMethodCard, models.PayMethodCash,
	).Scan(&totals)

	// Paginated: 9,260 rows for the busiest clinic without a bound.
	page := ParsePageParams(r)

	var sales []models.Shop
	if err := page.Paginate(query.Session(&gorm.Session{})).Order("date DESC, id DESC").Find(&sales).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch sales"})
		return
	}

	items := make([]ShopResponse, len(sales))
	for i, s := range sales {
		items[i] = shopToResponse(s)
	}

	writeJSON(w, http.StatusOK, ShopListResponse{
		PaginatedResponse: NewPaginatedResponse(items, totals.Count, page),
		Totals:            totals,
	})
}

// SalesTotals sums a range of sales by payment method.
type SalesTotals struct {
	Card  string `json:"card" validate:"required"`
	Cash  string `json:"cash" validate:"required"`
	Total string `json:"total" validate:"required"`
	Count int64  `json:"-"`
}

// ShopListResponse is a page of sales plus totals for the whole range.
type ShopListResponse struct {
	PaginatedResponse
	Totals SalesTotals `json:"totals" validate:"required"`
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

	if msg := validateSale(req.Name, req.Price, req.Date); msg != "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: msg})
		return
	}
	if claims.Zip == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "sales are recorded by a clinic; this account has no clinic"})
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
	var sale models.Shop
	if err := ownedByCaller(h.db, r, "zip").Where("id = ?", chi.URLParam(r, "id")).First(&sale).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "sale not found"})
		return
	}

	var req UpdateShopRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if req.Name != nil {
		sale.Name = *req.Name
	}
	if req.Price != nil {
		sale.Price = *req.Price
	}
	if req.Date != nil {
		sale.Date = *req.Date
	}
	if req.Method != nil {
		sale.Method = models.NormalizePayMethod(*req.Method)
	}
	if req.Comment != nil {
		sale.Comment = *req.Comment
	}
	if msg := validateSale(sale.Name, sale.Price, sale.Date); msg != "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: msg})
		return
	}

	if err := h.db.Model(&sale).Select("name", "price", "date", "pay", "coment").Updates(&sale).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update sale"})
		return
	}
	writeJSON(w, http.StatusOK, shopToResponse(sale))
}

// UpdateShopRequest is a partial update of a sale; omitted fields keep
// their value. The clinic is never editable.
type UpdateShopRequest struct {
	Name    *string `json:"name"`
	Price   *string `json:"price"`
	Date    *string `json:"date"`
	Method  *string `json:"method" validate:"omitempty,oneof=card cash"`
	Comment *string `json:"comment"`
}

// validateSale returns a user-facing error, or "" when the sale is valid.
func validateSale(name, price, date string) string {
	switch {
	case strings.TrimSpace(name) == "":
		return "name is required"
	case !isMoney(price):
		return "price must be a number, e.g. 12.50"
	case !isISODate(date):
		return "date must be YYYY-MM-DD"
	}
	return ""
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
	if err := ownedByCaller(h.db, r, "zip").Where("id = ?", id).First(&sale).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "sale not found"})
		return
	}

	if err := h.db.Delete(&sale).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete sale"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "sale deleted"})
}
