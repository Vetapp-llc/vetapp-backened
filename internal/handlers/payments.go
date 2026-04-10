package handlers

import (
	"net/http"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"gorm.io/gorm"
)

// PaymentHandler handles payment endpoints.
type PaymentHandler struct {
	db *gorm.DB
}

// NewPaymentHandler creates a new PaymentHandler.
func NewPaymentHandler(db *gorm.DB) *PaymentHandler {
	return &PaymentHandler{db: db}
}

// --- Request/Response types ---

// RecordPaymentRequest is the request body for recording a payment.
type RecordPaymentRequest struct {
	UUID         string   `json:"uuid" validate:"required"`          // Pet ID
	Date         string   `json:"date" validate:"required"`          // Payment date
	Method       string   `json:"method" validate:"required,oneof=card cash"` // card or cash
	Amount       string   `json:"amount" validate:"required"`        // Amount in GEL
	Owner        string   `json:"owner"`                             // Owner personal ID
	ProcedureIDs []uint   `json:"procedure_ids"`                     // Procedure IDs to mark as paid
}

// PaymentResponse is the API response for a payment.
type PaymentResponse struct {
	ID     uint   `json:"id"`
	UUID   string `json:"uuid"`
	Date   string `json:"date"`
	Method string `json:"method"`
	Amount string `json:"amount"`
	VetID  string `json:"vet_id"`
	Owner  string `json:"owner"`
}

// DailySummary is the daily payment summary.
type DailySummary struct {
	Date  string `json:"date"`
	Card  string `json:"card"`
	Cash  string `json:"cash"`
	Total string `json:"total"`
}

func paymentToResponse(p models.Payment) PaymentResponse {
	return PaymentResponse{
		ID: p.ID, UUID: p.UUID, Date: p.Date, Method: p.Method,
		Amount: p.Amount, VetID: p.VetID, Owner: p.Owner,
	}
}

// --- Handlers ---

// Record records a payment and optionally marks procedures as paid.
// @Summary Record payment
// @Tags payments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body RecordPaymentRequest true "Payment data"
// @Success 201 {object} PaymentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /payments/record [post]
func (h *PaymentHandler) Record(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var req RecordPaymentRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	payment := models.Payment{
		UUID:   req.UUID,
		Date:   req.Date,
		Method: req.Method,
		Amount: req.Amount,
		SK:     claims.Zip,
		VetID:  formatUint(claims.UserID),
		Owner:  req.Owner,
	}

	if err := h.db.Create(&payment).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to record payment"})
		return
	}

	// Mark procedures as paid (phone="1" means paid in the vaccination table)
	if len(req.ProcedureIDs) > 0 {
		h.db.Model(&models.Procedure{}).Where("id IN ?", req.ProcedureIDs).Update("phone", "1")
	}

	writeJSON(w, http.StatusCreated, paymentToResponse(payment))
}

// Daily returns the daily payment summary.
// @Summary Daily payment summary
// @Tags payments
// @Produce json
// @Security BearerAuth
// @Param clinic query string false "Clinic code"
// @Param date query string true "Date (YYYY-MM-DD)"
// @Success 200 {object} DailySummary
// @Failure 400 {object} ErrorResponse
// @Router /payments/daily [get]
func (h *PaymentHandler) Daily(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	date := r.URL.Query().Get("date")
	if date == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date is required"})
		return
	}

	clinic := r.URL.Query().Get("clinic")
	if clinic == "" {
		clinic = claims.Zip
	}

	var summary DailySummary
	summary.Date = date

	h.db.Raw(
		`SELECT COALESCE(SUM(CASE WHEN method='card' THEN amount::numeric ELSE 0 END)::text, '0') AS card,
		        COALESCE(SUM(CASE WHEN method='cash' THEN amount::numeric ELSE 0 END)::text, '0') AS cash,
		        COALESCE(SUM(amount::numeric)::text, '0') AS total
		 FROM paymethod WHERE sk = ? AND date = ?`, clinic, date,
	).Scan(&summary)

	writeJSON(w, http.StatusOK, summary)
}

// History returns payment history filtered by params.
// @Summary Payment history
// @Tags payments
// @Produce json
// @Security BearerAuth
// @Param clinic query string false "Clinic code"
// @Param vet_id query string false "Vet member ID"
// @Param date_from query string false "Start date (YYYY-MM-DD)"
// @Param date_to query string false "End date (YYYY-MM-DD)"
// @Success 200 {array} PaymentResponse
// @Failure 500 {object} ErrorResponse
// @Router /payments/history [get]
func (h *PaymentHandler) History(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	query := h.db.Model(&models.Payment{})

	clinic := r.URL.Query().Get("clinic")
	if clinic == "" {
		clinic = claims.Zip
	}
	query = query.Where("sk = ?", clinic)

	if vetID := r.URL.Query().Get("vet_id"); vetID != "" {
		query = query.Where("vet_id = ?", vetID)
	}
	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		query = query.Where("date >= ?", dateFrom)
	}
	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		query = query.Where("date <= ?", dateTo)
	}

	var payments []models.Payment
	if err := query.Order("date DESC, id DESC").Find(&payments).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch payment history"})
		return
	}

	items := make([]PaymentResponse, len(payments))
	for i, p := range payments {
		items[i] = paymentToResponse(p)
	}

	writeJSON(w, http.StatusOK, items)
}
