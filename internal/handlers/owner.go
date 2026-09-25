package handlers

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
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
	PetStatus          string  `json:"petStatus"`                              // "INHABITANT", "ADOPTED", "WORKMATE"
	SubscriptionStatus string  `json:"subscriptionStatus" validate:"required"` // "active", "expired", "unregistered"
	SubscriptionExpiry *string `json:"subscriptionExpiry"`
}

// OwnerPetDetail is the full detail view for a pet from the owner portal.
//
// `Categories` is shared with the public pet profile via the package-
// level `ProcedureCategoryCount` type — same shape, same JSON, so the
// owner-specific alias was just dead code.
type OwnerPetDetail struct {
	OwnerPetItem
	UUID       string                   `json:"uuid" validate:"required"`
	Vet        string                   `json:"vet" validate:"required"`
	Castrated  bool                     `json:"castrated" validate:"required"`
	Code       string                   `json:"code" validate:"required"`
	Categories []ProcedureCategoryCount `json:"categories"`
}

// OwnerCreatePetRequest is the request body for an owner adding a pet.
type OwnerCreatePetRequest struct {
	Name      string `json:"name" validate:"required"`
	Pet       string `json:"pet"`
	Sex       string `json:"sex"`
	Variety   string `json:"variety"`
	Chip      string `json:"chip"`
	Date      string `json:"date"`
	Color     string `json:"color"`
	PetStatus string `json:"petStatus"` // INHABITANT | ADOPTED | WORKMATE
}

// OwnerEctoItem is one ectoparasite product on a record. Owner-added
// records have exactly one item; legacy clinic records may have 1–4
// (one per slot). The mobile UI renders these as labeled chips.
//
// The legacy PHP scheme stores each type in TWO columns:
//   - drops:  Vac1 (dropdown choice), Vac (custom-typed name)
//   - pills:  Vac3 (dropdown choice), Vac2 (custom-typed name)
//   - collar: Vac5 (dropdown choice), Vac4 (custom-typed name)
//   - spray:  Vac7 (dropdown choice), Vac6 (custom-typed name)
//
// On read we pick the custom value when set, else the dropdown value
// (matches `view11.php:73-89`). New writes from the mobile/Next.js side
// fill exactly one (choice OR custom) per type.
type OwnerEctoItem struct {
	Type string `json:"type" validate:"required"` // "drops" | "pills" | "collar" | "spray"
	Name string `json:"name" validate:"required"` // brand / preparat name
}

// OwnerTestResult is one row of a diagnostic test panel. Each `tp=2/22/222`
// (dog/cat/other test) record carries up to 16 of these, depending on
// which panels were run.
type OwnerTestResult struct {
	Label  string `json:"label" validate:"required"`  // human-readable test name (Georgian)
	Result string `json:"result" validate:"required"` // typically "დადებითი" / "უარყოფითი" but can be free text
}

// OwnerProcedureItem is a medical record as seen by the owner.
//
// `VaccineType` / `Preparat` come from the legacy `vac` and `vacn`
// columns. `Serial` is the vaccine batch number (legacy `ser` column).
// `VetFullName` is the vet's full name resolved from
// `memberlogin_members`; populated only when `vetname` is a numeric
// user_id that matches a member row.
//
// `AddedByOwner` is `true` when the record was self-reported from the
// mobile app (no clinic context: `sk` column is empty). This is the
// signal the UI uses for the "added by owner" badge and the delete
// affordance — independent of `VetName`, so owners can self-report the
// name of an external vet without losing those guarantees.
type OwnerProcedureItem struct {
	ID            string            `json:"id" validate:"required"`
	Date          *string           `json:"date"`
	NextDate      *string           `json:"nextDate"`
	ProcedureType string            `json:"procedureType" validate:"required"`
	ProcedureName string            `json:"procedureName" validate:"required"`
	Diagnosis     string            `json:"diagnosis" validate:"required"`
	Notes         string            `json:"notes" validate:"required"`
	Comment       string            `json:"comment" validate:"required"`
	Anamnesis     string            `json:"anamnesis"`                   // tp=10x/20x — anamnesis. tp=1/2 — also surfaced if set.
	Prescription  string            `json:"prescription"`                // dani column — prescription / დანიშნულება. Shown on every category.
	VetName       string            `json:"vetName" validate:"required"` // raw column value (id or free-text)
	VetFullName   string            `json:"vetFullName"`                 // resolved "First Last" via JOIN
	ClinicName    string            `json:"clinicName"`                  // resolved clinic name via `sk` -> memberlogin_members.company_name
	VaccineType   string            `json:"vaccineType"`                 // tp=1 — vaccine type ("კომპლექსური ვაქცინა")
	Preparat      string            `json:"preparat"`                    // tp=1 — vaccine brand. tp=12 — dewormer drug.
	Serial        string            `json:"serial"`                      // tp=1 — batch / serial number
	Treatment     string            `json:"treatment"`                   // tp=10x/20x — treatment / medications administered
	AddedByOwner  bool              `json:"addedByOwner"`                // true => self-reported, deletable
	EctoItems     []OwnerEctoItem   `json:"ectoItems"`                   // populated only for tp=11 records
	TestResults   []OwnerTestResult `json:"testResults"`                 // populated only for tp=2/22/222 records
	Vaccinations  []string          `json:"vaccinations" validate:"required"`
}

