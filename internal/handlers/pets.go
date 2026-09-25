package handlers

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"vetapp-backend/internal/data"
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
	ID         string  `json:"id" validate:"required"`
	Name       string  `json:"name" validate:"required"`
	Species    string  `json:"species" validate:"required"`
	Breed      string  `json:"breed" validate:"required"`
	Sex        string  `json:"sex" validate:"required"`
	Chip       string  `json:"chip" validate:"required"`
	OwnerName  string  `json:"ownerName" validate:"required"`
	OwnerPhone string  `json:"ownerPhone" validate:"required"`
	Birth      *string `json:"birth"`
	Color      string  `json:"color" validate:"required"`
}

type PetDetail struct {
	PetListItem
	UUID            string          `json:"uuid" validate:"required"`
	Vet             string          `json:"vet" validate:"required"`
	Date            *string         `json:"date"`
	Variety         string          `json:"variety" validate:"required"`
	Code            string          `json:"code" validate:"required"`
	Status          string          `json:"status" validate:"required"`
	Birth2          *string         `json:"birth2"`
	Castrated       bool            `json:"castrated" validate:"required"`
	Cast            string          `json:"cast"`
	CastDate        *string         `json:"castDate"`
	ChipDate        string          `json:"chipDate"`
	OwnerEmail      string          `json:"ownerEmail" validate:"required"`
	OwnerPersonalId string          `json:"ownerPersonalId" validate:"required"`
	MedicalRecords  []MedicalRecord `json:"medicalRecords" validate:"required"`
}

type MedicalRecord struct {
	ID            string   `json:"id" validate:"required"`
	Date          *string  `json:"date"`
	ProcedureType string   `json:"procedureType" validate:"required"`
	ProcedureName string   `json:"procedureName" validate:"required"`
	Diagnosis     string   `json:"diagnosis" validate:"required"`
	Notes         string   `json:"notes" validate:"required"`
	Comment       string   `json:"comment" validate:"required"`
	VetName       string   `json:"vetName" validate:"required"`
	Price         string   `json:"price" validate:"required"`
	Anamnesis     string   `json:"anamnesis" validate:"required"`
	Vaccinations  []string `json:"vaccinations" validate:"required"`
	Tests         []string `json:"tests" validate:"required"`
}

type PaginatedResponse struct {
	Data       interface{} `json:"data" validate:"required"`
	Total      int64       `json:"total" validate:"required"`
	Page       int         `json:"page" validate:"required"`
	PageSize   int         `json:"pageSize" validate:"required"`
	TotalPages int         `json:"totalPages" validate:"required"`
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
	Address   string `json:"address"`
	// Status is ignored: a clinic-registered pet always starts unregistered
	// (see Create). Kept so older clients still decode.
	Status *int `json:"status"`
	// PetStatus is how the pet lives: INHABITANT (domestic), STREET,
	// ADOPTED or WORKMATE.
	PetStatus string `json:"petStatus" validate:"omitempty,oneof=INHABITANT STREET ADOPTED WORKMATE"`
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

// List returns pets.
//
// Without a lookup key it browses the caller's own clinic. With an exact
// owner personal ID (`owner_id`) or microchip (`chip`) it searches every
// clinic, like vet/search2.php and vet/search3.php: a pet registered
// elsewhere must be findable when it walks in. Both keys are exact
// matches, so this cannot be used to enumerate other clinics' pets.
//
// @Summary List pets
// @Tags pets
// @Produce json
// @Security BearerAuth
// @Param search query string false "Search own clinic by name, owner, phone, chip"
// @Param owner_id query string false "Exact owner personal ID (all clinics)"
// @Param chip query string false "Exact microchip (all clinics)"
// @Param page query int false "Page number" default(1)
// @Param pageSize query int false "Page size" default(20)
// @Success 200 {object} PaginatedResponse
// @Failure 500 {object} ErrorResponse
// @Router /pets [get]
func (h *PetHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	query := h.db.Model(&models.Pet{})

	ownerID := strings.TrimSpace(r.URL.Query().Get("owner_id"))
	chip := strings.TrimSpace(r.URL.Query().Get("chip"))
	switch {
	// Compared trimmed: legacy rows carry stray spaces in both columns
	// (43 owner IDs, 151 chips), and an exact match silently missed them.
	// Backed by expression indexes (migration 013).
	case ownerID != "":
		query = query.Where("TRIM(uuid) = ?", ownerID)
	case chip != "":
		query = query.Where("TRIM(chip) = ?", chip)
	default:
		query = query.Where("vet = ?", claims.Zip)
		if search := strings.TrimSpace(r.URL.Query().Get("search")); search != "" {
			like := "%" + search + "%"
			query = query.Where("name ILIKE ? OR first_name ILIKE ? OR chip ILIKE ? OR phone ILIKE ?",
				like, like, like, like)
		}
	}

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

// Get returns a pet with the caller's clinic's records for it.
//
// A clinic may open a pet registered there or one it has treated (see
// canAccessPet). The records are the caller's clinic's own, as on
// vet/allprocedures.php; admins see every clinic's.
//
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
	if !canAccessPet(h.db, r, id) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var pet models.Pet
	if err := h.db.Where("id = ?", id).First(&pet).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	procs := h.clinicRecords(r, pet.ID)
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
		OwnerPersonalId: pet.UUID,
		Castrated:       pet.Cast != "",
		Cast:            pet.Cast,
		ChipDate:        pet.ChipDate,
		MedicalRecords:  records,
	}
	if pet.Birth2 != "" {
		detail.Birth2 = &pet.Birth2
	}
	if pet.Date != "" {
		detail.Date = &pet.Date
	}
	if pet.CastDate != "" {
		detail.CastDate = &pet.CastDate
	}

	writeJSON(w, http.StatusOK, detail)
}

