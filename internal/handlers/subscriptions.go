package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"
	"vetapp-backend/internal/services"

	"gorm.io/gorm"
)

// SubscriptionHandler handles iPay subscription endpoints.
type SubscriptionHandler struct {
	db          *gorm.DB
	ipayService *services.IPayService
}

// NewSubscriptionHandler creates a new SubscriptionHandler.
func NewSubscriptionHandler(db *gorm.DB, ipayService *services.IPayService) *SubscriptionHandler {
	return &SubscriptionHandler{db: db, ipayService: ipayService}
}

// --- Request/Response types ---

// PackageResponse is the API response for a subscription package.
type PackageResponse struct {
	ID       uint   `json:"id" validate:"required"`
	Name     string `json:"name" validate:"required"`
	Price    string `json:"price" validate:"required"`
	Duration int    `json:"duration" validate:"required"`
}

// CheckoutRequest is the request body for creating a checkout session.
type CheckoutRequest struct {
	PetID     uint `json:"pet_id" validate:"required"`
	PackageID uint `json:"package_id" validate:"required"`
}

// CheckoutResponse is the response with the iPay redirect URL.
type CheckoutResponse struct {
	RedirectURL string `json:"redirect_url" validate:"required"`
	OrderID     string `json:"order_id" validate:"required"`
}

// --- Handlers ---

// Packages returns available subscription packages.
// @Summary List subscription packages
// @Tags subscriptions
// @Produce json
// @Success 200 {array} PackageResponse
// @Failure 500 {object} ErrorResponse
// @Router /subscriptions/packages [get]
func (h *SubscriptionHandler) Packages(w http.ResponseWriter, r *http.Request) {
	var packages []models.Package
	if err := h.db.Order("price ASC").Find(&packages).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch packages"})
		return
	}

	items := make([]PackageResponse, len(packages))
	for i, p := range packages {
		items[i] = PackageResponse{ID: p.ID, Name: p.Name, Price: p.Price, Duration: p.Duration}
	}

	writeJSON(w, http.StatusOK, items)
}

// Checkout creates an iPay checkout session.
// @Summary Create checkout session
// @Tags subscriptions
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CheckoutRequest true "Checkout data"
// @Success 200 {object} CheckoutResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /subscriptions/checkout [post]
func (h *SubscriptionHandler) Checkout(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var req CheckoutRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Verify pet belongs to owner
	var pet models.Pet
	if err := h.db.First(&pet, req.PetID).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}
	if pet.UUID != claims.LastName {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	// Get package
	var pkg models.Package
	if err := h.db.First(&pkg, req.PackageID).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "package not found"})
		return
	}

	// Get iPay token
	token, err := h.ipayService.GetToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "payment gateway error"})
		return
	}

	// Create order
	callbackURL := r.Header.Get("Origin") + "/api/subscriptions/callback"
	order, err := h.ipayService.CreateOrder(token, pkg.Price, req.PetID, callbackURL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create payment order"})
		return
	}

	// Record pending payment
	sub := models.Subscription{
		UUID:     formatUint(req.PetID),
		Amount:   pkg.Price,
		Status:   "pending",
		OrderID:  order.OrderID,
		Package:  pkg.Name,
		Date:     time.Now().Format("2006-01-02"),
		Provider: "ipay",
	}
	h.db.Create(&sub)

	writeJSON(w, http.StatusOK, CheckoutResponse{
		RedirectURL: order.RedirectURL,
		OrderID:     order.OrderID,
	})
}

