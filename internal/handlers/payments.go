package handlers

import (
	"errors"
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
	UUID         string `json:"uuid" validate:"required"`                   // Pet ID
	Date         string `json:"date"`                                       // Payment date; defaults to today
	Method       string `json:"method" validate:"required,oneof=card cash"` // card or cash
	Amount       string `json:"amount" validate:"required"`                 // Amount in GEL
	Owner        string `json:"owner"`                                      // Owner personal ID
	ProcedureIDs []uint `json:"procedure_ids"`                              // Procedures to mark paid; empty = all of the pet's unpaid items that day
}

// PaymentResponse is the API response for a payment.
//
// `vet_id` and `owner` are deliberately absent: the paymethod table has
// no such columns (see models.Payment), so the previous fields could
// only ever have returned empty strings. A payment is attributed via
// the pet (`uuid`) and the clinic.
type PaymentResponse struct {
	ID     uint   `json:"id" validate:"required"`
	UUID   string `json:"uuid" validate:"required"`
	Date   string `json:"date" validate:"required"`
	Method string `json:"method" validate:"required"`
	Amount string `json:"amount" validate:"required"`
}

// DailySummary is the daily payment summary.
type DailySummary struct {
	Date  string `json:"date" validate:"required"`
	Card  string `json:"card" validate:"required"`
	Cash  string `json:"cash" validate:"required"`
	Total string `json:"total" validate:"required"`
}

func paymentToResponse(p models.Payment) PaymentResponse {
	return PaymentResponse{
		ID: p.ID, UUID: p.UUID, Date: p.Date, Method: p.Method,
		Amount: p.Amount,
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
	log := middleware.RequestLogger(r)

	var req RecordPaymentRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if claims.Zip == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "payments are taken by a clinic; this account has no clinic"})
		return
	}
	if req.Date == "" {
		req.Date = todayGeorgia()
	}
	if !isISODate(req.Date) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date must be YYYY-MM-DD"})
		return
	}
	if !isMoney(req.Amount) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "amount must be a number, e.g. 40 or 12.50"})
		return
	}

	// The API speaks "card"/"cash"; the column stores the Georgian
	// labels the legacy PHP frontend writes ("ბარათი" / "ნაღდი
	// ანგარიშწორება"). Translate on the way in so the clinic's existing
	// reports — which group by this column — see one consistent set of
	// values regardless of which frontend recorded the payment.
	payment := models.Payment{
		UUID:   req.UUID,
		Date:   req.Date,
		Method: models.NormalizePayMethod(req.Method),
		Amount: req.Amount,
		SK:     claims.Zip,
	}

	// vet/paystatus.php: insert the payment, then mark the pet's unpaid
	// items paid and stamp the method on them (`company`). One
	// transaction, so a payment can never exist without its items marked
	// or the other way round.
	//
	// Only unpaid items of this pet at this clinic can be marked. If a
	// listed item is already paid (a double submit, or two desks paying
	// the same visit) the whole payment is refused rather than charged
	// twice.
	var marked int64
	var replayed bool
	err := h.db.Transaction(func(tx *gorm.DB) error {
		prior, err := claimIdempotency(tx, r, "payments", req)
		if err != nil {
			return err
		}
		if prior != 0 {
			replayed = true
			return tx.First(&payment, prior).Error
		}
		q := tx.Model(&models.Procedure{}).
			Where("uuid = ? AND sk = ? AND phone = ?", req.UUID, claims.Zip, "0")
		if len(req.ProcedureIDs) > 0 {
			q = q.Where("id IN ?", req.ProcedureIDs)
		} else {
			q = q.Where("date = ?", req.Date)
		}
		res := q.Updates(map[string]interface{}{"phone": "1", "company": payment.Method})
		if res.Error != nil {
			return res.Error
		}
		marked = res.RowsAffected
		// Nothing left to pay (a double submit of "pay today's items"), or
		// a listed item already paid: refuse rather than record money
		// against nothing.
		if marked == 0 || (len(req.ProcedureIDs) > 0 && marked != int64(len(uniqueUints(req.ProcedureIDs)))) {
			return errAlreadyPaid
		}
		if err := tx.Create(&payment).Error; err != nil {
			return err
		}
		return recordIdempotency(tx, r, payment.ID)
	})
	if replayed && err == nil {
		writeJSON(w, http.StatusCreated, paymentToResponse(payment))
		return
	}
	if errors.Is(err, errIdempotencyMismatch) {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: err.Error()})
		return
	}
	if errors.Is(err, errAlreadyPaid) {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "nothing to pay: these items are already paid or do not belong to this pet"})
		return
	}
	if err != nil {
		log.Error("payment_failed", "error", err, "amount", req.Amount, "method", req.Method, "pet_id", req.UUID)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to record payment"})
		return
	}

	log.Info("payment_recorded", "payment_id", payment.ID, "amount", req.Amount, "method", req.Method, "items_marked", marked, "pet_id", req.UUID)

	writeJSON(w, http.StatusCreated, paymentToResponse(payment))
}

