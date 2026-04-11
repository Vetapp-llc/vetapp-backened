package handlers

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// OwnerPortalHandler handles owner-facing (mobile app) endpoints.
type OwnerPortalHandler struct {
	db *gorm.DB
}

// NewOwnerPortalHandler creates a new OwnerPortalHandler.
func NewOwnerPortalHandler(db *gorm.DB) *OwnerPortalHandler {
	return &OwnerPortalHandler{db: db}
}

// --- Response types ---

// OwnerPetItem is the owner's view of a pet, including subscription status.
type OwnerPetItem struct {
	ID                 string  `json:"id" validate:"required"`
	Name               string  `json:"name" validate:"required"`
	Species            string  `json:"species" validate:"required"`
	Breed              string  `json:"breed" validate:"required"`
	Sex                string  `json:"sex" validate:"required"`
	Chip               string  `json:"chip" validate:"required"`
	Birth              *string `json:"birth"`
	Color              string  `json:"color" validate:"required"`
	SubscriptionStatus string  `json:"subscriptionStatus" validate:"required"` // "active", "expired", "unregistered"
	SubscriptionExpiry *string `json:"subscriptionExpiry"`
}

// OwnerPetDetail is the full detail view for a pet from the owner portal.
type OwnerPetDetail struct {
	OwnerPetItem
	UUID      string `json:"uuid" validate:"required"`
	Vet       string `json:"vet" validate:"required"`
	Castrated bool   `json:"castrated" validate:"required"`
	Code      string `json:"code" validate:"required"`
}

// OwnerCreatePetRequest is the request body for an owner adding a pet.
type OwnerCreatePetRequest struct {
	Name    string `json:"name" validate:"required"`
	Pet     string `json:"pet"`
	Sex     string `json:"sex"`
	Variety string `json:"variety"`
	Chip    string `json:"chip"`
	Date    string `json:"date"`
	Color   string `json:"color"`
}

// OwnerProcedureItem is a medical record as seen by the owner.
type OwnerProcedureItem struct {
	ID            string   `json:"id" validate:"required"`
	Date          *string  `json:"date"`
	NextDate      *string  `json:"nextDate"`
	ProcedureType string   `json:"procedureType" validate:"required"`
	ProcedureName string   `json:"procedureName" validate:"required"`
	Diagnosis     string   `json:"diagnosis" validate:"required"`
	Notes         string   `json:"notes" validate:"required"`
	Comment       string   `json:"comment" validate:"required"`
	VetName       string   `json:"vetName" validate:"required"`
	Vaccinations  []string `json:"vaccinations" validate:"required"`
}

// AccessCodeResponse is the response when generating a new access code.
type AccessCodeResponse struct {
	Code string `json:"code" validate:"required"`
}

// CalendarItem is an upcoming procedure grouped by date.
type CalendarItem struct {
	Date    string `json:"date" validate:"required"`
	PetID   string `json:"petId" validate:"required"`
	PetName string `json:"petName" validate:"required"`
	Type    string `json:"type" validate:"required"`
	Name    string `json:"name" validate:"required"`
}

// OwnerVisit is an appointment as seen by the owner.
type OwnerVisit struct {
	ID        uint   `json:"id" validate:"required"`
	Date      string `json:"date" validate:"required"`
	Time      string `json:"time" validate:"required"`
	Operation string `json:"operation" validate:"required"`
	VetName   string `json:"vetName" validate:"required"`
	Status    string `json:"status" validate:"required"`
}

// --- Helpers ---

func subscriptionStatus(pet models.Pet) string {
	if pet.Status >= 2 {
		return "unregistered"
	}
	if pet.Birth2 == "" {
		return "unregistered"
	}
	expiry, err := time.Parse("2006-01-02", pet.Birth2)
	if err != nil {
		return "unregistered"
	}
	if expiry.Before(time.Now()) {
		return "expired"
	}
	return "active"
}

func petToOwnerItem(p models.Pet) OwnerPetItem {
	item := OwnerPetItem{
		ID:                 strconv.Itoa(int(p.ID)),
		Name:               p.Name,
		Species:            p.Pet,
		Breed:              p.Variety,
		Sex:                p.Sex,
		Chip:               p.Chip,
		Color:              p.Color,
		SubscriptionStatus: subscriptionStatus(p),
	}
	if p.Date != "" {
		item.Birth = &p.Date
	}
	if p.Birth2 != "" {
		item.SubscriptionExpiry = &p.Birth2
	}
	return item
}

