package handlers

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"
	"vetapp-backend/internal/services"

	"gorm.io/gorm"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	db           *gorm.DB
	authService  *services.AuthService
	smsService   *services.SMSService
	emailService *services.EmailService
	// publicBaseURL is the absolute URL the email-verification link
	// points back to (e.g. https://vetapp-backened-production.up.railway.app).
	// Resolved at construction time from cfg.EmailVerifyBaseURL with
	// cfg.BaseURL as a fallback.
	publicBaseURL string
}

// NewAuthHandler creates a new AuthHandler.
//
// `emailService` may be a service whose .Enabled() reports false —
// in that case email-verification calls degrade to the legacy "pending"
// stub response so existing clients don't break.
//
// `publicBaseURL` is the URL prefix for outgoing verification links.
// Pass cfg.EmailVerifyBaseURL (or fall back to cfg.BaseURL) — the
// router does this for us.
func NewAuthHandler(db *gorm.DB, authService *services.AuthService, smsService *services.SMSService, emailService *services.EmailService, publicBaseURL string) *AuthHandler {
	return &AuthHandler{
		db:            db,
		authService:   authService,
		smsService:    smsService,
		emailService:  emailService,
		publicBaseURL: publicBaseURL,
	}
}

// --- Request/Response types ---

type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=1"`
}

type LoginResponse struct {
	AccessToken  string `json:"access_token" validate:"required"`
	RefreshToken string `json:"refreshToken" validate:"required"` // camelCase — frontend expects this
}