// clinicRecords returns a pet's procedures, newest first: the caller's
// clinic's for a vet, every clinic's for an admin.
func (h *PetHandler) clinicRecords(r *http.Request, petID uint) []models.Procedure {
	q := h.db.Where("uuid = ?", strconv.Itoa(int(petID)))
	if !isAdmin(r) {
		q = q.Where("sk = ?", middleware.GetClaims(r).Zip)
	}
	var procs []models.Procedure
	q.Order("date DESC, id DESC").Find(&procs)
	return procs
}

// Create registers a pet at the caller's clinic (vet/addpet2.php).
//
// Like PHP it starts unregistered (status 2) with the placeholder
// access code 1313: a subscription is only ever activated by a payment,
// never by the clinic that created the record.
//
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
	log := middleware.RequestLogger(r)

	var req CreatePetRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if claims.Zip == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "this account has no clinic"})
		return
	}
	if req.Date != "" && !isISODate(req.Date) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date must be YYYY-MM-DD"})
		return
	}

	// Validate breed against known breeds for dog/cat
	if !data.IsValidBreed(req.Pet, req.Variety) {
		log.Warn("pet_create_failed", "reason", "invalid_breed", "species", req.Pet, "breed", req.Variety)
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid breed for this species"})
		return
	}

	pet := models.Pet{
		UUID:      strings.TrimSpace(req.UUID),
		Name:      strings.TrimSpace(req.Name),
		Pet:       normalizeSpecies(req.Pet),
		Sex:       normalizeSex(req.Sex),
		Variety:   req.Variety,
		Chip:      strings.TrimSpace(req.Chip),
		Date:      req.Date,
		Code:      vetCreatedPetCode,
		Phone:     req.Phone,
		Email:     req.Email,
		FirstName: req.FirstName,
		Color:     req.Color,
		Vet:       claims.Zip,
		Status:    petStatusUnregistered,
		PetStatus: req.PetStatus,
	}

	if err := h.db.Create(&pet).Error; err != nil {
		log.Error("pet_create_failed", "reason", "db_error", "error", err, "name", req.Name, "owner_uuid", req.UUID)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create pet"})
		return
	}

	// Update owner address if provided
	if req.Address != "" {
		h.db.Model(&models.User{}).Where("last_name = ? AND group_id = ?", pet.UUID, models.RoleOwner).Update("address", req.Address)
	}

	log.Info("pet_created", "pet_id", pet.ID, "name", pet.Name, "species", pet.Pet, "owner_uuid", pet.UUID)

	writeJSON(w, http.StatusCreated, petToListItem(pet))
}

// What vet/addpet2.php writes for a pet a clinic registers.
const (
	petStatusUnregistered = 2
	vetCreatedPetCode     = "1313"
)

// UpdatePetRequest mirrors vet/updatepet.php. Subscription state
// (status, birth2), the access code and the registering clinic are not
// editable by a clinic.
type UpdatePetRequest struct {
	Name      *string `json:"name"`
	Pet       *string `json:"pet"`
	Sex       *string `json:"sex"`
	Variety   *string `json:"variety"`
	Color     *string `json:"color"`
	Date      *string `json:"date"`
	Chip      *string `json:"chip"`
	ChipDate  *string `json:"chipd"`
	Cast      *string `json:"cast"`
	CastDate  *string `json:"castdate"`
	UUID      *string `json:"uuid"`
	FirstName *string `json:"first_name"`
	Phone     *string `json:"phone"`
	Email     *string `json:"email"`
}

