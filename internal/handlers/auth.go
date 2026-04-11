package handlers

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"
	"vetapp-backend/internal/services"

	"gorm.io/gorm"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	db          *gorm.DB
	authService *services.AuthService
	smsService  *services.SMSService
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(db *gorm.DB, authService *services.AuthService, smsService *services.SMSService) *AuthHandler {
	return &AuthHandler{db: db, authService: authService, smsService: smsService}
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

// UserResponse is the public user profile returned by /auth/me.
type UserResponse struct {
	ID          uint   `json:"id" validate:"required"`
	FirstName   string `json:"first_name" validate:"required"`
	LastName    string `json:"last_name" validate:"required"`
	Email       string `json:"email" validate:"required"`
	Phone       string `json:"phone" validate:"required"`
	Zip         string `json:"zip" validate:"required"`
	GroupID     int    `json:"group_id" validate:"required"`
	CompanyName string `json:"company_name" validate:"required"`
	Status      string `json:"status" validate:"required"`
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
	var req LoginRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Find user by email
	var user models.User
	if err := h.db.Where("email = ?", req.Email).First(&user).Error; err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid credentials"})
		return
	}

	// Decrypt stored password and compare (MySQL AES_ENCRYPT format)
	storedPassword, err := h.authService.DecryptPassword(user.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "authentication error"})
		return
	}

	if storedPassword != req.Password {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid credentials"})
		return
	}

	// Generate token pair
	tokens, err := h.authService.GenerateTokenPair(&user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to generate tokens"})
		return
	}

	// Update last login
	now := time.Now()
	h.db.Model(&user).Update("last_login", now)

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
	var req RegisterRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Check if email already exists
	var existing models.User
	if err := h.db.Where("email = ?", req.Email).First(&existing).Error; err == nil {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "email already registered"})
		return
	}

	// Encrypt password (MySQL AES_ENCRYPT compatible)
	encryptedBytes, err := h.authService.EncryptPassword(req.Password)
	if err != nil {
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
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create user"})
		return
	}

	// Generate tokens
	tokens, err := h.authService.GenerateTokenPair(&user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to generate tokens"})
		return
	}

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
	var req RefreshRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Validate refresh token
	claims, err := h.authService.ValidateRefreshToken(req.RefreshToken)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid or expired refresh token"})
		return
	}

	// Look up user
	var user models.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "user not found"})
		return
	}

	// Generate new token pair
	tokens, err := h.authService.GenerateTokenPair(&user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to generate tokens"})
		return
	}

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
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create OTP"})
		return
	}

	// Send SMS
	msg := fmt.Sprintf("VetApp: თქვენი კოდია %s", code)
	if err := h.smsService.Send(req.Phone, msg); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to send SMS"})
		return
	}

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
	var req OTPVerifyRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	var otp models.OTP
	if err := h.db.First(&otp, req.OTPID).Error; err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid OTP"})
		return
	}

	if otp.Used || time.Now().After(otp.ExpiresAt) {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "OTP expired or already used"})
		return
	}

	if otp.Code != req.Code {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid code"})
		return
	}

	// Mark as used
	h.db.Model(&otp).Update("used", true)

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
	var req PasswordResetRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Verify OTP
	var otp models.OTP
	if err := h.db.First(&otp, req.OTPID).Error; err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid OTP"})
		return
	}

	if otp.Used || time.Now().After(otp.ExpiresAt) || otp.Code != req.OTPCode {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid or expired OTP"})
		return
	}

	// Find user by phone
	var user models.User
	if err := h.db.Where("phone = ?", otp.Phone).First(&user).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "user not found"})
		return
	}

	// Encrypt new password
	encrypted, err := h.authService.EncryptPassword(req.NewPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to encrypt password"})
		return
	}

	if err := h.db.Model(&user).Update("password", encrypted).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update password"})
		return
	}

	// Mark OTP as used
	h.db.Model(&otp).Update("used", true)

	writeJSON(w, http.StatusOK, MessageResponse{Message: "password reset successfully"})
}

// --- Helpers ---

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