type RegisterRequest struct {
	FirstName string `json:"first_name" validate:"required"`
	LastName  string `json:"last_name" validate:"required"`
	Email     string `json:"email" validate:"required,email"`
	Phone     string `json:"phone"`
	Password  string `json:"password" validate:"required,min=1"`
	GroupID   int    `json:"group_id"`
	Zip       string `json:"zip"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// UserResponse is the public user profile returned by /auth/me. Mirrors
// the columns the mobile profile screen needs to render.
type UserResponse struct {
	ID            uint   `json:"id" validate:"required"`
	FirstName     string `json:"first_name" validate:"required"`
	LastName      string `json:"last_name" validate:"required"`
	Email         string `json:"email" validate:"required"`
	Phone         string `json:"phone" validate:"required"`
	Address       string `json:"address"`
	City          string `json:"city"`
	CountryID     int    `json:"country_id"`
	Zip           string `json:"zip" validate:"required"`
	GroupID       int    `json:"group_id" validate:"required"`
	CompanyName   string `json:"company_name" validate:"required"`
	Status        string `json:"status" validate:"required"`
	EmailVerified bool   `json:"email_verified"`
	PhoneVerified bool   `json:"phone_verified"`
}

// --- Handlers ---

// Login authenticates a user with email + password.
// @Summary Login
// @Tags auth
// @Accept json
// @Produce json
// @Param body body LoginRequest true "Login credentials"
// @Success 200 {object} LoginResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Router /auth/login [post]
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)

	var req LoginRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Find user by email
	var user models.User
	if err := h.db.Where("email = ?", req.Email).First(&user).Error; err != nil {
		log.Warn("login_failed", "email", req.Email, "reason", "user_not_found")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid credentials"})
		return
	}

	// Decrypt stored password and compare (MySQL AES_ENCRYPT format)
	storedPassword, err := h.authService.DecryptPassword(user.Password)
	if err != nil {
		log.Error("login_failed", "email", req.Email, "reason", "decrypt_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "authentication error"})
		return
	}

	if storedPassword != req.Password {
		log.Warn("login_failed", "email", req.Email, "reason", "invalid_password")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid credentials"})
		return
	}

	// Generate token pair
	tokens, err := h.authService.GenerateTokenPair(&user)
	if err != nil {
		log.Error("login_failed", "email", req.Email, "reason", "token_generation", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to generate tokens"})
		return
	}

	// Update last login
	now := time.Now()
	h.db.Model(&user).Update("last_login", now)

	log.Info("login_success", "email", req.Email, "user_id", user.ID, "clinic", user.Zip)

	writeJSON(w, http.StatusOK, LoginResponse{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
	})
}

// Register creates a new user account.
// @Summary Register
// @Tags auth
// @Accept json
// @Produce json
// @Param body body RegisterRequest true "Registration details"
// @Success 201 {object} LoginResponse
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /auth/register [post]
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)

	var req RegisterRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Check if email already exists
	var existing models.User
	if err := h.db.Where("email = ?", req.Email).First(&existing).Error; err == nil {
		log.Warn("register_failed", "email", req.Email, "reason", "email_exists")
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "email already registered"})
		return
	}

	// Encrypt password (MySQL AES_ENCRYPT compatible)
	encryptedBytes, err := h.authService.EncryptPassword(req.Password)
	if err != nil {
		log.Error("register_failed", "email", req.Email, "reason", "encrypt_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to encrypt password"})
		return
	}

	// Default to owner role if not specified
	groupID := req.GroupID
	if groupID == 0 {
		groupID = models.RoleOwner
	}

	user := models.User{
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Email:     req.Email,
		Phone:     req.Phone,
		Password:  encryptedBytes,
		GroupID:   groupID,
		Zip:       req.Zip,
		Status:    "T",
	}

	if err := h.db.Create(&user).Error; err != nil {
		log.Error("register_failed", "email", req.Email, "reason", "db_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create user"})
		return
	}

	// Generate tokens
	tokens, err := h.authService.GenerateTokenPair(&user)
	if err != nil {
		log.Error("register_failed", "email", req.Email, "reason", "token_generation", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to generate tokens"})
		return
	}

	log.Info("user_registered", "email", req.Email, "user_id", user.ID, "clinic", req.Zip)

	writeJSON(w, http.StatusCreated, LoginResponse{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
	})
}

// Refresh exchanges a refresh token for a new token pair.
// @Summary Refresh tokens
// @Tags auth
// @Accept json
// @Produce json
// @Param body body RefreshRequest true "Refresh token"
// @Success 200 {object} LoginResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Router /auth/refresh [post]
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)

	var req RefreshRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Validate refresh token
	claims, err := h.authService.ValidateRefreshToken(req.RefreshToken)
	if err != nil {
		log.Warn("refresh_failed", "reason", "invalid_token", "error", err)
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid or expired refresh token"})
		return
	}

	// Look up user
	var user models.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		log.Warn("refresh_failed", "user_id", claims.UserID, "reason", "user_not_found")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "user not found"})
		return
	}

	// Generate new token pair
	tokens, err := h.authService.GenerateTokenPair(&user)
	if err != nil {
		log.Error("refresh_failed", "user_id", user.ID, "reason", "token_generation", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to generate tokens"})
		return
	}

	log.Info("token_refreshed", "user_id", user.ID)

	writeJSON(w, http.StatusOK, tokens)
}

// Me returns the current authenticated user's profile.
// @Summary Get current user
// @Tags auth
// @Produce json
// @Security BearerAuth
// @Success 200 {object} UserResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /auth/me [get]
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}

	var user models.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "user not found"})
		return
	}

	writeJSON(w, http.StatusOK, user)
}

// UpdateMeRequest is the payload for updating the current user's profile.
// Only the fields a user can edit themselves are exposed — `email`,
// `last_name` (Georgian personal ID), `group_id`, and `password` are
// intentionally NOT in this struct because changing them is a separate
// (privileged or audited) flow.
//
// Pointer fields → only sent fields are updated, so the mobile app can
// do a partial PATCH-style call without needing to send the complete
// object.
type UpdateMeRequest struct {
	FirstName *string `json:"first_name"`
	Phone     *string `json:"phone"`
	Address   *string `json:"address"`
	City      *string `json:"city"`
	CountryID *int    `json:"country_id"`
}

// UpdateMe updates the current authenticated user's editable profile
// fields. Pointer fields → only sent fields are updated, so the mobile
// app can do a partial PATCH-style call without needing to send the
// complete object.
// @Summary Update current user profile
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body UpdateMeRequest true "Fields to update"
// @Success 200 {object} UserResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /auth/me [put]
func (h *AuthHandler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}

	var req UpdateMeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	var user models.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "user not found"})
		return
	}

	// Build an update map containing only the fields the client sent.
	// Skipping nil-valued pointers preserves untouched columns.
	updates := map[string]interface{}{}
	if req.FirstName != nil {
		updates["first_name"] = *req.FirstName
	}
	if req.Phone != nil {
		// Strip whitespace so we don't store stray spaces from autofill.
		updates["phone"] = strings.TrimSpace(*req.Phone)
	}
	if req.Address != nil {
		updates["address"] = strings.TrimSpace(*req.Address)
	}
	if req.City != nil {
		updates["city"] = strings.TrimSpace(*req.City)
	}
	if req.CountryID != nil {
		updates["country_id"] = *req.CountryID
	}
	if len(updates) == 0 {
		// Nothing to update — return the current row unchanged.
		writeJSON(w, http.StatusOK, user)
		return
	}

	if err := h.db.Model(&user).Updates(updates).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update profile"})
		return
	}

	// Re-read so the response reflects the row exactly as it now lives
	// in the DB (defaults, triggers, etc.).
	h.db.First(&user, claims.UserID)
	writeJSON(w, http.StatusOK, user)
}

// ChangePasswordRequest is the request body for changing the
// authenticated user's password from inside the app (Settings →
// Authorization → Change Password). This is distinct from
// `PasswordReset`, which uses an OTP-recovery flow when the user has
// FORGOTTEN their password.
//
// Both `current_password` and `new_password` are sent in plaintext over
// HTTPS — same posture as Login. The backend re-encrypts the new value
// with the legacy AES-128-ECB scheme so the column stays compatible
// with the PHP frontend during the migration.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" validate:"required,min=1"`
	// 8 chars matches the registration / reset minimum used elsewhere.
	NewPassword string `json:"new_password" validate:"required,min=8,max=50"`
}

