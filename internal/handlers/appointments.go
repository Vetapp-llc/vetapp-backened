package handlers

import (
	"encoding/json"
	"net/http"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// AppointmentHandler handles appointment endpoints.
type AppointmentHandler struct {
	db *gorm.DB
}

// NewAppointmentHandler creates a new AppointmentHandler.
func NewAppointmentHandler(db *gorm.DB) *AppointmentHandler {
	return &AppointmentHandler{db: db}
}

// --- Request/Response types ---

// CreateAppointmentRequest is the request body for booking an appointment.
type CreateAppointmentRequest struct {
	UUID   string `json:"uuid" validate:"required"`   // Pet ID
	Date   string `json:"date" validate:"required"`   // Date (YYYY-MM-DD)
	Time   string `json:"time"`                       // Time slot
	PName  string `json:"pname"`                      // Pet name
	Owner  string `json:"owner"`                      // Owner personal ID
	OwnerN string `json:"ownern"`                     // Owner name
	Phone  string `json:"phone"`                      // Owner phone
	TPName string `json:"tpname"`                     // Procedure type
	Koment string `json:"koment"`                     // Notes
	Status string `json:"status"`                     // Status
}

// AssignSlotRequest is the request body for assigning a time slot.
type AssignSlotRequest struct {
	Time    string `json:"time" validate:"required"`
	VetName string `json:"vetname"`
}

// AppointmentResponse is the API response for an appointment.
type AppointmentResponse struct {
	ID      uint   `json:"id"`
	UUID    string `json:"uuid"`
	Date    string `json:"date"`
	Time    string `json:"time"`
	VetName string `json:"vetname"`
	PName   string `json:"pname"`
	Owner   string `json:"owner"`
	OwnerN  string `json:"ownern"`
	Phone   string `json:"phone"`
	TPName  string `json:"tpname"`
	Koment  string `json:"koment"`
	Status  string `json:"status"`
}

// TimeSlot represents an available time slot.
type TimeSlot struct {
	Time      string `json:"time"`
	Available bool   `json:"available"`
}

func appointmentToResponse(a models.Appointment) AppointmentResponse {
	return AppointmentResponse{
		ID: a.ID, UUID: a.UUID, Date: a.Date, Time: a.Time,
		VetName: a.VetName, PName: a.PName, Owner: a.Owner,
		OwnerN: a.OwnerN, Phone: a.Phone, TPName: a.TPName,
		Koment: a.Koment, Status: a.Status,
	}
}

// --- Handlers ---

// List returns appointments filtered by query params.
// @Summary List appointments
// @Tags appointments
// @Produce json
// @Security BearerAuth
// @Param clinic query string false "Clinic code"
// @Param date_from query string false "Start date (YYYY-MM-DD)"
// @Param date_to query string false "End date (YYYY-MM-DD)"
// @Param vet_id query string false "Vet member ID"
// @Success 200 {array} AppointmentResponse
// @Failure 500 {object} ErrorResponse
// @Router /appointments [get]
func (h *AppointmentHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	query := h.db.Model(&models.Appointment{})

	clinic := r.URL.Query().Get("clinic")
	if clinic == "" {
		clinic = claims.Zip
	}
	query = query.Where("sk = ?", clinic)

	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		query = query.Where("date >= ?", dateFrom)
	}
	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		query = query.Where("date <= ?", dateTo)
	}
	if vetID := r.URL.Query().Get("vet_id"); vetID != "" {
		query = query.Where("vetname = ?", vetID)
	}

	var appointments []models.Appointment
	if err := query.Order("date DESC, time ASC").Find(&appointments).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch appointments"})
		return
	}

	items := make([]AppointmentResponse, len(appointments))
	for i, a := range appointments {
		items[i] = appointmentToResponse(a)
	}

	writeJSON(w, http.StatusOK, items)
}

