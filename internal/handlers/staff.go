package handlers

import (
	"net/http"
	"strings"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"
	"vetapp-backend/internal/services"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// StaffHandler handles clinic staff management endpoints.
type StaffHandler struct {
	db          *gorm.DB
	authService *services.AuthService
}

// NewStaffHandler creates a new StaffHandler.
func NewStaffHandler(db *gorm.DB, authService *services.AuthService) *StaffHandler {
	return &StaffHandler{db: db, authService: authService}
}

// --- Request/Response types ---

// CreateStaffRequest is the request body for adding a staff member.
type CreateStaffRequest struct {
	FirstName string `json:"first_name" validate:"required"`
	LastName  string `json:"last_name" validate:"required"`
	Email     string `json:"email" validate:"required,email"`
	Phone     string `json:"phone"`
	Password  string `json:"password" validate:"required,min=6"`
}

// StaffResponse is the API response for a staff member.
type StaffResponse struct {
	ID        uint   `json:"id" validate:"required"`
	FirstName string `json:"first_name" validate:"required"`
	LastName  string `json:"last_name" validate:"required"`
	Email     string `json:"email" validate:"required"`
	Phone     string `json:"phone" validate:"required"`
	Status    string `json:"status" validate:"required"`
}

func staffToResponse(u models.User) StaffResponse {
	return StaffResponse{
		ID: u.ID, FirstName: u.FirstName, LastName: u.LastName,
		Email: u.Email, Phone: u.Phone, Status: u.Status,
	}
}

// --- Handlers ---

// List returns staff members for a clinic.
// @Summary List staff
// @Tags staff
// @Produce json
// @Security BearerAuth
// @Param clinic query string false "Clinic code"
// @Success 200 {array} StaffResponse
// @Failure 500 {object} ErrorResponse
// @Router /staff [get]
func (h *StaffHandler) List(w http.ResponseWriter, r *http.Request) {
	clinic, ok := resolveClinic(w, r)
	if !ok {
		return
	}

	// Removed staff keep their rows (see Delete) but are not listed.
	var users []models.User
	if err := h.db.Where("zip = ? AND group_id = ? AND status = ?", clinic, models.RoleVet, "T").
		Order("first_name ASC").Find(&users).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch staff"})
		return
	}

	items := make([]StaffResponse, len(users))
	for i, u := range users {
		items[i] = staffToResponse(u)
	}

	writeJSON(w, http.StatusOK, items)
}

// Create adds a new staff member (creates a user with group_id=2).
func (h *StaffHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var req CreateStaffRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if claims.Zip == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "this account has no clinic"})
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	if h.emailTaken(req.Email, 0) {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "email already registered"})
		return
	}

	// Both encodings: the AES blob is what the PHP app (and the MySQL →
	// Supabase sync) understands; bcrypt is what this backend prefers.
	encryptedBytes, err := h.authService.EncryptPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to encrypt password"})
		return
	}
	hash, err := h.authService.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to encrypt password"})
		return
	}

	user := models.User{
		FirstName:    strings.TrimSpace(req.FirstName),
		LastName:     strings.TrimSpace(req.LastName),
		Email:        req.Email,
		Phone:        req.Phone,
		Password:     encryptedBytes,
		PasswordHash: hash,
		GroupID:      models.RoleVet,
		Zip:          claims.Zip,
		Status:       "T",
	}

	if err := h.db.Create(&user).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create staff member"})
		return
	}

	writeJSON(w, http.StatusCreated, staffToResponse(user))
}

// emailTaken reports whether an active account other than exceptID uses
// the address. Removed staff (status F, email prefixed) do not count.
func (h *StaffHandler) emailTaken(email string, exceptID uint) bool {
	var n int64
	h.db.Model(&models.User{}).
		Where("LOWER(email) = ? AND status = ? AND id <> ?", strings.ToLower(email), "T", exceptID).
		Count(&n)
	return n > 0
}

// UpdateStaffRequest mirrors vet/updatevet.php: name, personal ID, phone
// and email. Password, role, clinic and status are never editable here.
type UpdateStaffRequest struct {
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
	Phone     *string `json:"phone"`
	Email     *string `json:"email" validate:"omitempty,email"`
}

func (h *StaffHandler) findStaff(r *http.Request) (models.User, bool) {
	var user models.User
	err := ownedByCaller(h.db, r, "zip").
		Where("id = ? AND group_id = ? AND status = ?", chi.URLParam(r, "id"), models.RoleVet, "T").
		First(&user).Error
	return user, err == nil
}

// Update edits an existing staff member.
func (h *StaffHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := h.findStaff(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "staff member not found"})
		return
	}

	var req UpdateStaffRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if req.FirstName != nil {
		if strings.TrimSpace(*req.FirstName) == "" {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "first_name is required"})
			return
		}
		user.FirstName = strings.TrimSpace(*req.FirstName)
	}
	if req.LastName != nil {
		user.LastName = strings.TrimSpace(*req.LastName)
	}
	if req.Phone != nil {
		user.Phone = strings.TrimSpace(*req.Phone)
	}
	if req.Email != nil {
		email := strings.ToLower(strings.TrimSpace(*req.Email))
		if email != strings.ToLower(user.Email) && h.emailTaken(email, user.ID) {
			writeJSON(w, http.StatusConflict, ErrorResponse{Error: "email already registered"})
			return
		}
		user.Email = email
	}

	if err := h.db.Model(&user).Select("first_name", "last_name", "phone", "email").Updates(&user).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update staff member"})
		return
	}
	writeJSON(w, http.StatusOK, staffToResponse(user))
}

// removedStaffZip and removedEmailPrefix are what vet/delatevet.php
// writes when a clinic removes a vet. The row is kept so every record
// that vet wrote (vaccination.vetname) still resolves to a name.
const (
	removedStaffZip    = "8888888888"
	removedEmailPrefix = "13131313"
)

// Delete removes a staff member from the clinic without deleting the
// account row: the email is prefixed (freeing the address and blocking
// login), the clinic is replaced by PHP's sentinel and the status set
// to F. A hard delete would orphan years of records attributed to them.
func (h *StaffHandler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	user, ok := h.findStaff(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "staff member not found"})
		return
	}
	if user.ID == claims.UserID {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "you cannot remove your own account"})
		return
	}

	err := h.db.Model(&user).Updates(map[string]interface{}{
		"email":  removedEmailPrefix + user.Email,
		"zip":    removedStaffZip,
		"status": "F",
	}).Error
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to remove staff member"})
		return
	}
	middleware.InvalidateAccount(user.ID)

	writeJSON(w, http.StatusOK, MessageResponse{Message: "staff member removed"})
}