// ChangePasswordResponse is intentionally tiny — clients just want a
// success signal so they can navigate back. Refreshing the JWT pair
// would be defensible (rotate-on-credential-change), but our refresh
// tokens already rotate on the next /auth/refresh call, so we don't
// invalidate sessions here. Documented so a future security review
// has the rationale.
type ChangePasswordResponse struct {
	OK bool `json:"ok" validate:"required"`
}

// ChangePassword lets a logged-in user rotate their password.
//
// Validation:
//
//   • Body decodes + matches the validator constraints above.
//   • `current_password` decrypts to the stored value (constant-time
//     compare via the AuthService helper would be nice; today it's a
//     plain `==` because legacy decryption already happens in Login).
//   • `new_password != current_password` — refuse a no-op rotation so
//     accidental double-clicks don't surface "success" without change.
//
// On success: 200 {ok: true}. On any failure: 4xx with a generic
// "wrong current password" message — we deliberately don't distinguish
// "user not found" from "wrong password" to avoid leaking which is the
// case (defence-in-depth, even though the JWT already proved identity).
//
// @Summary Change password
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body ChangePasswordRequest true "Current and new password"
// @Success 200 {object} ChangePasswordResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Router /auth/change-password [post]
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)

	claims := middleware.GetClaims(r)
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}

	var req ChangePasswordRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	if req.NewPassword == req.CurrentPassword {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "new password must differ from current password"})
		return
	}

	var user models.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		// Treat as "wrong current password" to avoid information leak.
		log.Warn("change_password_failed", "user_id", claims.UserID, "reason", "user_not_found")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "current password is incorrect"})
		return
	}

	current, err := h.authService.DecryptPassword(user.Password)
	if err != nil {
		log.Error("change_password_failed", "user_id", user.ID, "reason", "decrypt_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "authentication error"})
		return
	}
	if current != req.CurrentPassword {
		log.Warn("change_password_failed", "user_id", user.ID, "reason", "invalid_current_password")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "current password is incorrect"})
		return
	}

	encrypted, err := h.authService.EncryptPassword(req.NewPassword)
	if err != nil {
		log.Error("change_password_failed", "user_id", user.ID, "reason", "encrypt_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not encrypt new password"})
		return
	}

	if err := h.db.Model(&user).Update("password", encrypted).Error; err != nil {
		log.Error("change_password_failed", "user_id", user.ID, "reason", "db_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not update password"})
		return
	}

	log.Info("password_changed", "user_id", user.ID)
	writeJSON(w, http.StatusOK, ChangePasswordResponse{OK: true})
}