// Create books a new appointment.
// @Summary Book appointment
// @Tags appointments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreateAppointmentRequest true "Appointment data"
// @Success 201 {object} AppointmentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /appointments [post]
func (h *AppointmentHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var req CreateAppointmentRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	appt := models.Appointment{
		UUID:    req.UUID,
		Date:    req.Date,
		Time:    req.Time,
		SK:      claims.Zip,
		VetName: formatUint(claims.UserID),
		PName:   req.PName,
		Owner:   req.Owner,
		OwnerN:  req.OwnerN,
		Phone:   req.Phone,
		TPName:  req.TPName,
		Koment:  req.Koment,
		Status:  req.Status,
	}

	if err := h.db.Create(&appt).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create appointment"})
		return
	}

	writeJSON(w, http.StatusCreated, appointmentToResponse(appt))
}

// Update edits an existing appointment.
// @Summary Update appointment
// @Tags appointments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Appointment ID"
// @Param body body object true "Fields to update"
// @Success 200 {object} AppointmentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /appointments/{id} [put]
func (h *AppointmentHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var appt models.Appointment
	if err := h.db.First(&appt, id).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "appointment not found"})
		return
	}

	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	delete(updates, "id")
	delete(updates, "sk")

	if err := h.db.Model(&appt).Updates(updates).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update appointment"})
		return
	}

	// Reload for response
	h.db.First(&appt, id)
	writeJSON(w, http.StatusOK, appointmentToResponse(appt))
}

// Delete cancels an appointment.
// @Summary Cancel appointment
// @Tags appointments
// @Produce json
// @Security BearerAuth
// @Param id path int true "Appointment ID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /appointments/{id} [delete]
func (h *AppointmentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var appt models.Appointment
	if err := h.db.First(&appt, id).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "appointment not found"})
		return
	}

	if err := h.db.Delete(&appt).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete appointment"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "appointment cancelled"})
}

// Slots returns available time slots for a given date and clinic.
// @Summary Get available time slots
// @Tags appointments
// @Produce json
// @Security BearerAuth
// @Param clinic query string false "Clinic code"
// @Param vet_id query string false "Vet member ID"
// @Param date query string true "Date (YYYY-MM-DD)"
// @Success 200 {array} TimeSlot
// @Failure 400 {object} ErrorResponse
// @Router /appointments/slots [get]
func (h *AppointmentHandler) Slots(w http.ResponseWriter, r *http.Request) {
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

	// Get booked slots for the day
	query := h.db.Model(&models.Appointment{}).Where("sk = ? AND date = ?", clinic, date)
	if vetID := r.URL.Query().Get("vet_id"); vetID != "" {
		query = query.Where("vetname = ?", vetID)
	}

	var booked []models.Appointment
	query.Find(&booked)

	bookedTimes := make(map[string]bool)
	for _, a := range booked {
		if a.Time != "" {
			bookedTimes[a.Time] = true
		}
	}

	// Generate 30-minute slots from 09:00 to 18:00
	allSlots := []string{
		"09:00", "09:30", "10:00", "10:30", "11:00", "11:30",
		"12:00", "12:30", "13:00", "13:30", "14:00", "14:30",
		"15:00", "15:30", "16:00", "16:30", "17:00", "17:30",
	}

	slots := make([]TimeSlot, len(allSlots))
	for i, t := range allSlots {
		slots[i] = TimeSlot{Time: t, Available: !bookedTimes[t]}
	}

	writeJSON(w, http.StatusOK, slots)
}

// AssignSlot assigns a time slot to an appointment.
// @Summary Assign time slot
// @Tags appointments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Appointment ID"
// @Param body body AssignSlotRequest true "Slot assignment"
// @Success 200 {object} AppointmentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /appointments/{id}/slot [put]
func (h *AppointmentHandler) AssignSlot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var appt models.Appointment
	if err := h.db.First(&appt, id).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "appointment not found"})
		return
	}

	var req AssignSlotRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	updates := map[string]interface{}{"time": req.Time}
	if req.VetName != "" {
		updates["vetname"] = req.VetName
	}

	if err := h.db.Model(&appt).Updates(updates).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to assign slot"})
		return
	}

	h.db.First(&appt, id)
	writeJSON(w, http.StatusOK, appointmentToResponse(appt))
}