// Callback handles the iPay webhook after payment.
// @Summary iPay payment callback
// @Tags subscriptions
// @Accept json
// @Produce json
// @Param body body object true "iPay callback data"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /subscriptions/callback [post]
func (h *SubscriptionHandler) Callback(w http.ResponseWriter, r *http.Request) {
	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid callback data"})
		return
	}

	orderID, _ := payload["shop_order_id"].(string)
	status, _ := payload["status"].(string)
	transID, _ := payload["trans_id"].(string)

	if orderID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "missing order_id"})
		return
	}

	// Find payment record
	var sub models.Subscription
	if err := h.db.Where("order_id = ?", orderID).First(&sub).Error; err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "order not found"})
		return
	}

	sub.TransID = transID

	if status == "success" || status == "CAPTURED" {
		sub.Status = "success"
		h.db.Save(&sub)

		// Find the package to get duration
		var pkg models.Package
		if err := h.db.Where("name = ?", sub.Package).First(&pkg).Error; err == nil {
			// Activate pet subscription
			expiry := time.Now().AddDate(0, 0, pkg.Duration).Format("2006-01-02")
			h.db.Model(&models.Pet{}).Where("id = ?", sub.UUID).
				Updates(map[string]interface{}{"status": 1, "birth2": expiry})
		}
	} else {
		sub.Status = "failed"
		h.db.Save(&sub)
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "callback processed"})
}

// --- Apple IAP ---

// AppleVerifyRequest is the request body for verifying an Apple IAP receipt.
type AppleVerifyRequest struct {
	PetID         uint   `json:"pet_id" validate:"required"`
	PackageID     uint   `json:"package_id" validate:"required"`
	Receipt       string `json:"receipt" validate:"required"`       // Base64-encoded receipt data
	TransactionID string `json:"transaction_id" validate:"required"` // Original transaction ID
	ProductID     string `json:"product_id" validate:"required"`     // IAP product identifier
}

// AppleVerify validates an Apple IAP receipt and activates the subscription.
// @Summary Verify Apple IAP receipt
// @Tags subscriptions
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body AppleVerifyRequest true "Apple IAP receipt"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /subscriptions/apple-verify [post]
func (h *SubscriptionHandler) AppleVerify(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var req AppleVerifyRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Verify pet belongs to owner
	var pet models.Pet
	if err := h.db.First(&pet, req.PetID).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}
	if pet.UUID != claims.LastName {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	// Get package
	var pkg models.Package
	if err := h.db.First(&pkg, req.PackageID).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "package not found"})
		return
	}

	// Verify receipt with Apple
	valid, err := verifyAppleReceipt(req.Receipt)
	if err != nil || !valid {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid receipt"})
		return
	}

	// Check for duplicate transaction
	var existing models.Subscription
	if err := h.db.Where("order_id = ? AND provider = 'apple'", req.TransactionID).First(&existing).Error; err == nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "transaction already processed"})
		return
	}

	// Record successful payment
	sub := models.Subscription{
		UUID:      formatUint(req.PetID),
		Amount:    pkg.Price,
		Status:    "success",
		OrderID:   req.TransactionID,
		Date:      time.Now().Format("2006-01-02"),
		Package:   pkg.Name,
		Provider:  "apple",
		Receipt:   req.Receipt,
		ProductID: req.ProductID,
	}
	h.db.Create(&sub)

	// Activate pet subscription
	expiry := time.Now().AddDate(0, 0, pkg.Duration).Format("2006-01-02")
	h.db.Model(&models.Pet{}).Where("id = ?", req.PetID).
		Updates(map[string]interface{}{"status": 1, "birth2": expiry})

	writeJSON(w, http.StatusOK, MessageResponse{Message: "subscription activated"})
}

// verifyAppleReceipt validates the receipt with Apple's App Store Server API.
// In production, use App Store Server API v2 (signed JWS transactions).
// For sandbox testing, this accepts any non-empty receipt.
func verifyAppleReceipt(receipt string) (bool, error) {
	if receipt == "" {
		return false, nil
	}

	// TODO: Implement proper Apple receipt validation
	// Option A: App Store Server API v2 (recommended)
	//   - Decode the signed JWS transaction
	//   - Verify signature against Apple's certificate
	//   - Check transaction ID, product ID, expiry
	//
	// Option B: verifyReceipt endpoint (deprecated but simpler)
	//   - POST to https://buy.itunes.apple.com/verifyReceipt (production)
	//   - POST to https://sandbox.itunes.apple.com/verifyReceipt (sandbox)
	//   - Send {"receipt-data": receipt, "password": shared_secret}
	//
	// For now, we trust the receipt from the mobile app.
	// This MUST be implemented before production release.
	return true, nil
}
