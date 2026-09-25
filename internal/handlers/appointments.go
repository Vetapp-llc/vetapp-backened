package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// AppointmentHandler handles appointment endpoints — booking a procedure
// for a later day (vet/addoperationdate.php), the booked list
// (vet/operationdate.php), picking a time (vet/timeslot.php) and
// cancelling (vet/delate.php).
type AppointmentHandler struct {
	db *gorm.DB
}

// NewAppointmentHandler creates a new AppointmentHandler.
func NewAppointmentHandler(db *gorm.DB) *AppointmentHandler {
	return &AppointmentHandler{db: db}
}

// appointmentSlots is the PHP timeslot grid: every half hour, 08:00–21:00.
var appointmentSlots = func() []string {
	out := make([]string, 0, 27)
	for m := 8 * 60; m <= 21*60; m += 30 {
		out = append(out, fmt.Sprintf("%02d:%02d", m/60, m%60))
	}
	return out
}()

func isSlot(t string) bool {
	for _, s := range appointmentSlots {
		if s == t {
			return true
		}
	}
	return false
}

var errSlotTaken = errors.New("slot taken")

// --- Request/Response types ---

// CreateAppointmentRequest is the request body for booking an appointment.
type CreateAppointmentRequest struct {
	UUID    string `json:"uuid"`                     // Pet ID; optional for a walk-in description
	Owner   string `json:"owner"`                    // Owner personal ID: proof of lookup for a pet of another clinic
	Date    string `json:"date" validate:"required"` // Appointment day (YYYY-MM-DD)
	Time    string `json:"time"`                     // Optional HH:MM slot
	VetName string `json:"vetname"`                  // Vet member ID; defaults to the caller
	PName   string `json:"pname"`                    // Free-text pet description when no pet id
	TPName  string `json:"tpname" validate:"required"`
	Price   string `json:"price"`
	Koment  string `json:"koment"`
}

// UpdateAppointmentRequest is a partial update; omitted fields keep their value.
type UpdateAppointmentRequest struct {
	Date    *string `json:"date"`
	Time    *string `json:"time"`
	VetName *string `json:"vetname"`
	TPName  *string `json:"tpname"`
	Price   *string `json:"price"`
	Koment  *string `json:"koment"`
}

// AssignSlotRequest is the request body for assigning a time slot.
type AssignSlotRequest struct {
	Time    string `json:"time" validate:"required"`
	VetName string `json:"vetname"`
}

// AppointmentResponse is the API response for an appointment.
type AppointmentResponse struct {
	ID       uint   `json:"id" validate:"required"`
	UUID     string `json:"uuid" validate:"required"`
	Date     string `json:"date" validate:"required"`
	BookedOn string `json:"booked_on" validate:"required"`
	Time     string `json:"time" validate:"required"`
	VetName  string `json:"vetname" validate:"required"`
	PName    string `json:"pname" validate:"required"`
	Owner    string `json:"owner" validate:"required"`
	OwnerN   string `json:"ownern" validate:"required"`
	TPName   string `json:"tpname" validate:"required"`
	Price    string `json:"price" validate:"required"`
	Koment   string `json:"koment" validate:"required"`
}

// TimeSlot represents an available time slot.
type TimeSlot struct {
	Time      string `json:"time" validate:"required"`
	Available bool   `json:"available" validate:"required"`
}

func appointmentToResponse(a models.Appointment) AppointmentResponse {
	return AppointmentResponse{
		ID: a.ID, UUID: a.UUID, Date: a.Date, BookedOn: a.BookedOn, Time: a.Time,
		VetName: a.VetName, PName: a.PName, Owner: a.Owner, OwnerN: a.OwnerN,
		TPName: a.TPName, Price: a.Price, Koment: cleanText(a.Koment),
	}
}

// --- Handlers ---