// Update edits a pet the caller's clinic may open.
//
// @Summary Update pet
// @Tags pets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param body body UpdatePetRequest true "Fields to change"
// @Success 200 {object} PetListItem
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /pets/{id} [put]
func (h *PetHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	log := middleware.RequestLogger(r)
	if !canAccessPet(h.db, r, id) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}
	var pet models.Pet
	if err := h.db.Where("id = ?", id).First(&pet).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var req UpdatePetRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	oldSpecies, oldBreed := strings.TrimSpace(pet.Pet), strings.TrimSpace(pet.Variety)
	set(&pet.Name, req.Name)
	set(&pet.Pet, req.Pet)
	set(&pet.Sex, req.Sex)
	set(&pet.Variety, req.Variety)
	set(&pet.Color, req.Color)
	set(&pet.Date, req.Date)
	set(&pet.Chip, req.Chip)
	set(&pet.ChipDate, req.ChipDate)
	set(&pet.Cast, req.Cast)
	set(&pet.CastDate, req.CastDate)
	set(&pet.UUID, req.UUID)
	set(&pet.FirstName, req.FirstName)
	set(&pet.Phone, req.Phone)
	set(&pet.Email, req.Email)
	pet.Pet, pet.Sex = normalizeSpecies(pet.Pet), normalizeSex(pet.Sex)

	switch {
	case pet.Name == "":
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "name is required"})
		return
	case pet.UUID == "":
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "owner personal ID is required"})
		return
	}
	for _, d := range []string{pet.Date, pet.ChipDate, pet.CastDate} {
		if d != "" && !isISODate(d) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "dates must be YYYY-MM-DD"})
			return
		}
	}
	// Validate the breed only when it or the species changes: legacy rows
	// hold free-text breeds ("ხ", "მეტისი") and the form resends every
	// field, so checking unchanged values made those pets uneditable.
	changed := normalizeSpecies(oldSpecies) != pet.Pet || oldBreed != strings.TrimSpace(pet.Variety)
	if changed && !data.IsValidBreed(pet.Pet, pet.Variety) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid breed for this species"})
		return
	}

	if err := h.db.Model(&pet).Select("name", "pet", "sex", "variety", "color", "date", "chip", "chipd",
		"cast", "castdate", "uuid", "first_name", "phone", "email").Updates(&pet).Error; err != nil {
		log.Error("pet_update_failed", "pet_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update pet"})
		return
	}

	log.Info("pet_updated", "pet_id", id)
	writeJSON(w, http.StatusOK, petToListItem(pet))
}

// Delete removes a pet. Admin only: the PHP clinic portal has no pet
// delete, and removing a pet orphans every clinic's records for it.
//
// @Summary Delete pet (admin)
// @Tags pets
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /pets/{id} [delete]
func (h *PetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	log := middleware.RequestLogger(r)
	if !isAdmin(r) {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "only an administrator can delete a pet"})
		return
	}

	var pet models.Pet
	if err := h.db.Where("id = ?", id).First(&pet).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}
	if err := h.db.Delete(&pet).Error; err != nil {
		log.Error("pet_delete_failed", "pet_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete pet"})
		return
	}

	log.Info("pet_deleted", "pet_id", id, "name", pet.Name)
	writeJSON(w, http.StatusOK, MessageResponse{Message: "pet deleted"})
}

// History returns the pet's medical records at the caller's clinic.
//
// @Summary Pet medical history
// @Tags pets
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Success 200 {array} MedicalRecord
// @Failure 404 {object} ErrorResponse
// @Router /pets/{id}/history [get]
func (h *PetHandler) History(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// 404 rather than 403: a distinct status would confirm which pet ids
	// exist to someone probing ids they cannot access.
	if !canAccessPet(h.db, r, id) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}
	n, _ := strconv.Atoi(id)
	procedures := h.clinicRecords(r, uint(n))

	records := make([]MedicalRecord, len(procedures))
	for i, p := range procedures {
		records[i] = procToMedicalRecord(p)
	}

	writeJSON(w, http.StatusOK, records)
}

// CertificateResponse is everything vet/cross.php prints on the
// trilingual border-crossing certificate. Each record is the pet's most
// recent of its kind at any clinic (the certificate travels with the
// pet); a missing one is null.
type CertificateResponse struct {
	Pet             CertificatePet        `json:"pet" validate:"required"`
	Owner           CertificateOwner      `json:"owner" validate:"required"`
	Rabies          *CertificateTreatment `json:"rabies"`
	Complex         *CertificateTreatment `json:"complex"`
	Dehelminization *CertificateTreatment `json:"dehelminization"`
	Ectoparasite    *CertificateTreatment `json:"ectoparasite"`
}

// CertificatePet is the animal block of the certificate.
type CertificatePet struct {
	ID       uint   `json:"id" validate:"required"`
	Name     string `json:"name" validate:"required"`
	Pet      string `json:"pet" validate:"required"`
	Sex      string `json:"sex" validate:"required"`
	Variety  string `json:"variety" validate:"required"`
	Color    string `json:"color" validate:"required"`
	Date     string `json:"date" validate:"required"`
	Chip     string `json:"chip" validate:"required"`
	Chipd    string `json:"chipd" validate:"required"`
	Cast     string `json:"cast" validate:"required"`
	Castdate string `json:"castdate" validate:"required"`
}

