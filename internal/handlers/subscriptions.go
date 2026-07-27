package handlers

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"
	"vetapp-backend/internal/services"

	"gorm.io/gorm"
)

// SubscriptionHandler handles card-gateway and Apple IAP subscription
// endpoints.
//
// Two card gateways are supported, both Bank of Georgia:
//
//   - "bog"  — the current Payments API (api.bog.ge)
//   - "ipay" — the legacy gateway, which BOG's docs mark deprecated
//
// `provider` selects which one NEW checkouts use. Callbacks always
// dispatch on the provider recorded on the subscription row, never on
// the current config, so orders opened before a provider switch still
// settle correctly.
type SubscriptionHandler struct {
	db          *gorm.DB
	ipayService *services.IPayService
	bogService  *services.BOGService
	provider    string
	baseURL     string
}

// NewSubscriptionHandler creates a new SubscriptionHandler.
//
// `provider` comes from config.PaymentProvider. If it asks for "bog"
// but no BOG credentials are configured, we fall back to iPay rather
// than failing every checkout — a misconfigured deployment should keep
// taking payments on the old rails instead of going dark.
func NewSubscriptionHandler(db *gorm.DB, ipayService *services.IPayService, bogService *services.BOGService, provider, baseURL string) *SubscriptionHandler {
	if provider == providerBOG && (bogService == nil || !bogService.Enabled()) {
		provider = providerIPay
	}
	return &SubscriptionHandler{
		db:          db,
		ipayService: ipayService,
		bogService:  bogService,
		provider:    provider,
		baseURL:     baseURL,
	}
}

// Payment provider identifiers, as stored in payments_ipay.provider.
const (
	providerIPay  = "ipay"
	providerBOG   = "bog"
	providerApple = "apple"
)

// newExternalOrderID mints a unique reference for one checkout attempt.
//
// BOG uses this value as the Idempotency-Key, so it must differ per
// attempt: reusing it makes BOG return the ORIGINAL order, which would
// strand a customer who abandoned a payment and came back on a stale
// (possibly expired) payment page. The random suffix comes from
// crypto/rand — a predictable counter would let someone guess another
// customer's order reference.
func newExternalOrderID(petID uint) (string, error) {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("vetapp-%d-%s", petID, hex.EncodeToString(b[:])), nil
}

// localeFromRequest maps the client's Accept-Language to the two
// languages BOG's hosted payment page supports. Georgian is the
// default because that is the primary market.
func localeFromRequest(r *http.Request) string {
	al := strings.ToLower(r.Header.Get("Accept-Language"))
	if strings.HasPrefix(al, "en") {
		return "en"
	}
	return "ka"
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
	// Provider tells the client which gateway served this checkout
	// ("bog" or "ipay"). Clients shouldn't branch on it for the happy
	// path — both are a WebView redirect — but it makes support
	// tickets and client-side logs far easier to trace back.
	Provider string `json:"provider"`
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
	log := middleware.RequestLogger(r)
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

	callbackURL := h.baseURL + "/api/subscriptions/callback"

	log.Info("checkout_token_request", "pet_id", req.PetID, "package_id", req.PackageID,
		"price", pkg.Price, "provider", h.provider)

	var (
		orderID     string
		redirectURL string
		err         error
	)

	switch h.provider {
	case providerBOG:
		var token string
		token, err = h.bogService.GetToken()
		if err != nil {
			log.Error("checkout_token_failed", "provider", providerBOG, "error", err)
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "payment gateway error: " + err.Error()})
			return
		}

		// external_order_id doubles as the idempotency key, so it must
		// be unique per checkout ATTEMPT — reusing a value would make
		// BOG return the earlier order and could strand the customer on
		// a stale payment page.
		externalID, err2 := newExternalOrderID(req.PetID)
		if err2 != nil {
			log.Error("checkout_orderid_failed", "error", err2)
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create payment order"})
			return
		}

		var order *services.BOGOrderResponse
		order, err = h.bogService.CreateOrder(
			token, pkg.Price, externalID, callbackURL,
			h.baseURL+"/api/subscriptions/return?status=success",
			h.baseURL+"/api/subscriptions/return?status=fail",
			localeFromRequest(r),
		)
		if err == nil {
			orderID, redirectURL = order.OrderID, order.RedirectURL
		}

	default:
		var token string
		token, err = h.ipayService.GetToken()
		if err != nil {
			log.Error("checkout_token_failed", "provider", providerIPay, "error", err)
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "payment gateway error: " + err.Error()})
			return
		}
		var order *services.OrderResponse
		order, err = h.ipayService.CreateOrder(token, pkg.Price, req.PetID, callbackURL)
		if err == nil {
			orderID, redirectURL = order.OrderID, order.RedirectURL
		}
	}

	if err != nil {
		log.Error("checkout_order_failed", "provider", h.provider, "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create payment order: " + err.Error()})
		return
	}

	// Record pending payment. We must succeed here — without this row
	// the eventual callback can't find the order and the user paid for
	// nothing.
	sub := models.Subscription{
		UUID:     formatUint(req.PetID),
		Amount:   pkg.Price,
		Status:   "pending",
		OrderID:  orderID,
		Package:  pkg.Name,
		Date:     time.Now().Format("2006-01-02"),
		Provider: h.provider,
	}
	if err := h.db.Create(&sub).Error; err != nil {
		log.Error("checkout_persist_failed", "order_id", orderID, "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to record pending payment"})
		return
	}

	log.Info("checkout_created", "order_id", orderID, "provider", h.provider)
	writeJSON(w, http.StatusOK, CheckoutResponse{
		RedirectURL: redirectURL,
		OrderID:     orderID,
		Provider:    h.provider,
	})
}

