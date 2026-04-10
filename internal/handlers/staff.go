package handlers

import (
	"encoding/json"
	"net/http"

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
	Password  string `json:"password" validate:"required,min=1"`
}

// StaffResponse is the API response for a staff member.
type StaffResponse struct {
	ID        uint   `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	Status    string `json:"status"`
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
	claims := middleware.GetClaims(r)

	clinic := r.URL.Query().Get("clinic")
	if clinic == "" {
		clinic = claims.Zip
	}

	var users []models.User
	if err := h.db.Where("zip = ? AND group_id = ?", clinic, models.RoleVet).
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
// @Summary Add staff member
// @Tags staff
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreateStaffRequest true "Staff data"
// @Success 201 {object} StaffResponse
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /staff [post]
func (h *StaffHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var req CreateStaffRequest
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

	encryptedBytes, err := h.authService.EncryptPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to encrypt password"})
		return
	}

	user := models.User{
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Email:     req.Email,
		Phone:     req.Phone,
		Password:  encryptedBytes,
		GroupID:   models.RoleVet,
		Zip:       claims.Zip,
		Status:    "T",
	}

	if err := h.db.Create(&user).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create staff member"})
		return
	}

	writeJSON(w, http.StatusCreated, staffToResponse(user))
}

// Update edits an existing staff member.
// @Summary Update staff member
// @Tags staff
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Staff member ID"
// @Param body body object true "Fields to update"
// @Success 200 {object} StaffResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /staff/{id} [put]
func (h *StaffHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	claims := middleware.GetClaims(r)

	var user models.User
	if err := h.db.Where("id = ? AND zip = ? AND group_id = ?", id, claims.Zip, models.RoleVet).
		First(&user).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "staff member not found"})
		return
	}

	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	// Protect sensitive fields
	delete(updates, "id")
	delete(updates, "password")
	delete(updates, "group_id")
	delete(updates, "zip")

	if err := h.db.Model(&user).Updates(updates).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update staff member"})
		return
	}

	h.db.First(&user, id)
	writeJSON(w, http.StatusOK, staffToResponse(user))
}

// Delete removes a staff member.
// @Summary Remove staff member
// @Tags staff
// @Produce json
// @Security BearerAuth
// @Param id path int true "Staff member ID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /staff/{id} [delete]
func (h *StaffHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	claims := middleware.GetClaims(r)

	var user models.User
	if err := h.db.Where("id = ? AND zip = ? AND group_id = ?", id, claims.Zip, models.RoleVet).
		First(&user).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "staff member not found"})
		return
	}

	if err := h.db.Delete(&user).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete staff member"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "staff member removed"})
}