// AccessCodeResponse is the response when generating a new access code.
type AccessCodeResponse struct {
	Code string `json:"code" validate:"required"`
}

// OwnerDiseaseItem is one entry from the `eals` (allergy/disease) table
// as exposed to the owner. Lives in its own table separate from
// `vaccination` — see Allergy model.
type OwnerDiseaseItem struct {
	ID   string  `json:"id" validate:"required"`
	Name string  `json:"name" validate:"required"`
	Date *string `json:"date"`
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
	// VetName is the raw `vetname` value (a member ID). Kept for
	// backwards compatibility; clients should display VetFullName.
	VetName string `json:"vetName" validate:"required"`
	// VetFullName is the resolved human name, empty when the
	// appointment has no vet assigned yet.
	VetFullName string `json:"vetFullName"`
	// PetID / PetName identify which animal the visit is for — an owner
	// with several pets cannot otherwise tell them apart.
	PetID   string `json:"petId"`
	PetName string `json:"petName"`
	Status  string `json:"status" validate:"required"`
	// Upcoming is true when the visit is today or later, so the client
	// doesn't have to re-implement date comparison against the server's
	// notion of "today".
	Upcoming bool `json:"upcoming"`
}

// --- Helpers ---

// resolveVetNames maps raw `vetname` values (member IDs stored as TEXT)
// to display names in one query, instead of one query per record.
//
// Schema notes:
//   - `vetname` is TEXT and legacy rows frequently carry trailing
//     whitespace (e.g. "149  ") from the MySQL import, so keys are
//     trimmed before lookup.
//   - "" and "0" both mean "no vet" — those records were self-reported
//     by the owner.
//   - In `memberlogin_members`, `first_name` holds the full
//     human-readable name while `last_name` is actually the Georgian
//     personal ID (see models/user.go), so display from `first_name`.
//
// Shared by the procedures and visits endpoints; keep it that way so
// the two can't disagree about how a vet is named.
func (h *OwnerPortalHandler) resolveVetNames(rawIDs []string) map[string]string {
	idSet := make(map[string]struct{}, len(rawIDs))
	for _, raw := range rawIDs {
		key := strings.TrimSpace(raw)
		if key != "" && key != "0" {
			idSet[key] = struct{}{}
		}
	}
	names := make(map[string]string, len(idSet))
	if len(idSet) == 0 {
		return names
	}

	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	type vetRow struct {
		ID        string
		FirstName string
	}
	var vets []vetRow
	h.db.Table("memberlogin_members").
		Select("id::text AS id, first_name").
		Where("id::text IN ?", ids).
		Scan(&vets)
	for _, v := range vets {
		if full := strings.TrimSpace(v.FirstName); full != "" {
			names[v.ID] = full
		}
	}
	return names
}