// List returns the clinic's appointments for a range of appointment days.
func (h *AppointmentHandler) List(w http.ResponseWriter, r *http.Request) {
	clinic, ok := resolveClinic(w, r)
	if !ok {
		return
	}
	query := h.db.Model(&models.Appointment{}).Where("sk = ?", clinic)
	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		query = query.Where("date2 >= ?", dateFrom)
	}
	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		query = query.Where("date2 <= ?", dateTo)
	}
	if vetID := r.URL.Query().Get("vet_id"); vetID != "" {
		query = query.Where("vetname = ?", vetID)
	}
	if petID := r.URL.Query().Get("pet_id"); petID != "" {
		query = query.Where("uuid = ?", petID)
	}

	page := ParsePageParams(r)
	var total int64
	query.Count(&total)

	var appointments []models.Appointment
	if err := page.Paginate(query).Order("date2 ASC, time ASC, id ASC").Find(&appointments).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch appointments"})
		return
	}

	items := make([]AppointmentResponse, len(appointments))
	for i, a := range appointments {
		items[i] = appointmentToResponse(a)
	}
	writeJSON(w, http.StatusOK, NewPaginatedResponse(items, total, page))
}

// Create books an appointment.
func (h *AppointmentHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var req CreateAppointmentRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if claims.Zip == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "this account has no clinic"})
		return
	}
	if !isISODate(req.Date) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date must be YYYY-MM-DD"})
		return
	}
	if req.Time != "" && !isSlot(req.Time) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "time must be a half-hour slot between 08:00 and 21:00"})
		return
	}
	if strings.TrimSpace(req.TPName) == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "tpname is required"})
		return
	}
	vet, ok := clinicVet(h.db, claims.Zip, claims.UserID, req.VetName)
	if !ok {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "vet is not a member of this clinic"})
		return
	}

	appt := models.Appointment{
		Date: req.Date, BookedOn: todayGeorgia(), Date3: legacyDate3(req.Date),
		Time: req.Time, SK: claims.Zip, VetName: vet,
		PName: req.PName, TPName: strings.TrimSpace(req.TPName), Koment: req.Koment, Price: req.Price,
	}
	if req.UUID != "" {
		if !canAccessPetWithProof(h.db, r, req.UUID, req.Owner) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
			return
		}
		var pet models.Pet
		if err := h.db.Where("id = ?", req.UUID).First(&pet).Error; err != nil {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
			return
		}
		appt.UUID = strconv.Itoa(int(pet.ID))
		appt.PName, appt.Owner, appt.OwnerN = pet.Name, pet.UUID, pet.FirstName
		appt.Sax, appt.Species = pet.Sex, pet.Pet
	} else if strings.TrimSpace(req.PName) == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "a pet or a description is required"})
		return
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := lockVetDay(tx, appt); err != nil {
			return err
		}
		return tx.Create(&appt).Error
	})
	if errors.Is(err, errSlotTaken) {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "this time is already booked for the vet"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create appointment"})
		return
	}
	writeJSON(w, http.StatusCreated, appointmentToResponse(appt))
}