// EmailVerifySendResponse is the JSON returned by /auth/email/verify/send.
//
// The endpoint is intentionally lenient about its response shape:
//
//   • "sent"    — the email was handed off to Resend successfully.
//   • "pending" — the service isn't configured (RESEND_API_KEY empty);
//                 the request is still accepted so the mobile UI can
//                 show "we got your request" without surfacing config
//                 problems to end users.
//
// The mobile alert maps both cases to the same friendly message so the
// user never sees "service not configured".
type EmailVerifySendResponse struct {
	Status string `json:"status" validate:"required"` // "sent" | "pending"
	Detail string `json:"detail,omitempty"`           // human-readable note
}

// generateVerificationToken returns a URL-safe random token suitable
// for an email verification link. 32 bytes of entropy → 43 chars in
// base64url, well past the brute-force threshold for a 24h TTL.
func generateVerificationToken() (string, error) {
	b := make([]byte, 32)
	if _, err := cryptorand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SendEmailVerification generates a single-use token, persists it,
// and sends a verification email via Resend.
//
// If the email service isn't configured (no RESEND_API_KEY) we still
// return 202 with status="pending" so the mobile contract stays
// stable across environments — that's how the integration shipped to
// TestFlight before Resend was wired up.
//
// @Summary Send email verification
// @Tags auth
// @Produce json
// @Security BearerAuth
// @Success 202 {object} EmailVerifySendResponse
// @Failure 401 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse "email already verified"
// @Router /auth/email/verify/send [post]
func (h *AuthHandler) SendEmailVerification(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)

	claims := middleware.GetClaims(r)
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}

	var user models.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "user not found"})
		return
	}

	if user.EmailVerified {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "email already verified"})
		return
	}

	if user.Email == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "user has no email on file"})
		return
	}

	// Service not configured — accept the request, log a warning, return
	// the legacy "pending" status so the mobile UX stays consistent.
	if h.emailService == nil || !h.emailService.Enabled() {
		log.Warn("email_verify_skipped", "reason", "service_disabled", "user_id", user.ID)
		writeJSON(w, http.StatusAccepted, EmailVerifySendResponse{
			Status: "pending",
			Detail: "email service not configured",
		})
		return
	}

	tokenStr, err := generateVerificationToken()
	if err != nil {
		log.Error("email_verify_failed", "reason", "token_gen", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not generate token"})
		return
	}

	token := models.EmailVerificationToken{
		UserID:    user.ID,
		Token:     tokenStr,
		ExpiresAt: time.Now().Add(24 * time.Hour),
		CreatedAt: time.Now(),
	}
	if err := h.db.Create(&token).Error; err != nil {
		log.Error("email_verify_failed", "reason", "db_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not persist token"})
		return
	}

	verifyURL := fmt.Sprintf("%s/api/auth/email/verify/confirm?token=%s",
		strings.TrimRight(h.publicBaseURL, "/"), tokenStr)

	subject := "VetApp — დაადასტურეთ თქვენი ელ.ფოსტა / Verify your email"
	html := emailVerificationHTML(user.FirstName, verifyURL)
	text := emailVerificationText(user.FirstName, verifyURL)

	id, err := h.emailService.SendHTML(user.Email, subject, html, text)
	if err != nil {
		log.Error("email_verify_failed", "reason", "send_failed", "user_id", user.ID, "error", err)
		// Don't reveal Resend internals to the client. Pending = "we
		// took your request, retry later" — fine UX.
		writeJSON(w, http.StatusAccepted, EmailVerifySendResponse{
			Status: "pending",
			Detail: "email send failed — please try again later",
		})
		return
	}

	log.Info("email_verify_sent", "user_id", user.ID, "resend_id", id)
	writeJSON(w, http.StatusAccepted, EmailVerifySendResponse{
		Status: "sent",
		Detail: "verification email dispatched",
	})
}