// ownerPersonalID returns the owner's personal ID from JWT claims.
func ownerPersonalID(r *http.Request) string {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return ""
	}
	return claims.LastName
}

// verifyPetOwnership checks that the pet belongs to the authenticated owner.
func (h *OwnerPortalHandler) verifyPetOwnership(petID string, ownerID string) (*models.Pet, error) {
	var pet models.Pet
	err := h.db.Where("id = ? AND uuid = ?", petID, ownerID).First(&pet).Error
	return &pet, err
}

// --- Handlers ---

// ListPets returns the owner's pets with subscription status.
// @Summary List owner's pets
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Success 200 {array} OwnerPetItem
// @Failure 401 {object} ErrorResponse
// @Router /owner/pets [get]
func (h *OwnerPortalHandler) ListPets(w http.ResponseWriter, r *http.Request) {
	personalID := ownerPersonalID(r)
	if personalID == "" {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}

	var pets []models.Pet
	h.db.Where("uuid = ?", personalID).Order("id DESC").Find(&pets)

	items := make([]OwnerPetItem, len(pets))
	for i, p := range pets {
		items[i] = petToOwnerItem(p)
	}

	writeJSON(w, http.StatusOK, items)
}

// GetPet returns a single pet detail for the owner.
// @Summary Get owner's pet detail
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Success 200 {object} OwnerPetDetail
// @Failure 404 {object} ErrorResponse
// @Router /owner/pets/{id} [get]
func (h *OwnerPortalHandler) GetPet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	personalID := ownerPersonalID(r)

	pet, err := h.verifyPetOwnership(id, personalID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	detail := OwnerPetDetail{
		OwnerPetItem: petToOwnerItem(*pet),
		UUID:         pet.UUID,
		Vet:          pet.Vet,
		Castrated:    pet.Cast != "",
		Code:         pet.Code,
	}

	writeJSON(w, http.StatusOK, detail)
}

// CreatePet adds a new pet for the owner.
// @Summary Owner adds pet
// @Tags owner
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body OwnerCreatePetRequest true "Pet data"
// @Success 201 {object} OwnerPetItem
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /owner/pets [post]
func (h *OwnerPortalHandler) CreatePet(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}

	var req OwnerCreatePetRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Get owner info for denormalized fields
	var user models.User
	h.db.First(&user, claims.UserID)

	pet := models.Pet{
		UUID:      claims.LastName, // Owner personal ID
		Name:      req.Name,
		Pet:       req.Pet,
		Sex:       req.Sex,
		Variety:   req.Variety,
		Chip:      req.Chip,
		Date:      req.Date,
		Color:     req.Color,
		Phone:     user.Phone,
		Email:     user.Email,
		FirstName: user.FirstName,
		Status:    3,    // Unregistered
		Code:      "1313", // Default code
	}

	if err := h.db.Create(&pet).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create pet"})
		return
	}

	writeJSON(w, http.StatusCreated, petToOwnerItem(pet))
}

// UpdatePet edits an owner's pet.
// @Summary Owner updates pet
// @Tags owner
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param body body object true "Fields to update"
// @Success 200 {object} OwnerPetItem
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /owner/pets/{id} [put]
func (h *OwnerPortalHandler) UpdatePet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	personalID := ownerPersonalID(r)

	pet, err := h.verifyPetOwnership(id, personalID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	// Protect sensitive fields
	delete(updates, "id")
	delete(updates, "uuid")
	delete(updates, "vet")
	delete(updates, "status")
	delete(updates, "birth2")
	delete(updates, "code")

	if err := h.db.Model(pet).Updates(updates).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update pet"})
		return
	}

	h.db.First(pet, id)
	writeJSON(w, http.StatusOK, petToOwnerItem(*pet))
}