// lockVetDay serialises bookings for one vet on one day and fails with
// errSlotTaken if appt's time is already booked. Must run inside a
// transaction: the advisory lock is released at commit.
//
// A unique index would be simpler, but production already holds
// duplicate bookings from the PHP era, so one cannot be created.
func lockVetDay(tx *gorm.DB, appt models.Appointment) error {
	if appt.Time == "" {
		return nil
	}
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(? || '|' || ? || '|' || ?))`,
		appt.SK, appt.VetName, appt.Date).Error; err != nil {
		return err
	}
	var n int64
	tx.Model(&models.Appointment{}).
		Where("sk = ? AND vetname = ? AND date2 = ? AND time = ? AND id <> ?", appt.SK, appt.VetName, appt.Date, appt.Time, appt.ID).
		Count(&n)
	if n > 0 {
		return errSlotTaken
	}
	return nil
}

// clinicVet resolves the vet an appointment or procedure is attributed to:
// the caller when empty, otherwise an active vet at the same clinic.
func clinicVet(db *gorm.DB, clinic string, caller uint, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == formatUint(caller) {
		return formatUint(caller), true
	}
	var n int64
	db.Model(&models.User{}).
		Where("id = ? AND zip = ? AND group_id = ? AND status = ?", requested, clinic, models.RoleVet, "T").
		Count(&n)
	return requested, n > 0
}

func (h *AppointmentHandler) findOwn(r *http.Request) (models.Appointment, bool) {
	var appt models.Appointment
	err := ownedByCaller(h.db, r, "sk").Where("id = ?", chi.URLParam(r, "id")).First(&appt).Error
	return appt, err == nil
}

// Update edits an appointment. The pet, owner and clinic are fixed.
func (h *AppointmentHandler) Update(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	appt, ok := h.findOwn(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "appointment not found"})
		return
	}
	var req UpdateAppointmentRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if req.Date != nil {
		if !isISODate(*req.Date) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date must be YYYY-MM-DD"})
			return
		}
		appt.Date, appt.Date3 = *req.Date, legacyDate3(*req.Date)
	}
	if req.Time != nil {
		if *req.Time != "" && !isSlot(*req.Time) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "time must be a half-hour slot between 08:00 and 21:00"})
			return
		}
		appt.Time = *req.Time
	}
	if req.VetName != nil {
		vet, valid := clinicVet(h.db, appt.SK, claims.UserID, *req.VetName)
		if !valid {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "vet is not a member of this clinic"})
			return
		}
		appt.VetName = vet
	}
	if req.TPName != nil {
		if strings.TrimSpace(*req.TPName) == "" {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "tpname is required"})
			return
		}
		appt.TPName = strings.TrimSpace(*req.TPName)
	}
	if req.Price != nil {
		appt.Price = *req.Price
	}
	if req.Koment != nil {
		appt.Koment = *req.Koment
	}
	h.save(w, appt)
}

// AssignSlot assigns a time (and optionally a vet) to an appointment —
// the second step of the PHP booking flow (vet/timeslot.php).
func (h *AppointmentHandler) AssignSlot(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	appt, ok := h.findOwn(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "appointment not found"})
		return
	}
	var req AssignSlotRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if !isSlot(req.Time) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "time must be a half-hour slot between 08:00 and 21:00"})
		return
	}
	if req.VetName != "" {
		vet, valid := clinicVet(h.db, appt.SK, claims.UserID, req.VetName)
		if !valid {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "vet is not a member of this clinic"})
			return
		}
		appt.VetName = vet
	}
	appt.Time = req.Time
	h.save(w, appt)
}

func (h *AppointmentHandler) save(w http.ResponseWriter, appt models.Appointment) {
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := lockVetDay(tx, appt); err != nil {
			return err
		}
		return tx.Model(&appt).Select("date2", "date3", "time", "vetname", "operation", "price", "coment").Updates(&appt).Error
	})
	if errors.Is(err, errSlotTaken) {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "this time is already booked for the vet"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update appointment"})
		return
	}
	writeJSON(w, http.StatusOK, appointmentToResponse(appt))
}

// Delete cancels an appointment.
func (h *AppointmentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	appt, ok := h.findOwn(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "appointment not found"})
		return
	}
	if err := h.db.Delete(&appt).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete appointment"})
		return
	}
	writeJSON(w, http.StatusOK, MessageResponse{Message: "appointment cancelled"})
}

// Slots returns the day's time grid for one vet, marking booked times —
// vet/timeslot.php checks availability per vet, not per clinic.
func (h *AppointmentHandler) Slots(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	clinic, ok := resolveClinic(w, r)
	if !ok {
		return
	}
	date := r.URL.Query().Get("date")
	if !isISODate(date) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date must be YYYY-MM-DD"})
		return
	}
	vet := r.URL.Query().Get("vet_id")
	if vet == "" {
		vet = formatUint(claims.UserID)
	}

	var booked []string
	h.db.Model(&models.Appointment{}).
		Where("sk = ? AND vetname = ? AND date2 = ? AND time <> ''", clinic, vet, date).
		Pluck("time", &booked)
	taken := make(map[string]bool, len(booked))
	for _, t := range booked {
		taken[t] = true
	}

	slots := make([]TimeSlot, len(appointmentSlots))
	for i, t := range appointmentSlots {
		slots[i] = TimeSlot{Time: t, Available: !taken[t]}
	}
	writeJSON(w, http.StatusOK, slots)
}