// ConfirmEmailVerification handles the GET request that fires when the
// user taps the verification link in their inbox. Renders an HTML
// success page on success, or an HTML error page otherwise — keeping
// the experience self-contained (no separate web frontend required).
//
// Token semantics:
//
//   • Must exist in email_verification_tokens.
//   • used_at IS NULL.
//   • expires_at > NOW().
//
// On success: flip user.email_verified = TRUE, set used_at = NOW().
// Idempotent if the user is already verified (we still 200 with the
// success page so back/forward navigation doesn't surface an error).
//
// @Summary Confirm email verification
// @Tags auth
// @Produce text/html
// @Param token query string true "Verification token from the email link"
// @Success 200 {string} string "HTML success page"
// @Failure 400 {string} string "HTML error page"
// @Router /auth/email/verify/confirm [get]
func (h *AuthHandler) ConfirmEmailVerification(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)

	tokenStr := strings.TrimSpace(r.URL.Query().Get("token"))
	if tokenStr == "" {
		writeVerifyHTML(w, http.StatusBadRequest, false, "Missing token")
		return
	}

	var token models.EmailVerificationToken
	if err := h.db.Where("token = ?", tokenStr).First(&token).Error; err != nil {
		log.Warn("email_verify_confirm_failed", "reason", "not_found")
		writeVerifyHTML(w, http.StatusBadRequest, false, "This verification link is invalid.")
		return
	}

	if token.UsedAt != nil {
		// Already consumed. Treat as success: the user might have
		// clicked twice / hit refresh.
		writeVerifyHTML(w, http.StatusOK, true, "Your email is already verified.")
		return
	}

	if time.Now().After(token.ExpiresAt) {
		log.Warn("email_verify_confirm_failed", "reason", "expired", "token_id", token.ID)
		writeVerifyHTML(w, http.StatusBadRequest, false, "This verification link has expired. Please request a new one from the app.")
		return
	}

	// Flip the user flag + mark token used in a single transaction so a
	// crash mid-operation can't leave us with a verified user and an
	// unconsumed token (or vice versa).
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.User{}).
			Where("id = ?", token.UserID).
			Update("emailVerified", true).Error; err != nil {
			return err
		}
		now := time.Now()
		return tx.Model(&token).Update("used_at", &now).Error
	})
	if err != nil {
		log.Error("email_verify_confirm_failed", "reason", "db_error", "error", err)
		writeVerifyHTML(w, http.StatusInternalServerError, false, "Something went wrong. Please try again.")
		return
	}

	log.Info("email_verified", "user_id", token.UserID)
	writeVerifyHTML(w, http.StatusOK, true, "Your email is verified — thanks! You can return to the VetApp app.")
}

// writeVerifyHTML renders a self-contained landing page (no external
// CSS) so the user gets a clean confirmation regardless of which
// device opens the link. The OK / fail variants share a layout but
// differ in headline + accent color.
//
// On success we surface a "Back to VetApp" deeplink button. The link
// scheme `vetapp://profile` matches the Expo deep-link config in the
// mobile app — tapping it on iOS/Android brings the user straight back
// to the app's profile screen. On desktops the scheme is a no-op (the
// browser shows a "no app to handle this" prompt) which is acceptable
// because desktop users see the page in their normal browser tab and
// will switch back to whatever they were doing manually.
func writeVerifyHTML(w http.ResponseWriter, status int, ok bool, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)

	accent := "#2563eb" // blue-600
	headline := "Email verified"
	emoji := "✅"
	if !ok {
		accent = "#dc2626" // red-600
		headline = "Verification failed"
		emoji = "⚠️"
	}

	// Only show the "Back to VetApp" CTA on success — for failures the
	// user may need to request a fresh link from the app first, so a
	// big "open the app" button would be misleading.
	//
	// `vetappmobile://` matches the `expo.scheme` field in
	// vetapp-mobile/app.json — tapping the button on iOS/Android brings
	// the user straight back to the app. Desktop browsers no-op on
	// custom schemes; the brand block stands on its own there.
	cta := ""
	if ok {
		cta = `<a href="vetappmobile://" style="display:inline-block;margin-top:24px;padding:12px 24px;background:#2563eb;color:#fff;text-decoration:none;border-radius:10px;font-weight:700;font-size:14px;">Back to VetApp</a>`
	}

	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s — VetApp</title>