// resolveClinicNames maps clinic codes (`vaccination.sk`) to the
// clinic's display name in one query.
//
// The clinic name lives on the vet accounts themselves:
// `memberlogin_members.company_name` for rows whose `zip` equals the
// code and whose group is Vet. Several staff share one clinic, so the
// lookup collapses them with MAX() — the name is the same across rows.
//
// Names are trimmed because the legacy data has stray leading spaces
// (" შპს ვეტექსი"), which would otherwise render as an indent.
func (h *OwnerPortalHandler) resolveClinicNames(rawCodes []string) map[string]string {
	codeSet := make(map[string]struct{}, len(rawCodes))
	for _, raw := range rawCodes {
		if key := strings.TrimSpace(raw); key != "" {
			codeSet[key] = struct{}{}
		}
	}
	names := make(map[string]string, len(codeSet))
	if len(codeSet) == 0 {
		return names
	}

	codes := make([]string, 0, len(codeSet))
	for c := range codeSet {
		codes = append(codes, c)
	}
	type clinicRow struct {
		Zip         string
		CompanyName string
	}
	var rows []clinicRow
	h.db.Table("memberlogin_members").
		Select("zip, MAX(company_name) AS company_name").
		Where("zip IN ? AND group_id = ? AND COALESCE(company_name,'') <> ''", codes, models.RoleVet).
		Group("zip").
		Scan(&rows)
	for _, r := range rows {
		if n := strings.TrimSpace(r.CompanyName); n != "" {
			names[strings.TrimSpace(r.Zip)] = n
		}
	}
	return names
}

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
		PetStatus:          p.PetStatus,
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

	petID := strconv.Itoa(int(pet.ID))

	detail := OwnerPetDetail{
		OwnerPetItem: petToOwnerItem(*pet),
		UUID:         pet.UUID,
		Vet:          pet.Vet,
		Castrated:    pet.Cast != "",
		Code:         pet.Code,
		Categories:   buildPetCategories(h.db, petID),
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

	// Default to INHABITANT if unspecified or invalid so home-screen
	// filters always have a bucket for every pet. STREET is a 2026
	// addition for stray / community-fed pets that owners adopted
	// informally without bringing them indoors.
	petStatus := req.PetStatus
	switch petStatus {
	case "INHABITANT", "ADOPTED", "WORKMATE", "STREET":
		// ok
	default:
		petStatus = "INHABITANT"
	}

	pet := models.Pet{
		UUID:      claims.LastName, // Owner personal ID
		Name:      req.Name,
		Pet:       normalizeSpecies(req.Pet),
		Sex:       normalizeSex(req.Sex),
		Variety:   req.Variety,
		Chip:      req.Chip,
		Date:      req.Date,
		Color:     req.Color,
		PetStatus: petStatus,
		Phone:     user.Phone,
		Email:     user.Email,
		FirstName: user.FirstName,
		Status:    3,      // Unregistered
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

	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	// Allowlist, as owner/updatepet2.php (minus the owner ID, which would
	// hand the pet to someone else). A blocklist let any other column
	// through — pets.happy, cast, the clinic's castdate, userId, …
	updates := pickFields(body, "name", "pet", "sex", "variety", "chip", "chipd",
		"date", "color", "petStatus", "phone", "first_name")
	for k, v := range updates {
		str, isString := v.(string)
		if !isString {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: k + " must be a string"})
			return
		}
		if (k == "date" || k == "chipd") && str != "" && !isISODate(str) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: k + " must be YYYY-MM-DD"})
			return
		}
	}
	if v, ok := updates["pet"].(string); ok {
		updates["pet"] = normalizeSpecies(v)
	}
	if v, ok := updates["sex"].(string); ok {
		updates["sex"] = normalizeSex(v)
	}
	if name, ok := updates["name"].(string); ok && strings.TrimSpace(name) == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "name is required"})
		return
	}
	if len(updates) == 0 {
		writeJSON(w, http.StatusOK, petToOwnerItem(*pet))
		return
	}

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

	// tp=999 means query allergies table instead. Kept for back-compat
	// with older mobile builds; new builds should hit the dedicated
	// `GET /api/owner/pets/{id}/diseases` endpoint.
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
		// Test category is species-split in the legacy PHP scheme:
		//   tp=2   → dog test    (vet/addtest.php)
		//   tp=22  → cat test    (vet/addtest1.php)
		//   tp=222 → other test  (vet/addtest2.php)
		// Expose a single "ანალიზი" tile in the UI by translating tp=2
		// into a 3-tp `IN` filter on the server side, so callers don't
		// have to know the species split.
		if tpStr == "2" {
			query = query.Where("tp IN ?", []string{"2", "22", "222"})
		} else {
			query = query.Where("tp = ?", tpStr)
		}
	}

	var procs []models.Procedure
	query.Order("date DESC, id DESC").Find(&procs)

	// Bulk-resolve vet names: collect unique numeric vetname values, look
	// them up once, and feed the lookup into the loop below. Avoids one
	// query per record.
	//
	// Schema notes:
	//   - `vaccination.vetname` is TEXT and frequently has trailing
	//     whitespace (e.g. "149  ") from the legacy MySQL import.
	//     We trim before using it as a key.
	//   - In `memberlogin_members`, the `first_name` column contains the
	//     full human-readable name (often "First Last" already), while
	//     `last_name` is actually the Georgian personal ID — see
	//     models/user.go. So display from `first_name` only.
	rawVetIDs := make([]string, 0, len(procs))
	rawClinicCodes := make([]string, 0, len(procs))
	for _, p := range procs {
		rawVetIDs = append(rawVetIDs, p.VetName)
		rawClinicCodes = append(rawClinicCodes, p.SK)
	}
	vetNames := h.resolveVetNames(rawVetIDs)
	clinicNames := h.resolveClinicNames(rawClinicCodes)

	items := make([]OwnerProcedureItem, len(procs))
	for i, p := range procs {
		items[i] = buildOwnerProcedureItem(&p, vetNames, clinicNames)
	}

	writeJSON(w, http.StatusOK, items)
}

