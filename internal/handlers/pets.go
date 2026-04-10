package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// PetHandler handles pet CRUD endpoints.
type PetHandler struct {
	db *gorm.DB
}

// NewPetHandler creates a new PetHandler.
func NewPetHandler(db *gorm.DB) *PetHandler {
	return &PetHandler{db: db}
}

// --- Response types matching frontend expectations ---

type PetListItem struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Species    string  `json:"species"`
	Breed      string  `json:"breed"`
	Sex        string  `json:"sex"`
	Chip       string  `json:"chip"`
	OwnerName  string  `json:"ownerName"`
	OwnerPhone string  `json:"ownerPhone"`
	Birth      *string `json:"birth"`
	Color      string  `json:"color"`
}

type PetDetail struct {
	PetListItem
	UUID            string          `json:"uuid"`
	Vet             string          `json:"vet"`
	Date            *string         `json:"date"`
	Variety         string          `json:"variety"`
	Code            string          `json:"code"`
	Status          string          `json:"status"`
	Birth2          *string         `json:"birth2"`
	Castrated       bool            `json:"castrated"`
	CastDate        *string         `json:"castDate"`
	OwnerEmail      string          `json:"ownerEmail"`
	OwnerPersonalId string          `json:"ownerPersonalId"`
	MedicalRecords  []MedicalRecord `json:"medicalRecords"`
}

type MedicalRecord struct {
	ID            string   `json:"id"`
	Date          *string  `json:"date"`
	ProcedureType string   `json:"procedureType"`
	ProcedureName string   `json:"procedureName"`
	Diagnosis     string   `json:"diagnosis"`
	Notes         string   `json:"notes"`
	Comment       string   `json:"comment"`
	VetName       string   `json:"vetName"`
	Price         string   `json:"price"`
	Anamnesis     string   `json:"anamnesis"`
	Vaccinations  []string `json:"vaccinations"`
	Tests         []string `json:"tests"`
}

type PaginatedResponse struct {
	Data       interface{} `json:"data"`
	Total      int64       `json:"total"`
	Page       int         `json:"page"`
	PageSize   int         `json:"pageSize"`
	TotalPages int         `json:"totalPages"`
}