var errAlreadyPaid = errors.New("already paid")

func uniqueUints(in []uint) []uint {
	seen := make(map[uint]struct{}, len(in))
	out := in[:0:0]
	for _, v := range in {
		if _, dup := seen[v]; !dup {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
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
	date := r.URL.Query().Get("date")
	if date == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date is required"})
		return
	}

	clinic, ok := resolveClinic(w, r)
	if !ok {
		return
	}

	var summary DailySummary

	// Real column names on `paymethod` are (zip, date, uuid, sum, pay) —
	// this query previously used method/amount/sk and so returned zeros
	// for every clinic on every date. `pay` holds the Georgian method
	// labels; `sum` is TEXT and can contain blanks and stray characters
	// in legacy rows, so it is filtered to a numeric shape before the
	// cast rather than allowed to abort the whole aggregate.
	// NOTE the `{0,1}` instead of the usual `?` quantifier: GORM counts
	// every literal '?' in the SQL string as a bind placeholder, so a
	// regex containing one desynchronises the argument list and the
	// query fails with "unused argument".
	const numericSum = `sum ~ '^[0-9]+(\.[0-9]+){0,1}$'`
	h.db.Raw(
		`SELECT COALESCE(SUM(CASE WHEN pay = ? AND `+numericSum+` THEN sum::numeric ELSE 0 END)::text, '0') AS card,
		        COALESCE(SUM(CASE WHEN pay = ? AND `+numericSum+` THEN sum::numeric ELSE 0 END)::text, '0') AS cash,
		        COALESCE(SUM(CASE WHEN `+numericSum+` THEN sum::numeric ELSE 0 END)::text, '0') AS total
		 FROM paymethod WHERE zip = ? AND date = ?`,
		models.PayMethodCard, models.PayMethodCash, clinic, date,
	).Scan(&summary)

	// Set after the Scan: the aggregate selects no `date` column, so
	// scanning into the struct zeroes anything assigned beforehand.
	summary.Date = date

	writeJSON(w, http.StatusOK, summary)
}

// History returns payment history filtered by params.
// @Summary Payment history
// @Tags payments
// @Produce json
// @Security BearerAuth
// @Param clinic query string false "Clinic code"
// @Param date_from query string false "Start date (YYYY-MM-DD)"
// @Param date_to query string false "End date (YYYY-MM-DD)"
// @Param page query int false "Page number (default 1)"
// @Param pageSize query int false "Rows per page (default 50, max 200)"
// @Success 200 {object} PaginatedResponse
// @Failure 500 {object} ErrorResponse
// @Router /payments/history [get]
func (h *PaymentHandler) History(w http.ResponseWriter, r *http.Request) {
	query := h.db.Model(&models.Payment{})

	clinic, ok := resolveClinic(w, r)
	if !ok {
		return
	}
	// Raw column name: these Where clauses are SQL strings, so they must
	// use the real column (`zip`), not the Go field name.
	query = query.Where("zip = ?", clinic)

	// NOTE: the documented `vet_id` filter is not supported — paymethod
	// has no per-staff column (see models.Payment). It is accepted and
	// ignored rather than 400-ing, so existing callers don't break; the
	// swagger annotation above no longer advertises it.
	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		query = query.Where("date >= ?", dateFrom)
	}
	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		query = query.Where("date <= ?", dateTo)
	}

	// Paginated: 52,225 rows for the busiest clinic without a bound.
	page := ParsePageParams(r)

	var total int64
	query.Count(&total)

	var payments []models.Payment
	if err := page.Paginate(query).Order("date DESC, id DESC").Find(&payments).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch payment history"})
		return
	}

	items := make([]PaymentResponse, len(payments))
	for i, p := range payments {
		items[i] = paymentToResponse(p)
	}

	writeJSON(w, http.StatusOK, NewPaginatedResponse(items, total, page))
}