// buildOwnerProcedureItem turns a Procedure DB row into an API response
// item, dispatching on `TP` to interpret the polymorphic columns
// correctly. Centralising this here means the GET list handler and the
// POST 201 response (CreateProcedure) can share the same logic.
//
// All multi-line text fields run through `cleanText()` on the way out
// so legacy `<br />` tags (from PHP's `nl2br()` on save) become real
// newlines.
//
// `tpname` is normalized to the canonical Georgian label when the raw
// DB value is missing, looks numeric, or doesn't match our category
// table. Legacy records often have `tpname="1"` or stale variant
// strings; this gives the UI a stable header line.
func buildOwnerProcedureItem(p *models.Procedure, vetNames map[string]string, clinicNames map[string]string) OwnerProcedureItem {
	vetKey := strings.TrimSpace(p.VetName)
	// Owner-added if there's no associated vet (vetname empty / "0").
	addedByOwner := vetKey == "" || vetKey == "0"

	procedureName := normalizeTPName(p.TP, p.TPName)

	item := OwnerProcedureItem{
		ID:            strconv.Itoa(int(p.ID)),
		ProcedureType: strconv.Itoa(p.TP),
		ProcedureName: procedureName,
		Comment:       cleanText(p.Coment),
		Prescription:  cleanText(p.Dani),
		VetName:       vetKey,
		VetFullName:   vetNames[vetKey],
		ClinicName:    clinicNames[strings.TrimSpace(p.SK)],
		AddedByOwner:  addedByOwner,
	}
	if p.Date != "" {
		item.Date = &p.Date
	}
	if p.Date2 != "" {
		item.NextDate = &p.Date2
	}

	// Per-tp column interpretation. See models/procedure.go for the full
	// schema-overload story.
	switch {
	case p.TP == 1: // Vaccination
		item.VaccineType = cleanText(p.Vac)
		item.Preparat = cleanText(p.VacN)
		item.Serial = cleanText(p.Ser)
		// Diagnosis/notes still surface in case the legacy clinic UI
		// stamped them on the same record.
		item.Diagnosis = cleanText(p.Diagn)
		item.Notes = cleanText(p.Nout)

	case p.TP == 11: // Ectoparasite — 8-slot custom/choice convention
		item.EctoItems = extractEctoItems(p)

	case p.TP == 12: // Dehelminization
		// PHP form (vet/adddeh.php) puts the dropdown drug choice in
		// `Deh` and falls back to `Vac` for free-typed custom names.
		if v := strings.TrimSpace(p.Deh); v != "" {
			item.Preparat = cleanText(v)
		} else {
			item.Preparat = cleanText(p.Vac)
		}

	case isTestTP(p.TP): // 2/22/222 — diagnostic test panels
		item.TestResults = extractTestResults(p)
		item.Diagnosis = cleanText(p.Diagn)
		item.Notes = cleanText(p.Nout)

	case isGenericProcedureTP(p.TP):
		// Generic sub-specialty (surgery, radiology, therapy, ...). The
		// PHP `addprocedure*.php` family uses these column meanings:
		//   Vac  = procedure name (per-record, can override category label)
		//   Vac1 = anamnesis
		//   Vac2 = diagnosis
		//   Vac3 = treatment / medications
		// Anam/Diagn/Nout columns are sometimes also stamped — prefer
		// the Vac1/Vac2/Vac3 values since that's where the form writes.
		//
		// Per partner spec, the procedure name in the owner UI is the
		// per-record editable name. If `Vac` was set on this record
		// (clinic typed a custom name, or owner renamed via the new
		// edit affordance), surface that as procedureName; otherwise
		// the canonical category label from `procedureTypeNames` wins
		// (already set above via normalizeTPName).
		if vacName := strings.TrimSpace(p.Vac); vacName != "" {
			item.ProcedureName = cleanText(vacName)
		}
		item.Anamnesis = cleanText(firstNonEmpty(p.Vac1, p.Anam))
		item.Diagnosis = cleanText(firstNonEmpty(p.Vac2, p.Diagn))
		item.Treatment = cleanText(firstNonEmpty(p.Vac3, p.Nout))
		// Notes column kept as a separate slot in case it's set
		// independently of `Vac3`/`Nout` already being shown as treatment.
		item.Notes = cleanText(p.Nout)

	default:
		// Unknown tp — emit raw fields without interpretation so users
		// don't lose data. Legacy orphan tps (3/4/5/555) hit this path
		// until they're migrated.
		item.VaccineType = cleanText(p.Vac)
		item.Preparat = cleanText(p.VacN)
		item.Diagnosis = cleanText(p.Diagn)
		item.Notes = cleanText(p.Nout)
	}

	// Vaccinations array kept as a flat list for back-compat with old
	// mobile builds that don't yet know about ectoItems / testResults.
	for _, v := range []string{p.Vac, p.Vac1, p.Vac2, p.Vac3, p.Vac4, p.Vac5, p.Vac6, p.Vac7, p.Vac8, p.Vac9} {
		if v != "" {
			item.Vaccinations = append(item.Vaccinations, cleanText(v))
		}
	}
	if item.Vaccinations == nil {
		item.Vaccinations = []string{}
	}

	return item
}