<style>
  body { margin:0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
         background:#f8fafc; color:#0f172a; min-height:100vh;
         display:flex; align-items:center; justify-content:center; padding:24px; }
  .card { background:#fff; border-radius:16px; padding:32px;
          box-shadow:0 10px 25px rgba(15,23,42,0.08); max-width:420px; width:100%%;
          text-align:center; }
  .emoji { font-size:48px; line-height:1; margin-bottom:16px; }
  h1 { font-size:22px; margin:0 0 12px; color:%s; }
  p  { font-size:15px; line-height:1.5; color:#475569; margin:0; }
  .brand { margin-top:24px; font-size:13px; font-weight:700; color:%s; letter-spacing:1px; }
</style>
</head>
<body>
  <div class="card">
    <div class="emoji">%s</div>
    <h1>%s</h1>
    <p>%s</p>
    %s
    <div class="brand">VETAPP</div>
  </div>
</body>
</html>`, headline, accent, accent, emoji, headline, message, cta)
}

// emailVerificationHTML renders the HTML body of the verification
// email. Inline-styled because most clients (Outlook, Gmail) strip
// <style> tags — the only reliable way to format an email is inline.
func emailVerificationHTML(firstName, link string) string {
	greet := "Hello"
	if firstName != "" {
		greet = "Hello " + firstName
	}
	return fmt.Sprintf(`<!doctype html>
<html><body style="margin:0;padding:0;background:#f8fafc;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;color:#0f172a;">
<table width="100%%" cellpadding="0" cellspacing="0" border="0">
  <tr><td align="center" style="padding:32px 16px;">
    <table width="480" cellpadding="0" cellspacing="0" border="0" style="background:#fff;border-radius:16px;box-shadow:0 10px 25px rgba(15,23,42,0.08);">
      <tr><td style="padding:32px;">
        <div style="font-size:13px;font-weight:700;color:#2563eb;letter-spacing:1px;margin-bottom:24px;">VETAPP</div>
        <h1 style="margin:0 0 16px;font-size:22px;color:#0f172a;">Verify your email</h1>
        <p style="margin:0 0 24px;font-size:15px;line-height:1.6;color:#475569;">%s,<br><br>Tap the button below to confirm your email address. The link expires in 24 hours.</p>
        <table cellpadding="0" cellspacing="0" border="0">
          <tr><td style="background:#2563eb;border-radius:10px;">
            <a href="%s" style="display:inline-block;padding:14px 28px;font-size:15px;font-weight:700;color:#fff;text-decoration:none;">Verify email</a>
          </td></tr>
        </table>
        <p style="margin:24px 0 0;font-size:13px;line-height:1.5;color:#94a3b8;">If the button doesn't work, copy and paste this link into your browser:<br><span style="word-break:break-all;color:#475569;">%s</span></p>
      </td></tr>
    </table>
    <p style="margin:16px 0 0;font-size:12px;color:#94a3b8;">If you didn't request this, you can safely ignore this email.</p>
  </td></tr>
</table>
</body></html>`, greet, link, link)
}

// emailVerificationText is the plaintext fallback. Same content,
// stripped of markup, used by clients that block HTML and as a
// deliverability boost.
func emailVerificationText(firstName, link string) string {
	greet := "Hello,"
	if firstName != "" {
		greet = "Hello " + firstName + ","
	}
	return fmt.Sprintf(`%s

Tap the link below to verify your email address. The link expires in 24 hours.

%s

If you didn't request this, you can safely ignore this email.

— VetApp`, greet, link)
}

// --- OTP types ---

// OTPSendRequest is the request body for sending an OTP.
type OTPSendRequest struct {
	Phone string `json:"phone" validate:"required"`
	Type  string `json:"type" validate:"required,oneof=register recovery"`
}

// OTPSendResponse is the response after sending an OTP.
type OTPSendResponse struct {
	OTPID uint `json:"otp_id" validate:"required"`
}

// OTPVerifyRequest is the request body for verifying an OTP.
type OTPVerifyRequest struct {
	OTPID uint   `json:"otp_id" validate:"required"`
	Code  string `json:"code" validate:"required"`
}

// OTPVerifyResponse is the response after verifying an OTP.
type OTPVerifyResponse struct {
	Verified bool `json:"verified" validate:"required"`
}

// PasswordResetRequest is the request body for resetting a password.
type PasswordResetRequest struct {
	OTPID       uint   `json:"otp_id" validate:"required"`
	OTPCode     string `json:"otp_code" validate:"required"`
	NewPassword string `json:"new_password" validate:"required,min=1"`
}

// --- OTP Handlers ---

// OTPSend generates and sends an OTP code via SMS.
// @Summary Send OTP
// @Tags auth
// @Accept json
// @Produce json
// @Param body body OTPSendRequest true "Phone and type"
// @Success 200 {object} OTPSendResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /auth/otp/send [post]
func (h *AuthHandler) OTPSend(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)

	var req OTPSendRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Generate 4-digit code
	code := fmt.Sprintf("%04d", rand.Intn(9000)+1000)

	otp := models.OTP{
		Phone:     req.Phone,
		Code:      code,
		Type:      req.Type,
		ExpiresAt: time.Now().Add(60 * time.Second),
		CreatedAt: time.Now(),
	}

	if err := h.db.Create(&otp).Error; err != nil {
		log.Error("otp_send_failed", "phone", req.Phone, "reason", "db_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create OTP"})
		return
	}

	// Send SMS
	msg := fmt.Sprintf("VetApp: თქვენი კოდია %s", code)
	if err := h.smsService.Send(req.Phone, msg); err != nil {
		log.Error("otp_send_failed", "phone", req.Phone, "reason", "sms_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to send SMS"})
		return
	}

	log.Info("otp_sent", "phone", req.Phone, "type", req.Type, "otp_id", otp.ID)

	writeJSON(w, http.StatusOK, OTPSendResponse{OTPID: otp.ID})
}

// OTPVerify verifies an OTP code.
// @Summary Verify OTP
// @Tags auth
// @Accept json
// @Produce json
// @Param body body OTPVerifyRequest true "OTP ID and code"
// @Success 200 {object} OTPVerifyResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Router /auth/otp/verify [post]
func (h *AuthHandler) OTPVerify(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)

	var req OTPVerifyRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	var otp models.OTP
	if err := h.db.First(&otp, req.OTPID).Error; err != nil {
		log.Warn("otp_verify_failed", "otp_id", req.OTPID, "reason", "not_found")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid OTP"})
		return
	}

	if otp.Used || time.Now().After(otp.ExpiresAt) {
		log.Warn("otp_verify_failed", "otp_id", req.OTPID, "reason", "expired_or_used")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "OTP expired or already used"})
		return
	}

	if otp.Code != req.Code {
		log.Warn("otp_verify_failed", "otp_id", req.OTPID, "reason", "wrong_code")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid code"})
		return
	}

	// Mark as used
	h.db.Model(&otp).Update("used", true)

	log.Info("otp_verified", "otp_id", req.OTPID, "phone", otp.Phone)

	writeJSON(w, http.StatusOK, OTPVerifyResponse{Verified: true})
}

// PasswordReset resets a user's password after OTP verification.
// @Summary Reset password
// @Tags auth
// @Accept json
// @Produce json
// @Param body body PasswordResetRequest true "OTP and new password"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /auth/password-reset [post]
func (h *AuthHandler) PasswordReset(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)

	var req PasswordResetRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Verify OTP
	var otp models.OTP
	if err := h.db.First(&otp, req.OTPID).Error; err != nil {
		log.Warn("password_reset_failed", "otp_id", req.OTPID, "reason", "otp_not_found")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid OTP"})
		return
	}

	if otp.Used || time.Now().After(otp.ExpiresAt) || otp.Code != req.OTPCode {
		log.Warn("password_reset_failed", "otp_id", req.OTPID, "reason", "otp_invalid")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid or expired OTP"})
		return
	}

	// Find user by phone
	var user models.User
	if err := h.db.Where("phone = ?", otp.Phone).First(&user).Error; err != nil {
		log.Warn("password_reset_failed", "phone", otp.Phone, "reason", "user_not_found")
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "user not found"})
		return
	}

	// Encrypt new password
	encrypted, err := h.authService.EncryptPassword(req.NewPassword)
	if err != nil {
		log.Error("password_reset_failed", "user_id", user.ID, "reason", "encrypt_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to encrypt password"})
		return
	}

	if err := h.db.Model(&user).Update("password", encrypted).Error; err != nil {
		log.Error("password_reset_failed", "user_id", user.ID, "reason", "db_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update password"})
		return
	}

	// Mark OTP as used
	h.db.Model(&otp).Update("used", true)

	log.Info("password_reset", "user_id", user.ID, "phone", otp.Phone)

	writeJSON(w, http.StatusOK, MessageResponse{Message: "password reset successfully"})
}

// --- Helpers ---

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
