package handlers

import (
	cryptorand "crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"
	"vetapp-backend/internal/services"

	"gorm.io/gorm"
)

// otpSendLimiter caps OTP-send requests per phone to deter SMS-pump
// abuse (we pay per SMS) and basic brute force preparation.
//   - Burst window: 60s, max 1 send per phone.
//   - Daily window: 24h, max 5 sends per phone.
//
// Two limiters cover both rules; the stricter one wins.
var (
	otpSendBurstLimiter = middleware.NewRateLimiter(1, 60*time.Second)
	otpSendDailyLimiter = middleware.NewRateLimiter(5, 24*time.Hour)
	// otpVerifyLimiter caps verify attempts per OTP ID across the whole
	// process — per-row `tries` already handles per-OTP brute force,
	// this catches enumeration of OTPIDs.
	otpVerifyLimiter = middleware.NewRateLimiter(20, time.Minute)
)

// numericCode returns a zero-padded n-digit numeric code generated
// from crypto/rand. We avoid math/rand here because OTPs are a
// security primitive and a predictable seed is unacceptable.
func numericCode(digits int) (string, error) {
	if digits <= 0 || digits > 12 {
		return "", fmt.Errorf("invalid digits: %d", digits)
	}
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint64(b[:])
	mod := uint64(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, n%mod), nil
}

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
	// FirstName is the full display name ("Given Surname").
	FirstName string `json:"first_name" validate:"required"`
	// LastName is the personal ID (legacy column reuse). App builds up
	// to 1.0 sent the surname here and the ID as `idNumber`; when an ID
	// field is present it wins and the surname joins the display name.
	LastName       string `json:"last_name"`
	IDNumber       string `json:"id_number"`
	IDNumberLegacy string `json:"idNumber"`
	Email          string `json:"email" validate:"required,email"`
	Phone          string `json:"phone" validate:"required"`
	Password       string `json:"password" validate:"required,min=1"`
	// The register OTP sent to Phone. Older builds used `otpId` (a
	// string) and `code`.
	OTPID       flexUint `json:"otp_id"`
	OTPIDLegacy flexUint `json:"otpId"`
	OTPCode     string   `json:"otp_code"`
	CodeLegacy  string   `json:"code"`
	// GroupID and Zip are ignored: public sign-up always creates an owner.
	// Kept so older clients still decode.
	GroupID int    `json:"group_id"`
	Zip     string `json:"zip"`
}

// flexUint decodes a JSON number or a numeric string.
type flexUint uint

func (f *flexUint) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	var n uint
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return fmt.Errorf("invalid id %q", s)
	}
	*f = flexUint(n)
	return nil
}