// CertificateTreatment is one treatment line: only what cross.php prints.
// The records may come from other clinics, so nothing else — notes,
// prices, payment state — leaves the server.
type CertificateTreatment struct {
	Date  string `json:"date" validate:"required"`
	Date2 string `json:"date2" validate:"required"`
	Vac   string `json:"vac" validate:"required"`
	VacN  string `json:"vacn" validate:"required"`
	Ser   string `json:"ser" validate:"required"`
	Deh   string `json:"deh" validate:"required"`
	Vac1  string `json:"vac1" validate:"required"`
	Vac2  string `json:"vac2" validate:"required"`
	Vac3  string `json:"vac3" validate:"required"`
	Vac4  string `json:"vac4" validate:"required"`
	Vac5  string `json:"vac5" validate:"required"`
	Vac6  string `json:"vac6" validate:"required"`
	Vac7  string `json:"vac7" validate:"required"`
}

func certTreatment(p models.Procedure) *CertificateTreatment {
	return &CertificateTreatment{Date: p.Date, Date2: p.Date2, Vac: p.Vac, VacN: p.VacN, Ser: p.Ser, Deh: p.Deh,
		Vac1: p.Vac1, Vac2: p.Vac2, Vac3: p.Vac3, Vac4: p.Vac4, Vac5: p.Vac5, Vac6: p.Vac6, Vac7: p.Vac7}
}

// CertificateOwner is the owner block of the certificate.
type CertificateOwner struct {
	Name       string `json:"name" validate:"required"`
	PersonalID string `json:"personal_id" validate:"required"`
	Phone      string `json:"phone" validate:"required"`
	Address    string `json:"address" validate:"required"`
}

// The vaccine types cross.php looks for (vaccination.vac on tp=1 rows).
const (
	vacRabies  = "ცოფის საწინააღმდეგო ვაქცინა"
	vacComplex = "კომპლექსური ვაქცინა"
)

// Certificate returns border crossing certificate data for a pet.
//
// @Summary Border crossing certificate
// @Tags pets
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Success 200 {object} CertificateResponse
// @Failure 404 {object} ErrorResponse
// @Router /pets/{id}/certificate [get]
func (h *PetHandler) Certificate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if !canAccessPet(h.db, r, id) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var pet models.Pet
	if err := h.db.Where("id = ?", id).First(&pet).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}
	petIDStr := strconv.Itoa(int(pet.ID))

	// The four "latest of its kind" lookups from cross.php in one query.
	// Dehelminization and ectoparasite are matched on tp as well as the
	// tpname cross.php used, since tpname is free text on old rows.
	var rows []struct {
		Kind string
		models.Procedure
	}
	h.db.Raw(`
		SELECT DISTINCT ON (kind) kind, v.*
		FROM (
			SELECT CASE
				WHEN vac = ? THEN 'rabies'
				WHEN vac = ? THEN 'complex'
				WHEN tp = '12' OR tpname = 'დეჰელმინთიზაცია' THEN 'dehel'
				WHEN tp = '11' OR tpname = 'ექტოპარაზიტების პრევენცია' THEN 'ecto'
			END AS kind, *
			FROM vaccination
			WHERE uuid = ?
		) v
		WHERE kind IS NOT NULL
		ORDER BY kind, id DESC`, vacRabies, vacComplex, petIDStr).Scan(&rows)

	resp := CertificateResponse{Pet: CertificatePet{
		ID: pet.ID, Name: pet.Name, Pet: pet.Pet, Sex: pet.Sex, Variety: pet.Variety, Color: pet.Color,
		Date: pet.Date, Chip: pet.Chip, Chipd: pet.ChipDate, Cast: pet.Cast, Castdate: pet.CastDate,
	}}
	for i := range rows {
		t := certTreatment(rows[i].Procedure)
		switch rows[i].Kind {
		case "rabies":
			resp.Rabies = t
		case "complex":
			resp.Complex = t
		case "dehel":
			resp.Dehelminization = t
		case "ecto":
			resp.Ectoparasite = t
		}
	}

	resp.Owner = CertificateOwner{Name: pet.FirstName, PersonalID: pet.UUID, Phone: pet.Phone}
	var owner models.User
	if err := h.db.Where("last_name = ? AND group_id = ?", pet.UUID, models.RoleOwner).First(&owner).Error; err == nil {
		resp.Owner.Address = owner.Address
		if resp.Owner.Phone == "" {
			resp.Owner.Phone = owner.Phone
		}
		if resp.Owner.Name == "" {
			resp.Owner.Name = owner.FirstName
		}
	}

	writeJSON(w, http.StatusOK, resp)
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