// firstNonEmpty returns the first non-blank (after trim) string from
// the given options. Used to choose between primary and fallback DB
// columns for generic-procedure fields.
func firstNonEmpty(opts ...string) string {
	for _, s := range opts {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// normalizeTPName picks the procedure's display name. It prefers the
// canonical Georgian label from `procedureTypeNames` for the given tp
// and only falls back to the raw `tpname` column when the canonical
// label is unavailable AND the raw value looks like a real label (not
// a leftover numeric string).
//
// Many legacy rows have `tpname="1"` or `tpname=""` because PHP forms
// occasionally wrote the tp number into the name field by mistake.
// Without this normalization, the mobile accordion header reads "1"
// instead of "ვაქცინაცია".
func normalizeTPName(tp int, raw string) string {
	if canonical, ok := procedureTypeNames[tp]; ok {
		return canonical
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	// Looks like a bare number ("1", "11", "107"…) — almost certainly
	// the tp got accidentally written into tpname. Hide it.
	if onlyDigits(trimmed) {
		return ""
	}
	return trimmed
}

// onlyDigits reports whether s is entirely ASCII digits 0-9. Cheaper
// than a regex for the short strings we handle here.
func onlyDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Diseases returns the pet's allergies / chronic diseases from the
// `eals` table (separate from the procedure history). The legacy app
// stores these in their own table because they're conditions, not
// dated procedures.
// @Summary Get pet diseases / allergies (owner view)
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Success 200 {array} OwnerDiseaseItem
// @Failure 404 {object} ErrorResponse
// @Router /owner/pets/{id}/diseases [get]
func (h *OwnerPortalHandler) Diseases(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	personalID := ownerPersonalID(r)

	if _, err := h.verifyPetOwnership(id, personalID); err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var allergies []models.Allergy
	h.db.Where("uuid = ?", id).Order("id DESC").Find(&allergies)

	items := make([]OwnerDiseaseItem, len(allergies))
	for i, a := range allergies {
		items[i] = OwnerDiseaseItem{
			ID:   strconv.Itoa(int(a.ID)),
			Name: cleanText(a.Name),
		}
		if a.Date != "" {
			d := a.Date
			items[i].Date = &d
		}
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

	// Upcoming reminders are rows whose reminder date (`date2`) is in the
	// future. `date2` is TEXT in ISO form, so a lexicographic >= is a
	// correct date comparison for that format.
	//
	// This previously also required `date3 > '2'`, on the belief that
	// date3 was a status flag ("1" cancelled / "2" sent / "3" pending).
	// It isn't: date3 holds the same reminder date in comma format
	// ("2027,03,18"), so the comparison only ever passed because year
	// strings sort above "2". Its real effect was to hide every
	// owner-created reminder, which the mobile client stores with an
	// empty date3 — the calendar tab was permanently empty for pets
	// whose records the owner added themselves. Filtering on the date
	// alone is what the screen actually means.
	//
	// Rows carrying legacy sentinels ("--", "") in date2 simply fail the
	// >= compare and stay excluded, as before.
	var procs []models.Procedure
	h.db.Where("uuid IN ? AND date2 >= ?", petIDs, today).
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

// procedureNameForTP returns the canonical Georgian procedure name for a
// given numeric `tp` code. The mapping mirrors the legacy PHP scheme,
// which is also what `ProcedureHandler.Types()` returns and what
// `procedureTypeNames` (in public.go) holds.
//
// Used to auto-populate `tpname` on owner-created records — without
// this, records render as raw numeric codes in the history list.
func procedureNameForTP(tp int) string {
	if name, ok := procedureTypeNames[tp]; ok {
		return name
	}
	return ""
}

// extractEctoItems decodes an ectoparasite (`tp=11`) record's 8 product
// slots into a list of `{type, name}` pairs. The PHP scheme stores each
// product type in two columns: a "choice" column (dropdown selection)
// and a "custom" column (free-typed name). The custom value wins if
// both are set, matching `view11.php:73-89`.
//
// Bug compensation: `addecto.php:459` has a known typo that writes the
// collar-custom value (`$vac4`) into the `vac6` column. So when both
// `Vac4` and `Vac6` are non-empty AND identical, treat `Vac6` as a
// duplicate and skip the spurious spray slot. This affects ~10s of
// legacy records.
func extractEctoItems(p *models.Procedure) []OwnerEctoItem {
	corruptVac6 := strings.TrimSpace(p.Vac4) != "" &&
		strings.TrimSpace(p.Vac4) == strings.TrimSpace(p.Vac6)

	type slot struct {
		typ           string
		choice        string
		custom        string
		skipDuplicate bool // for the vac6/vac4 PHP-bug case
	}
	slots := []slot{
		{typ: "drops", choice: p.Vac1, custom: p.Vac},
		{typ: "pills", choice: p.Vac3, custom: p.Vac2},
		{typ: "collar", choice: p.Vac5, custom: p.Vac4},
		{typ: "spray", choice: p.Vac7, custom: p.Vac6, skipDuplicate: corruptVac6},
	}

	var items []OwnerEctoItem
	for _, s := range slots {
		if s.skipDuplicate {
			// Spray slot: vac6 is a corrupt copy of vac4, ignore it.
			// Still surface the dropdown choice if one exists.
			if v := strings.TrimSpace(s.choice); v != "" {
				items = append(items, OwnerEctoItem{Type: s.typ, Name: v})
			}
			continue
		}
		// Custom-typed value preferred; falls back to dropdown choice.
		name := strings.TrimSpace(s.custom)
		if name == "" {
			name = strings.TrimSpace(s.choice)
		}
		if name != "" {
			items = append(items, OwnerEctoItem{Type: s.typ, Name: name})
		}
	}
	return items
}

// extractTestResults decodes a test record's panel results into labeled
// rows, using the same per-species form definitions the clinic records
// them with (procedure_forms.go), so every result PHP or the web app
// writes is shown under its real name.
func extractTestResults(p *models.Procedure) []OwnerTestResult {
	form := testFormFor(p.TP)
	if form == nil {
		return nil
	}
	var items []OwnerTestResult
	for _, f := range form.Fields {
		if f.Kind != "result" && !(p.TP == 222 && f.Column == "ser") {
			continue
		}
		v := strings.TrimSpace(procedureColumn(p, f.Column))
		if v == "" {
			continue
		}
		label := f.Label
		if f.Group != "" {
			label = f.Group + " — " + f.Label
		}
		if p.TP == 222 { // free-text test: name in vac, result in ser
			label = strings.TrimSpace(p.Vac)
		}
		items = append(items, OwnerTestResult{Label: label, Result: v})
	}
	return items
}

// procedureColumn reads a vaccination column by name.
func procedureColumn(p *models.Procedure, col string) string {
	switch col {
	case "vac":
		return p.Vac
	case "vacn":
		return p.VacN
	case "deh":
		return p.Deh
	case "ser":
		return p.Ser
	case "vac1":
		return p.Vac1
	case "vac2":
		return p.Vac2
	case "vac3":
		return p.Vac3
	case "vac4":
		return p.Vac4
	case "vac5":
		return p.Vac5
	case "vac6":
		return p.Vac6
	case "vac7":
		return p.Vac7
	case "vac8":
		return p.Vac8
	case "vac9":
		return p.Vac9
	case "test1":
		return p.Test1
	case "test2":
		return p.Test2
	case "test3":
		return p.Test3
	case "test4":
		return p.Test4
	case "test5":
		return p.Test5
	case "test6":
		return p.Test6
	case "test7":
		return p.Test7
	case "test8":
		return p.Test8
	case "address":
		return p.Address
	case "sax":
		return p.Sax
	}
	return ""
}

// isGenericProcedureTP reports whether the given tp uses the "generic
// procedure" column convention: vac=name, vac1=anamnesis, vac2=diagnosis,
// vac3=treatment. These are the sub-specialty tps written by
// vet/addprocedure*.php.
func isGenericProcedureTP(tp int) bool {
	switch tp {
	case 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 115, 116, 202, 203:
		return true
	}
	return false
}

// isTestTP reports whether the tp is one of the species-split test types.
func isTestTP(tp int) bool {
	return tp == 2 || tp == 22 || tp == 222
}

// OwnerCreateProcedureRequest is the payload for an owner self-reporting a
// procedure (home vaccination, deworming, ectoparasite treatment, test, etc.)
// against their own pet. Clinic-only fields (sk, vetname, price) are not
// exposed; the pet is identified by the URL path, not the body. Records
// created via this endpoint are marked "added by owner" and don't show a
// vet name in the history — see the AddedByOwner field on
// OwnerProcedureItem.
type OwnerCreateProcedureRequest struct {
	TP     int    `json:"tp" validate:"required,min=1"`
	Date   string `json:"date"`
	Date2  string `json:"date2"`
	Date3  string `json:"date3"`
	TPName string `json:"tpname"`
	Vac    string `json:"vac"`
	VacN   string `json:"vacn"`
	Ser    string `json:"ser"`
	Deh    string `json:"deh"`
	Diagn  string `json:"diagn"`
	Nout   string `json:"nout"`
	Anam   string `json:"anam"`
	Coment string `json:"coment"`
	Dani   string `json:"dani"`
	Vac1   string `json:"vac1"`
	Vac2   string `json:"vac2"`
	Vac3   string `json:"vac3"`
	Vac4   string `json:"vac4"`
	Vac5   string `json:"vac5"`
	Vac6   string `json:"vac6"`
	Vac7   string `json:"vac7"`
	Vac8   string `json:"vac8"`
	Vac9   string `json:"vac9"`
}

// ownerWriteAllowedTPs lists the procedure categories an owner is
// allowed to self-record from the mobile app. Per partner spec
// (item 7 from the 2026-04-28 review), owners can record records in
// every category — the form is just dramatically simpler than the
// clinic side: editable name, diagnosis, prescription, comment.
//
// Tests are still clinic-only because they require a panel of column
// values that the simplified form doesn't expose, and would otherwise
// produce empty test panels in history.
//
// Mirrors the `OWNER_WRITE_ALLOWED_NAMES` set in the mobile app —
// both sides must agree.
var ownerWriteAllowedTPs = map[int]bool{
	1:   true, // vaccination
	11:  true, // ectoparasite
	12:  true, // dehelminization
	101: true, // stomatology
	102: true, // cardiology
	103: true, // oxygen therapy
	104: true, // traumatology
	105: true, // dermatology
	106: true, // surgery
	107: true, // other
	108: true, // consultation
	109: true, // radiology
	110: true, // sterilization
	115: true, // microchip
	116: true, // laboratory
	202: true, // therapy
	203: true, // ophthalmology
	// 2 / 22 / 222 (test) excluded — those need a structured panel.
}

// CreateProcedure adds a medical record to one of the owner's pets.
// Owner-created records have empty `vetname`/`sk` and their `owner` field is
// set to the owner's personal ID — this is what `DeleteProcedure` uses to
// decide whether the owner is allowed to remove the record.
// @Summary Owner adds procedure to pet
// @Tags owner
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param body body OwnerCreateProcedureRequest true "Procedure data"
// @Success 201 {object} OwnerProcedureItem
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /owner/pets/{id}/procedures [post]
func (h *OwnerPortalHandler) CreateProcedure(w http.ResponseWriter, r *http.Request) {
	petID := chi.URLParam(r, "id")
	personalID := ownerPersonalID(r)

	pet, err := h.verifyPetOwnership(petID, personalID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var req OwnerCreateProcedureRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// Owners can self-record only a small subset of categories (home
	// boosters, ecto/dehel treatments). Anything else is clinic-only —
	// reject so a buggy/older mobile client can't bypass the UI gate.
	if !ownerWriteAllowedTPs[req.TP] {
		writeJSON(w, http.StatusForbidden, ErrorResponse{
			Error: "owners can only add vaccination, ectoparasite, or dehelminization records",
		})
		return
	}

	// Auto-populate the human-readable procedure name when the client
	// didn't send one. Without this, owner-added records show up as raw
	// numeric tp ("1", "3", …) in the history list.
	tpName := req.TPName
	if tpName == "" {
		tpName = procedureNameForTP(req.TP)
	}

	// `date3` is date2 in the PHP calendar's JavaScript-month encoding —
	// see legacyDate3. Always derived: the mobile client never sends it,
	// and a client-supplied value could disagree with date2.
	date3 := legacyDate3(req.Date2)

	proc := models.Procedure{
		UUID:   petID,
		TP:     req.TP,
		Date:   req.Date,
		Date2:  req.Date2,
		Date3:  date3,
		TPName: tpName,
		Vac:    req.Vac,
		VacN:   req.VacN,
		Ser:    req.Ser,
		Deh:    req.Deh,
		Diagn:  req.Diagn,
		Nout:   req.Nout,
		Anam:   req.Anam,
		Coment: req.Coment,
		Dani:   req.Dani,
		Vac1:   req.Vac1,
		Vac2:   req.Vac2,
		Vac3:   req.Vac3,
		Vac4:   req.Vac4,
		Vac5:   req.Vac5,
		Vac6:   req.Vac6,
		Vac7:   req.Vac7,
		Vac8:   req.Vac8,
		Vac9:   req.Vac9,
		PName:  pet.Name,
		Owner:  personalID,
		// VetName + SK intentionally empty — this is what marks the
		// record as owner-created (used by DeleteProcedure auth and by
		// the mobile accordion's badge / delete affordance).
		Phone: "0", // unpaid — owner records don't go through clinic billing
	}

	if err := h.db.Create(&proc).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create procedure"})
		return
	}

	// Build the response the same way the list endpoint does so the
	// shape stays identical between create-201 and the next GET refresh.
	// Pass an empty vetNames map — the owner-created record has no
	// numeric vetname to resolve.
	item := buildOwnerProcedureItem(&proc, map[string]string{}, map[string]string{})
	writeJSON(w, http.StatusCreated, item)
}

// DeleteProcedure removes an owner-created medical record from the pet.
// Records created by a vet (non-empty `vetname`) cannot be deleted by the
// owner — returns 403 in that case.
// @Summary Owner deletes self-added procedure
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param procId path int true "Procedure ID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /owner/pets/{id}/procedures/{procId} [delete]
func (h *OwnerPortalHandler) DeleteProcedure(w http.ResponseWriter, r *http.Request) {
	petID := chi.URLParam(r, "id")
	procID := chi.URLParam(r, "procId")
	personalID := ownerPersonalID(r)

	if _, err := h.verifyPetOwnership(petID, personalID); err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var proc models.Procedure
	if err := h.db.Where("id = ? AND uuid = ?", procID, petID).First(&proc).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "procedure not found"})
		return
	}

	// Owners may only delete records they themselves created. Vet-entered
	// records have a non-empty vetname and are read-only from the owner app.
	if proc.VetName != "" {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "cannot delete clinic-entered record"})
		return
	}

	if err := h.db.Delete(&proc).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete procedure"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "procedure deleted"})
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
	// date2 is the appointment day (see models.Appointment).
	h.db.Where("owner = ?", personalID).Order("date2 DESC, time ASC").Find(&appointments)

	// Resolve vet member IDs to display names in one query, the same way
	// the procedures endpoint does.
	rawVetIDs := make([]string, 0, len(appointments))
	for _, a := range appointments {
		rawVetIDs = append(rawVetIDs, a.VetName)
	}
	vetNames := h.resolveVetNames(rawVetIDs)

	// Resolve pet names for the appointments that reference a pet id.
	// `pname` on the appointment is free text the clinic typed and is
	// often a description rather than a name ("ძაღლი ჩარლი მენჯოს..."),
	// so prefer the actual pet record and fall back to pname.
	petIDSet := make(map[string]struct{}, len(appointments))
	for _, a := range appointments {
		if id := strings.TrimSpace(a.UUID); id != "" {
			petIDSet[id] = struct{}{}
		}
	}
	petNames := make(map[string]string, len(petIDSet))
	if len(petIDSet) > 0 {
		// Integer ids so the lookup uses pets_pkey; `id::text IN` scanned
		// the whole table.
		ids := make([]uint64, 0, len(petIDSet))
		for id := range petIDSet {
			if n, err := strconv.ParseUint(id, 10, 32); err == nil {
				ids = append(ids, n)
			}
		}
		type petRow struct {
			ID   string
			Name string
		}
		var rows []petRow
		if len(ids) > 0 {
			h.db.Table("pets").
				Select("id::text AS id, name").
				Where("id IN ?", ids).
				Scan(&rows)
		}
		for _, p := range rows {
			if n := strings.TrimSpace(p.Name); n != "" {
				petNames[p.ID] = n
			}
		}
	}

	today := todayGeorgia()

	items := make([]OwnerVisit, len(appointments))
	for i, a := range appointments {
		petID := strings.TrimSpace(a.UUID)
		petName := petNames[petID]
		if petName == "" {
			petName = strings.TrimSpace(a.PName)
		}

		items[i] = OwnerVisit{
			ID:          a.ID,
			Date:        a.Date,
			Time:        a.Time,
			Operation:   a.TPName,
			VetName:     a.VetName,
			VetFullName: vetNames[strings.TrimSpace(a.VetName)],
			PetID:       petID,
			PetName:     petName,
			Status:      a.Status,
			// TEXT ISO dates compare correctly lexicographically.
			Upcoming: a.Date >= today,
		}
	}

	writeJSON(w, http.StatusOK, items)
}