// personalIDPattern accepts a Georgian personal number (11 digits) or a
// foreign document number.
var personalIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{5,20}$`)

// registrationIdentity works out the personal ID and display name from
// either request shape (see RegisterRequest).
func registrationIdentity(req RegisterRequest) (personalID, displayName string) {
	displayName = strings.TrimSpace(req.FirstName)
	last := strings.TrimSpace(req.LastName)
	for _, v := range []string{req.IDNumber, req.IDNumberLegacy} {
		if v = strings.TrimSpace(v); v != "" {
			personalID = v
			break
		}
	}
	if personalID == "" {
		return last, displayName
	}
	if last != "" && last != personalID {
		displayName = strings.TrimSpace(displayName + " " + last)
	}
	return personalID, displayName
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

// toUserResponse projects the full User model down to the public
// `UserResponse` shape returned by /auth/me. Centralised so the JSON
// contract stays in one place — without it, callers `writeJSON(user)`
// directly and quietly leak password_hash, last_login, etc., and the
// Swagger doc drifts from reality.
func toUserResponse(u models.User) UserResponse {
	return UserResponse{
		ID:            u.ID,
		FirstName:     u.FirstName,
		LastName:      u.LastName,
		Email:         u.Email,
		Phone:         u.Phone,
		Address:       u.Address,
		City:          u.City,
		CountryID:     u.CountryID,
		Zip:           u.Zip,
		GroupID:       u.GroupID,
		CompanyName:   u.CompanyName,
		Status:        u.Status,
		EmailVerified: u.EmailVerified,
		PhoneVerified: u.PhoneVerified,
	}
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
	// Only active accounts may sign in. A removed vet keeps a row (with
	// status F) and several legacy rows can share one address; without
	// the status filter the lowest id won, disabled or not.
	// Case-insensitive: phone keyboards capitalise the first letter.
	if err := h.db.Where("LOWER(TRIM(email)) = LOWER(TRIM(?)) AND status = ?", req.Email, "T").
		Order("last_login DESC NULLS LAST, id DESC").First(&user).Error; err != nil {
		log.Warn("login_failed", "email", req.Email, "reason", "user_not_found")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid credentials", Code: codeInvalidCredentials})
		return
	}

	// Verify password — prefer bcrypt, fall back to legacy AES.
	// On a successful AES verification, backfill bcrypt so this user
	// migrates off the reversible scheme on their next login.
	authed := false
	if user.PasswordHash != "" && h.authService.VerifyBcrypt(user.PasswordHash, req.Password) {
		authed = true
	} else if len(user.Password) > 0 && h.authService.VerifyLegacyAES(user.Password, req.Password) {
		authed = true
		if hash, err := h.authService.HashPassword(req.Password); err == nil {
			if err := h.db.Model(&user).Update("password_hash", hash).Error; err != nil {
				log.Warn("login_bcrypt_backfill_failed", "user_id", user.ID, "error", err)
			}
		}
	}
	if !authed {
		// Generic 401 — never distinguish "user has corrupt ciphertext"
		// or "wrong password" so attackers can't enumerate accounts.
		log.Warn("login_failed", "email", req.Email, "reason", "invalid_password")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid credentials", Code: codeInvalidCredentials})
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

	personalID, displayName := registrationIdentity(req)
	if !personalIDPattern.MatchString(personalID) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid personal ID", Code: codeInvalidPersonalID})
		return
	}
	phone := normalizePhone(req.Phone)
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid phone number", Code: codeInvalidPhone})
		return
	}
	email := strings.TrimSpace(req.Email)

	// Check if email already exists
	if h.emailTaken(email) {
		log.Warn("register_failed", "email", email, "reason", "email_exists")
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "email already registered", Code: codeEmailTaken})
		return
	}

	// The phone must be proven with the register code sent to it; the
	// request used to carry the code but nothing checked it.
	otpID := uint(req.OTPID)
	if otpID == 0 {
		otpID = uint(req.OTPIDLegacy)
	}
	otpCode := req.OTPCode
	if otpCode == "" {
		otpCode = req.CodeLegacy
	}
	otp, status, otpErr := checkOTP(h.db, otpID, otpCode, "register", phone)
	if otpErr != nil {
		log.Warn("register_failed", "email", email, "reason", otpErr.Code)
		writeJSON(w, status, *otpErr)
		return
	}

	// Bcrypt is the source of truth going forward. We still write the
	// AES column so that any out-of-band reader (admin tooling) keeps
	// working during the migration window.
	hash, err := h.authService.HashPassword(req.Password)
	if err != nil {
		log.Error("register_failed", "email", req.Email, "reason", "hash_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to hash password"})
		return
	}
	encryptedBytes, err := h.authService.EncryptPassword(req.Password)
	if err != nil {
		log.Error("register_failed", "email", req.Email, "reason", "encrypt_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to encrypt password"})
		return
	}

	// Public sign-up only ever creates a pet owner. It used to take
	// group_id and zip from the request, so anyone could register as an
	// administrator (group 4) or as a vet of any clinic (group 2 + zip).
	// Vets are created by their clinic (POST /staff).
	groupID := models.RoleOwner

	// The personal ID is what links an owner to their pets (pets.uuid), so
	// a second account on an ID already in use would see and edit the
	// first owner's pets. Refuse it; the owner should sign in or reset
	// their password instead.
	// Disabled accounts count too: their ID still owns pets. The check
	// and the insert below share an advisory lock on the ID, so two
	// simultaneous sign-ups with one ID cannot both pass.
	tx := h.db.Begin()
	defer tx.Rollback()
	tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('register|' || ?))`, personalID)
	// And on the email, so two sign-ups with one address (different IDs)
	// cannot both pass the check above.
	tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('register-email|' || LOWER(TRIM(?))))`, email)
	var emailTaken int64
	tx.Model(&models.User{}).Where("LOWER(TRIM(email)) = LOWER(TRIM(?))", email).Count(&emailTaken)
	if emailTaken > 0 {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "email already registered", Code: codeEmailTaken})
		return
	}
	var taken int64
	tx.Model(&models.User{}).
		Where("TRIM(last_name) = ? AND group_id = ?", personalID, models.RoleOwner).
		Count(&taken)
	if taken > 0 {
		log.Warn("register_failed", "email", email, "reason", "personal_id_in_use")
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "an account with this personal ID already exists", Code: codePersonalIDTaken})
		return
	}
	// Clinics register pets under the owner's personal ID and record the
	// owner's phone on them. Claiming an ID that already has pets needs a
	// verified phone the clinic has on record for it; otherwise anyone
	// could type someone else's ID and see their pets. (IDs whose pets
	// carry no usable phone can't be checked and are let through.)
	if !phoneOnRecordFor(tx, personalID, phone) {
		log.Warn("register_failed", "email", email, "reason", "identity_unverified")
		writeJSON(w, http.StatusConflict, ErrorResponse{
			Error: "this phone number doesn't match the clinic's records for this personal ID",
			Code:  codeIdentityUnverified,
		})
		return
	}
	if !consumeOTP(tx, otp.ID) {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "code expired or already used", Code: codeOTPExpired})
		return
	}

	user := models.User{
		FirstName:    displayName,
		LastName:     personalID,
		Email:        email,
		Phone:        phone,
		Password:     encryptedBytes,
		PasswordHash: hash,
		GroupID:      groupID,
		Zip:          "",
		Status:       "T",
	}

	if err := tx.Create(&user).Error; err != nil {
		log.Error("register_failed", "email", req.Email, "reason", "db_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create user"})
		return
	}
	if err := tx.Commit().Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create account"})
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
	if err := h.db.Where("id = ? AND status = ?", claims.UserID, "T").First(&user).Error; err != nil {
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
	log := middleware.RequestLogger(r)

	claims := middleware.GetClaims(r)
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}

	log.Info("me_lookup", "user_id", claims.UserID, "email", claims.Email)

	var user models.User
	if err := h.db.Where("id = ?", claims.UserID).First(&user).Error; err != nil {
		log.Warn("me_user_not_found", "user_id", claims.UserID, "error", err)
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "user not found"})
		return
	}

	writeJSON(w, http.StatusOK, toUserResponse(user))
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
		phone := normalizePhone(*req.Phone)
		if phone == "" {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid phone number", Code: codeInvalidPhone})
			return
		}
		updates["phone"] = phone
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
	writeJSON(w, http.StatusOK, toUserResponse(user))
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
//   - Body decodes + matches the validator constraints above.
//   - `current_password` decrypts to the stored value (constant-time
//     compare via the AuthService helper would be nice; today it's a
//     plain `==` because legacy decryption already happens in Login).
//   - `new_password != current_password` — refuse a no-op rotation so
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
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "current password is incorrect", Code: codeWrongPassword})
		return
	}

	// Verify current password using the same dual-read order as Login.
	currentOK := false
	if user.PasswordHash != "" && h.authService.VerifyBcrypt(user.PasswordHash, req.CurrentPassword) {
		currentOK = true
	} else if len(user.Password) > 0 && h.authService.VerifyLegacyAES(user.Password, req.CurrentPassword) {
		currentOK = true
	}
	if !currentOK {
		log.Warn("change_password_failed", "user_id", user.ID, "reason", "invalid_current_password")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "current password is incorrect", Code: codeWrongPassword})
		return
	}

	newHash, err := h.authService.HashPassword(req.NewPassword)
	if err != nil {
		log.Error("change_password_failed", "user_id", user.ID, "reason", "hash_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not hash new password"})
		return
	}
	encrypted, err := h.authService.EncryptPassword(req.NewPassword)
	if err != nil {
		log.Error("change_password_failed", "user_id", user.ID, "reason", "encrypt_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not encrypt new password"})
		return
	}

	if err := h.db.Model(&user).Updates(map[string]interface{}{
		"password":      encrypted,
		"password_hash": newHash,
	}).Error; err != nil {
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
//   - "sent"    — the email was handed off to Resend successfully.
//   - "pending" — the service isn't configured (RESEND_API_KEY empty);
//     the request is still accepted so the mobile UI can
//     show "we got your request" without surfacing config
//     problems to end users.
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
//   - Must exist in email_verification_tokens.
//   - used_at IS NULL.
//   - expires_at > NOW().
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
		// Use the Go field name so GORM resolves the `gorm:"column:emailVerified"`
		// tag from the User model — passing the raw column name would
		// generate `SET emailVerified = ?` which Postgres lower-cases
		// unless quoted, silently updating zero rows.
		if err := tx.Model(&models.User{}).
			Where("id = ?", token.UserID).
			UpdateColumn("EmailVerified", true).Error; err != nil {
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
	// Email (register only) lets the app learn the address is taken
	// before an SMS is paid for.
	Email string `json:"email"`
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
	phone := normalizePhone(req.Phone)
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid phone number", Code: codeInvalidPhone})
		return
	}

	// Answer what we can before spending an SMS.
	if req.Type == "register" && strings.TrimSpace(req.Email) != "" && h.emailTaken(req.Email) {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "email already registered", Code: codeEmailTaken})
		return
	}
	if req.Type == "recovery" {
		var n int64
		h.db.Model(&models.User{}).Where(phoneMatchSQL+" AND status = ?", phone, phone, "T").Count(&n)
		if n == 0 {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "no account with this phone number", Code: codeUserNotFound})
			return
		}
	}

	// Per-phone send rate-limit. Both windows must allow.
	for _, l := range []*middleware.RateLimiter{otpSendBurstLimiter, otpSendDailyLimiter} {
		if !l.Allow(phone) {
			wait := int(l.RetryAfter(phone).Seconds()) + 1
			log.Warn("otp_send_rate_limited", "phone", phone, "retry_after", wait)
			writeJSON(w, http.StatusTooManyRequests, ErrorResponse{
				Error: "please wait before requesting another code", Code: codeRateLimited, RetryAfter: wait,
			})
			return
		}
	}

	// 6-digit code from crypto/rand. The 4-digit code we used to
	// generate had only 10⁴ entropy; wrong guesses lock the code after
	// otpMaxTries.
	code, err := numericCode(6)
	if err != nil {
		log.Error("otp_send_failed", "reason", "rand", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to generate code"})
		return
	}

	otp := models.OTP{
		Phone:     phone,
		Code:      code,
		Type:      req.Type,
		ExpiresAt: time.Now().Add(otpLifetime),
		CreatedAt: time.Now(),
	}

	if err := h.db.Create(&otp).Error; err != nil {
		log.Error("otp_send_failed", "phone", phone, "reason", "db_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create OTP"})
		return
	}

	// Send SMS
	msg := fmt.Sprintf("VetApp: თქვენი კოდია %s", code)
	if err := h.smsService.Send(smsDestination(phone), msg); err != nil {
		log.Error("otp_send_failed", "phone", phone, "reason", "sms_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to send SMS"})
		return
	}

	log.Info("otp_sent", "phone", phone, "type", req.Type, "otp_id", otp.ID)

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

	// Whole-process verify rate limit (catches OTPID enumeration).
	if !otpVerifyLimiter.Allow(fmt.Sprintf("%d", req.OTPID)) {
		log.Warn("otp_verify_rate_limited", "otp_id", req.OTPID)
		writeJSON(w, http.StatusTooManyRequests, ErrorResponse{Error: "too many attempts", Code: codeRateLimited, RetryAfter: 60})
		return
	}

	// Checking a code does not use it up: the step that needs it
	// (register, password-reset) consumes it. Marking it used here made
	// the reset that follows always fail.
	otp, status, otpErr := checkOTP(h.db, req.OTPID, req.Code, "", "")
	if otpErr != nil {
		log.Warn("otp_verify_failed", "otp_id", req.OTPID, "reason", otpErr.Code)
		writeJSON(w, status, *otpErr)
		return
	}

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

	otp, status, otpErr := checkOTP(h.db, req.OTPID, req.OTPCode, "recovery", "")
	if otpErr != nil {
		log.Warn("password_reset_failed", "otp_id", req.OTPID, "reason", otpErr.Code)
		writeJSON(w, status, *otpErr)
		return
	}

	// Find user by phone. Older rows hold "+995…" or "995…"; several
	// legacy accounts can share a number — reset the one used last.
	phone := normalizePhone(otp.Phone)
	var user models.User
	if err := h.db.Where(phoneMatchSQL+" AND status = ?", phone, phone, "T").
		Order("last_login DESC NULLS LAST, id DESC").First(&user).Error; err != nil {
		log.Warn("password_reset_failed", "phone", otp.Phone, "reason", "user_not_found")
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "user not found", Code: codeUserNotFound})
		return
	}

	// Bcrypt is preferred; AES is dual-written for migration-window safety.
	newHash, err := h.authService.HashPassword(req.NewPassword)
	if err != nil {
		log.Error("password_reset_failed", "user_id", user.ID, "reason", "hash_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to hash password"})
		return
	}
	encrypted, err := h.authService.EncryptPassword(req.NewPassword)
	if err != nil {
		log.Error("password_reset_failed", "user_id", user.ID, "reason", "encrypt_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to encrypt password"})
		return
	}

	// Using the code and changing the password commit together, so one
	// code resets one password once.
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if !consumeOTP(tx, otp.ID) {
			return errOTPUsed
		}
		return tx.Model(&user).Updates(map[string]interface{}{
			"password":      encrypted,
			"password_hash": newHash,
		}).Error
	})
	if err == errOTPUsed {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "code expired or already used", Code: codeOTPExpired})
		return
	}
	if err != nil {
		log.Error("password_reset_failed", "user_id", user.ID, "reason", "db_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update password"})
		return
	}

	log.Info("password_reset", "user_id", user.ID, "phone", otp.Phone)

	writeJSON(w, http.StatusOK, MessageResponse{Message: "password reset successfully"})
}

// otpMaxTries is an OTP's attempt budget: every check (right or wrong)
// spends one, atomically, so parallel guesses cannot exceed it. A
// recovery uses two (verify, then reset).
const otpMaxTries = 8

// otpLifetime is how long a sent code stays valid: long enough to read
// the SMS and, for recovery, pick a new password (the code is consumed
// by the reset, not by /otp/verify).
const otpLifetime = 5 * time.Minute

var errOTPUsed = fmt.Errorf("otp already used")

// checkOTP validates a code for purpose typ ("" = any). A wrong code
// counts against the OTP. With phone set, the OTP must have been sent to
// that (normalized) number. On failure it returns the status and body to
// send.
func checkOTP(db *gorm.DB, otpID uint, code, typ, phone string) (models.OTP, int, *ErrorResponse) {
	invalid := &ErrorResponse{Error: "invalid code", Code: codeOTPInvalid}
	var otp models.OTP
	if otpID == 0 || strings.TrimSpace(code) == "" || db.First(&otp, otpID).Error != nil {
		return otp, http.StatusUnauthorized, invalid
	}
	if (typ != "" && otp.Type != typ) || (phone != "" && normalizePhone(otp.Phone) != phone) {
		return otp, http.StatusUnauthorized, invalid
	}
	expired := &ErrorResponse{Error: "code expired or already used", Code: codeOTPExpired}
	// expires_at is compared with a Go time, the way it was written: the
	// column has no time zone, so NOW() would be off by the session zone.
	spend := db.Exec(`UPDATE otp_codes SET tries = tries + 1
		WHERE id = ? AND used = false AND expires_at > ? AND tries < ?`, otp.ID, time.Now(), otpMaxTries)
	if spend.Error != nil || spend.RowsAffected != 1 {
		return otp, http.StatusUnauthorized, expired
	}
	if subtle.ConstantTimeCompare([]byte(otp.Code), []byte(strings.TrimSpace(code))) != 1 {
		return otp, http.StatusUnauthorized, invalid
	}
	return otp, 0, nil
}

// consumeOTP marks the OTP used; false if another request got there
// first or it expired meanwhile.
func consumeOTP(tx *gorm.DB, id uint) bool {
	res := tx.Exec(`UPDATE otp_codes SET used = true WHERE id = ? AND used = false AND expires_at > ?`, id, time.Now())
	return res.Error == nil && res.RowsAffected == 1
}

// phoneOnRecordFor reports whether phone may claim personalID: true when
// no pet under the ID has a usable phone, or one of them is this phone.
func phoneOnRecordFor(db *gorm.DB, personalID, phone string) bool {
	var phones []string
	db.Raw(`SELECT DISTINCT phone FROM pets WHERE TRIM(uuid) = ? AND TRIM(COALESCE(phone, '')) <> ''`, personalID).Scan(&phones)
	usable := false
	for _, p := range phones {
		n := normalizePhone(p)
		if n == phone {
			return true
		}
		usable = usable || n != ""
	}
	return !usable
}

// emailTaken reports whether any account already uses this address.
func (h *AuthHandler) emailTaken(email string) bool {
	var n int64
	h.db.Model(&models.User{}).Where("LOWER(TRIM(email)) = LOWER(TRIM(?))", email).Count(&n)
	return n > 0
}

// --- Helpers ---

// writeJSON writes a JSON response with the given status code. Encode
// errors fire after the header has been written, so we can't recover —
// just log so the failure isn't silent.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Warn("writejson_encode_failed", "status", status, "error", err)
	}
}