// Procedures returns medical records for a pet, filtered by type.
// @Summary Get pet procedures (owner view)
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param tp query int false "Procedure type code (999 for allergies)"
// @Success 200 {array} OwnerProcedureItem
// @Failure 404 {object} ErrorResponse
// @Router /owner/pets/{id}/procedures [get]
func (h *OwnerPortalHandler) Procedures(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	personalID := ownerPersonalID(r)

	if _, err := h.verifyPetOwnership(id, personalID); err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	tpStr := r.URL.Query().Get("tp")

	// tp=999 means query allergies table instead
	if tpStr == "999" {
		var allergies []models.Allergy
		h.db.Where("uuid = ?", id).Order("id DESC").Find(&allergies)

		items := make([]OwnerProcedureItem, len(allergies))
		for i, a := range allergies {
			date := a.Date
			items[i] = OwnerProcedureItem{
				ID:            strconv.Itoa(int(a.ID)),
				ProcedureType: "999",
				ProcedureName: a.Name,
			}
			if date != "" {
				items[i].Date = &date
			}
			items[i].Vaccinations = []string{}
		}
		writeJSON(w, http.StatusOK, items)
		return
	}

	query := h.db.Where("uuid = ?", id)
	if tpStr != "" {
		query = query.Where("tp = ?", tpStr)
	}

	var procs []models.Procedure
	query.Order("date DESC, id DESC").Find(&procs)

	items := make([]OwnerProcedureItem, len(procs))
	for i, p := range procs {
		item := OwnerProcedureItem{
			ID:            strconv.Itoa(int(p.ID)),
			ProcedureType: strconv.Itoa(p.TP),
			ProcedureName: p.TPName,
			Diagnosis:     p.Diagn,
			Notes:         p.Nout,
			Comment:       p.Coment, // Visible comment (not internal koment)
			VetName:       p.VetName,
		}
		if p.Date != "" {
			item.Date = &p.Date
		}
		if p.Date2 != "" {
			item.NextDate = &p.Date2
		}
		// Collect vaccinations
		for _, v := range []string{p.Vac, p.Vac1, p.Vac2, p.Vac3, p.Vac4, p.Vac5, p.Vac6, p.Vac7, p.Vac8, p.Vac9} {
			if v != "" {
				item.Vaccinations = append(item.Vaccinations, v)
			}
		}
		if item.Vaccinations == nil {
			item.Vaccinations = []string{}
		}
		items[i] = item
	}

	writeJSON(w, http.StatusOK, items)
}

// GenerateCode creates a new 4-digit access code for the pet.
// @Summary Generate pet access code
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Success 200 {object} AccessCodeResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /owner/pets/{id}/code [get]
func (h *OwnerPortalHandler) GenerateCode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	personalID := ownerPersonalID(r)

	pet, err := h.verifyPetOwnership(id, personalID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	code := strconv.Itoa(1000 + rand.Intn(9000))

	if err := h.db.Model(pet).Update("code", code).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to generate code"})
		return
	}

	writeJSON(w, http.StatusOK, AccessCodeResponse{Code: code})
}

// Calendar returns upcoming procedures for all owner's pets.
// @Summary Owner calendar (upcoming procedures)
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Success 200 {array} CalendarItem
// @Failure 401 {object} ErrorResponse
// @Router /owner/calendar [get]
func (h *OwnerPortalHandler) Calendar(w http.ResponseWriter, r *http.Request) {
	personalID := ownerPersonalID(r)
	if personalID == "" {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}

	// Get all owner's pet IDs
	var pets []models.Pet
	h.db.Where("uuid = ?", personalID).Find(&pets)

	petIDs := make([]string, len(pets))
	petNames := make(map[string]string)
	for i, p := range pets {
		id := strconv.Itoa(int(p.ID))
		petIDs[i] = id
		petNames[id] = p.Name
	}

	if len(petIDs) == 0 {
		writeJSON(w, http.StatusOK, []CalendarItem{})
		return
	}

	today := time.Now().Format("2006-01-02")

	var procs []models.Procedure
	h.db.Where("uuid IN ? AND date2 >= ? AND date3 > '2'", petIDs, today).
		Order("date2 ASC").Find(&procs)

	items := make([]CalendarItem, len(procs))
	for i, p := range procs {
		items[i] = CalendarItem{
			Date:    p.Date2,
			PetID:   p.UUID,
			PetName: petNames[p.UUID],
			Type:    strconv.Itoa(p.TP),
			Name:    p.TPName,
		}
	}

	writeJSON(w, http.StatusOK, items)
}

// Visits returns the owner's upcoming appointments.
// @Summary Owner visits (appointments)
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Success 200 {array} OwnerVisit
// @Failure 401 {object} ErrorResponse
// @Router /owner/visits [get]
func (h *OwnerPortalHandler) Visits(w http.ResponseWriter, r *http.Request) {
	personalID := ownerPersonalID(r)
	if personalID == "" {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}

	var appointments []models.Appointment
	h.db.Where("owner = ?", personalID).Order("date DESC, time ASC").Find(&appointments)

	items := make([]OwnerVisit, len(appointments))
	for i, a := range appointments {
		items[i] = OwnerVisit{
			ID:        a.ID,
			Date:      a.Date,
			Time:      a.Time,
			Operation: a.TPName,
			VetName:   a.VetName,
			Status:    a.Status,
		}
	}

	writeJSON(w, http.StatusOK, items)
}