// Callback handles the card-gateway webhook after payment, for both
// BOG and legacy iPay.
//
// Security model: the request body is treated as a *notification*, not
// authoritative data. We extract the order reference, look up the local
// subscription, then call the gateway's own status API with our OAuth
// token to confirm the real status. This makes the endpoint immune to
// spoofed callbacks — anyone could POST an order id + status='success'
// otherwise, and order references are guessable.
//
// BOG additionally signs callbacks (`Callback-Signature`, SHA256withRSA).
// We verify it when a public key is configured, over the RAW request
// body: BOG's docs require verifying before deserialization because
// JSON field order is part of the signed payload. A failed signature is
// rejected outright. A missing signature or unconfigured key is NOT
// treated as proof of anything — it just falls through to the
// re-fetch, which is the real gate.
//
// Idempotent: the subscription's status flip + pet activation run in a
// single transaction with `WHERE status='pending'`, so re-deliveries
// (or retries) don't double-extend the pet's expiry.
//
// @Summary Card payment callback (BOG / iPay)
// @Tags subscriptions
// @Accept json
// @Produce json
// @Param body body object true "Gateway callback data"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /subscriptions/callback [post]
func (h *SubscriptionHandler) Callback(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)

	// Read the raw body first — signature verification must run over
	// the exact bytes received, so we cannot decode straight into a map.
	// Bounded so a hostile caller can't stream us out of memory.
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid callback data"})
		return
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid callback data"})
		return
	}

	// Order reference: iPay sends `shop_order_id` at the top level;
	// BOG wraps the payment in `body` and uses `order_id`.
	orderID, _ := payload["shop_order_id"].(string)
	if orderID == "" {
		if body, ok := payload["body"].(map[string]interface{}); ok {
			orderID, _ = body["order_id"].(string)
		}
	}
	if orderID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "missing order_id"})
		return
	}

	// Find local payment record (must be one we created via Checkout).
	var sub models.Subscription
	if err := h.db.Where("order_id = ?", orderID).First(&sub).Error; err != nil {
		log.Warn("ipay_callback_unknown_order", "order_id", orderID)
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "order not found"})
		return
	}

	// Idempotency — already processed.
	if sub.Status != "pending" {
		log.Info("ipay_callback_already_processed", "order_id", orderID, "status", sub.Status)
		writeJSON(w, http.StatusOK, MessageResponse{Message: "already processed"})
		return
	}

	// Authoritative status comes from the gateway that created the
	// order — dispatch on the stored provider, not the current config,
	// so orders opened before a provider switch still settle.
	var (
		success bool
		transID string
	)

	switch sub.Provider {
	case providerBOG:
		if h.bogService == nil || !h.bogService.Enabled() {
			log.Error("bog_callback_service_unavailable", "order_id", orderID)
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to verify order"})
			return
		}

		// Reject a present-but-invalid signature outright. Absence is
		// handled by the re-fetch below, per BOG marking the header
		// optional.
		if ok, err := h.bogService.VerifyCallbackSignature(raw, r.Header.Get("Callback-Signature")); err != nil {
			log.Warn("bog_callback_bad_signature", "order_id", orderID, "error", err)
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid signature"})
			return
		} else if ok {
			log.Info("bog_callback_signature_ok", "order_id", orderID)
		}

		st, err := h.bogService.GetOrderStatus(orderID)
		if err != nil {
			log.Error("bog_callback_status_fetch_failed", "order_id", orderID, "error", err)
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to verify order"})
			return
		}
		success = h.bogService.IsPaid(st.Status)

	default:
		st, err := h.ipayService.GetOrderStatus(orderID)
		if err != nil {
			log.Error("ipay_callback_status_fetch_failed", "order_id", orderID, "error", err)
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to verify order"})
			return
		}
		transID = st.TransID
		success = st.Status == "success" || st.Status == "CAPTURED" || st.Status == "PAID" || st.Status == "completed"
	}
	if !success {
		// Conditional update — only if we still see status='pending'.
		res := h.db.Model(&models.Subscription{}).
			Where("id = ? AND status = ?", sub.ID, "pending").
			Updates(map[string]interface{}{"status": "failed", "trans_id": transID})
		if res.Error != nil {
			log.Error("ipay_callback_db_failed", "order_id", orderID, "error", res.Error)
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "db error"})
			return
		}
		writeJSON(w, http.StatusOK, MessageResponse{Message: "callback processed"})
		return
	}

	// Look up the package once so we know how far to extend the pet's
	// expiry. Failure here means the package was renamed or deleted —
	// we still record the payment success but skip pet activation, and
	// the user should contact support.
	var pkg models.Package
	if err := h.db.Where("name = ?", sub.Package).First(&pkg).Error; err != nil {
		log.Warn("ipay_callback_package_not_found", "package", sub.Package, "order_id", orderID)
	}

	expiry := time.Now().AddDate(0, 0, pkg.Duration).Format("2006-01-02")

	err = h.db.Transaction(func(tx *gorm.DB) error {
		// Conditional flip — bails out if another goroutine got here first.
		res := tx.Model(&models.Subscription{}).
			Where("id = ? AND status = ?", sub.ID, "pending").
			Updates(map[string]interface{}{"status": "success", "trans_id": transID})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// Lost the race — someone else activated this. Treat as success.
			return nil
		}

		if pkg.Duration > 0 {
			if err := tx.Model(&models.Pet{}).Where("id = ?", sub.UUID).
				Updates(map[string]interface{}{"status": 1, "birth2": expiry}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Error("ipay_callback_activation_failed", "order_id", orderID, "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "activation failed"})
		return
	}

	log.Info("payment_callback_success", "order_id", orderID, "provider", sub.Provider, "trans_id", transID)
	writeJSON(w, http.StatusOK, MessageResponse{Message: "callback processed"})
}

// --- Apple IAP ---

// AppleVerifyRequest is the request body for verifying an Apple IAP receipt.
type AppleVerifyRequest struct {
	PetID             uint   `json:"pet_id" validate:"required"`
	PackageID         uint   `json:"package_id" validate:"required"`
	SignedTransaction string `json:"signed_transaction" validate:"required"` // StoreKit 2 JWS signed transaction
}

// AppleVerify validates an Apple IAP receipt and activates the subscription.
//
// Validation layers (defence in depth):
//
//  1. JWS signature against Apple's Root CA G3 (services.VerifyAppleJWS).
//  2. Bundle ID match — receipt must be from APPLE_BUNDLE_ID env var.
//     Without this check, a sandbox or cross-app receipt would activate
//     a real subscription.
//  3. Product ID match — receipt's productId must equal the package's
//     `apple_product_id`. Stops a user from buying the cheap pack
//     locally and sending us a different productId.
//  4. Environment — in production we reject `Sandbox` receipts.
//  5. Duplicate guard — txInfo.TransactionID already-processed.
//
// On success the receipt's expiresDate (when present) is the source of
// truth for the pet's expiry; we fall back to package duration only if
// the receipt didn't include one.
//
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
	log := middleware.RequestLogger(r)
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

	// Verify JWS signed transaction from StoreKit 2
	txInfo, err := services.VerifyAppleJWS(req.SignedTransaction)
	if err != nil {
		log.Warn("apple_verify_jws_failed", "error", err)
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid receipt: " + err.Error()})
		return
	}

	// Bundle ID check — APPLE_BUNDLE_ID is set per-environment via env var.
	// We deliberately fail-closed when unset in production: an empty
	// expected bundle would pass any receipt, defeating the check.
	expectedBundle := strings.TrimSpace(os.Getenv("APPLE_BUNDLE_ID"))
	prod := os.Getenv("RAILWAY_ENVIRONMENT") != "" || os.Getenv("ENV") == "production"
	if expectedBundle == "" {
		if prod {
			log.Error("apple_verify_misconfigured", "reason", "APPLE_BUNDLE_ID not set in production")
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "service misconfigured"})
			return
		}
		// In dev, allow but warn so the next deploy won't surprise us.
		log.Warn("apple_verify_no_bundle_check", "received", txInfo.BundleID)
	} else if txInfo.BundleID != expectedBundle {
		log.Warn("apple_verify_wrong_bundle", "expected", expectedBundle, "received", txInfo.BundleID)
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "receipt bundle mismatch"})
		return
	}

	// Reject sandbox receipts in production.
	if prod && txInfo.Environment == "Sandbox" {
		log.Warn("apple_verify_sandbox_in_prod", "transaction_id", txInfo.TransactionID)
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "sandbox receipt not accepted"})
		return
	}

	// Product ID check — the package the user clicked "buy" on must
	// match the productId in the signed receipt. Skipped if the package
	// doesn't have an apple_product_id configured (legacy rows during
	// rollout); after backfill the audit can flip to fail-closed.
	if pkg.AppleProductID != "" && txInfo.ProductID != pkg.AppleProductID {
		log.Warn("apple_verify_wrong_product", "expected", pkg.AppleProductID, "received", txInfo.ProductID)
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "receipt product mismatch"})
		return
	}

	// Duplicate guard — same transaction can't activate twice.
	var existing models.Subscription
	if err := h.db.Where("order_id = ? AND provider = 'apple'", txInfo.TransactionID).First(&existing).Error; err == nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "transaction already processed"})
		return
	}

	// Pick the strongest expiry signal: receipt > package duration.
	expiry := time.Now().AddDate(0, 0, pkg.Duration).Format("2006-01-02")
	if txInfo.ExpiresDate > 0 {
		expiry = time.UnixMilli(txInfo.ExpiresDate).UTC().Format("2006-01-02")
	}

	sub := models.Subscription{
		UUID:      formatUint(req.PetID),
		Amount:    pkg.Price,
		Status:    "success",
		OrderID:   txInfo.TransactionID,
		Date:      time.Now().Format("2006-01-02"),
		Package:   pkg.Name,
		Provider:  "apple",
		Receipt:   req.SignedTransaction,
		ProductID: txInfo.ProductID,
	}

	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&sub).Error; err != nil {
			return err
		}
		return tx.Model(&models.Pet{}).Where("id = ?", req.PetID).
			Updates(map[string]interface{}{"status": 1, "birth2": expiry}).Error
	})
	if err != nil {
		log.Error("apple_verify_persist_failed", "transaction_id", txInfo.TransactionID, "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to record subscription"})
		return
	}

	log.Info("apple_verify_success", "transaction_id", txInfo.TransactionID, "pet_id", req.PetID, "expiry", expiry)
	writeJSON(w, http.StatusOK, MessageResponse{Message: "subscription activated"})
}