// CreatePetRequest is the request body for creating a pet.
type CreatePetRequest struct {
	UUID      string `json:"uuid" validate:"required"`
	Name      string `json:"name" validate:"required"`
	Pet       string `json:"pet"`
	Sex       string `json:"sex"`
	Variety   string `json:"variety"`
	Chip      string `json:"chip"`
	Date      string `json:"date"`
	Code      string `json:"code"`
	Phone     string `json:"phone"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	Color     string `json:"color"`
}

// CertificateResponse is the response for border crossing certificate.
type CertificateResponse struct {
	Pet             PetListItem   `json:"pet"`
	Vaccination     MedicalRecord `json:"vaccination"`
	Rabies          MedicalRecord `json:"rabies"`
	Dehelminization MedicalRecord `json:"dehelminization"`
	Ectoparasite    MedicalRecord `json:"ectoparasite"`
}

// petToListItem converts a DB pet model to the frontend list response.
func petToListItem(p models.Pet) PetListItem {
	item := PetListItem{
		ID:         strconv.Itoa(int(p.ID)),
		Name:       p.Name,
		Species:    p.Pet,
		Breed:      p.Variety,
		Sex:        p.Sex,
		Chip:       p.Chip,
		OwnerName:  p.FirstName,
		OwnerPhone: p.Phone,
		Color:      p.Color,
	}
	if p.Date != "" {
		item.Birth = &p.Date
	}
	return item
}

// List returns pets filtered by query params.
// @Summary List pets
// @Tags pets
// @Produce json
// @Security BearerAuth
// @Param search query string false "Search by name, phone, chip"
// @Param owner_id query string false "Filter by owner personal ID"
// @Param chip query string false "Filter by microchip"
// @Param page query int false "Page number" default(1)
// @Param pageSize query int false "Page size" default(20)
// @Success 200 {object} PaginatedResponse
// @Failure 500 {object} ErrorResponse
// @Router /pets [get]
func (h *PetHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	query := h.db.Model(&models.Pet{}).Where("vet = ?", claims.Zip)

	// Filter by owner personal ID
	if ownerID := r.URL.Query().Get("owner_id"); ownerID != "" {
		query = query.Where("uuid = ?", ownerID)
	}

	// Filter by microchip
	if chip := r.URL.Query().Get("chip"); chip != "" {
		query = query.Where("chip = ?", chip)
	}

	// Search by name, phone, email, chip
	if search := r.URL.Query().Get("search"); search != "" {
		like := "%" + search + "%"
		query = query.Where("name ILIKE ? OR first_name ILIKE ? OR chip ILIKE ? OR phone ILIKE ?",
			like, like, like, like)
	}

	// Pagination
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 50 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var total int64
	query.Count(&total)

	var pets []models.Pet
	if err := query.Offset(offset).Limit(pageSize).Order("id DESC").Find(&pets).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch pets"})
		return
	}

	items := make([]PetListItem, len(pets))
	for i, p := range pets {
		items[i] = petToListItem(p)
	}

	writeJSON(w, http.StatusOK, PaginatedResponse{
		Data:       items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: int(math.Ceil(float64(total) / float64(pageSize))),
	})
}

// Get returns a single pet by ID with medical records.
// @Summary Get pet by ID
// @Tags pets
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Success 200 {object} PetDetail
// @Failure 404 {object} ErrorResponse
// @Router /pets/{id} [get]
func (h *PetHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	claims := middleware.GetClaims(r)

	var pet models.Pet
	if err := h.db.Where("id = ? AND vet = ?", id, claims.Zip).First(&pet).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	// Fetch medical records
	var procs []models.Procedure
	h.db.Where("uuid = ? AND sk = ?", strconv.Itoa(int(pet.ID)), claims.Zip).Order("date DESC").Find(&procs)

	records := make([]MedicalRecord, len(procs))
	for i, p := range procs {
		records[i] = procToMedicalRecord(p)
	}

	detail := PetDetail{
		PetListItem:     petToListItem(pet),
		UUID:            pet.UUID,
		Vet:             pet.Vet,
		Variety:         pet.Variety,
		Code:            pet.Code,
		Status:          strconv.Itoa(pet.Status),
		OwnerEmail:      pet.Email,
		OwnerPersonalId: pet.Code,
		Castrated:       pet.Cast != "",
		MedicalRecords:  records,
	}
	if pet.Birth2 != "" {
		detail.Birth2 = &pet.Birth2
	}

	writeJSON(w, http.StatusOK, detail)
}

// Create adds a new pet.
// @Summary Create pet
// @Tags pets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreatePetRequest true "Pet data"
// @Success 201 {object} PetListItem
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /pets [post]
func (h *PetHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var req CreatePetRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	pet := models.Pet{
		UUID:      req.UUID,
		Name:      req.Name,
		Pet:       req.Pet,
		Sex:       req.Sex,
		Variety:   req.Variety,
		Chip:      req.Chip,
		Date:      req.Date,
		Code:      req.Code,
		Phone:     req.Phone,
		Email:     req.Email,
		FirstName: req.FirstName,
		Color:     req.Color,
		Vet:       claims.Zip,
		Status:    1,
	}

	if err := h.db.Create(&pet).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create pet"})
		return
	}

	writeJSON(w, http.StatusCreated, petToListItem(pet))
}

// Update edits an existing pet.
// @Summary Update pet
// @Tags pets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param body body object true "Fields to update"
// @Success 200 {object} PetListItem
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /pets/{id} [put]
func (h *PetHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	claims := middleware.GetClaims(r)

	var pet models.Pet
	if err := h.db.Where("id = ? AND vet = ?", id, claims.Zip).First(&pet).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	delete(updates, "vet")
	delete(updates, "id")

	if err := h.db.Model(&pet).Updates(updates).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update pet"})
		return
	}

	writeJSON(w, http.StatusOK, petToListItem(pet))
}

// Delete removes a pet.
// @Summary Delete pet
// @Tags pets
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /pets/{id} [delete]
func (h *PetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	claims := middleware.GetClaims(r)

	var pet models.Pet
	if err := h.db.Where("id = ? AND vet = ?", id, claims.Zip).First(&pet).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	if err := h.db.Delete(&pet).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete pet"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "pet deleted"})
}

// History returns all medical records for a pet.
// @Summary Get pet medical history
// @Tags pets
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Success 200 {array} MedicalRecord
// @Failure 500 {object} ErrorResponse
// @Router /pets/{id}/history [get]
func (h *PetHandler) History(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var procedures []models.Procedure
	if err := h.db.Where("uuid = ?", id).Order("date DESC, id DESC").Find(&procedures).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch history"})
		return
	}

	records := make([]MedicalRecord, len(procedures))
	for i, p := range procedures {
		records[i] = procToMedicalRecord(p)
	}

	writeJSON(w, http.StatusOK, records)
}

// Certificate returns border crossing certificate data for a pet.
// @Summary Get pet certificate data
// @Tags pets
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Success 200 {object} CertificateResponse
// @Failure 404 {object} ErrorResponse
// @Router /pets/{id}/certificate [get]
func (h *PetHandler) Certificate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var pet models.Pet
	if err := h.db.First(&pet, id).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	petIDStr := strconv.Itoa(int(pet.ID))

	var lastVax, lastRabies, lastDehel, lastEcto models.Procedure
	h.db.Where("uuid = ? AND tp = 1", petIDStr).Order("date DESC").First(&lastVax)
	h.db.Where("uuid = ? AND tp = 101", petIDStr).Order("date DESC").First(&lastRabies)
	h.db.Where("uuid = ? AND tp = 3", petIDStr).Order("date DESC").First(&lastDehel)
	h.db.Where("uuid = ? AND tp = 4", petIDStr).Order("date DESC").First(&lastEcto)

	writeJSON(w, http.StatusOK, CertificateResponse{
		Pet:             petToListItem(pet),
		Vaccination:     procToMedicalRecord(lastVax),
		Rabies:          procToMedicalRecord(lastRabies),
		Dehelminization: procToMedicalRecord(lastDehel),
		Ectoparasite:    procToMedicalRecord(lastEcto),
	})
}

// procToMedicalRecord converts a DB procedure to the frontend medical record format.
func procToMedicalRecord(p models.Procedure) MedicalRecord {
	rec := MedicalRecord{
		ID:            strconv.Itoa(int(p.ID)),
		ProcedureType: strconv.Itoa(p.TP),
		ProcedureName: p.TPName,
		Diagnosis:     p.Diagn,
		Notes:         p.Nout,
		Comment:       p.Koment,
		VetName:       p.VetName,
		Price:         p.Price,
		Anamnesis:     p.Anam,
	}
	if p.Date != "" {
		rec.Date = &p.Date
	}

	// Collect non-empty vaccination fields
	for _, v := range []string{p.Vac, p.Vac1, p.Vac2, p.Vac3, p.Vac4, p.Vac5, p.Vac6, p.Vac7, p.Vac8, p.Vac9} {
		if v != "" {
			rec.Vaccinations = append(rec.Vaccinations, v)
		}
	}
	if rec.Vaccinations == nil {
		rec.Vaccinations = []string{}
	}

	// Collect non-empty test fields (tests are not in our model yet, return empty)
	rec.Tests = []string{}

	return rec
}
