package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// ProcedureHandler handles medical procedure endpoints.
type ProcedureHandler struct {
	db *gorm.DB
}

// NewProcedureHandler creates a new ProcedureHandler.
func NewProcedureHandler(db *gorm.DB) *ProcedureHandler {
	return &ProcedureHandler{db: db}
}

// --- Request/Response types for Swagger ---

// CreateProcedureRequest is the request body for creating a procedure.
type CreateProcedureRequest struct {
	UUID   string `json:"uuid" validate:"required"`
	TP     int    `json:"tp" validate:"required,min=1"`
	Date   string `json:"date"`
	Date2  string `json:"date2"`
	Date3  string `json:"date3"`
	TPName string `json:"tpname"`
	Vac    string `json:"vac"`
	VacN   string `json:"vacn"`
	Phone  string `json:"phone"`
	Price  string `json:"price"`
	PName  string `json:"pname"`
	Owner  string `json:"owner"`
	OwnerN string `json:"ownern"`
	Anam   string `json:"anam"`
	Diagn  string `json:"diagn"`
	Nout   string `json:"nout"`
	Koment string `json:"koment"`
	Coment string `json:"coment"`
	Dani   string `json:"dani"`
	Ser    string `json:"ser"`
	Deh    string `json:"deh"`
	Vac1   string `json:"vac1"`
	Vac2   string `json:"vac2"`
	Vac3   string `json:"vac3"`
	Vac4   string `json:"vac4"`
	Vac5   string `json:"vac5"`
	Vac6   string `json:"vac6"`
	Vac7   string `json:"vac7"`
	Vac8   string `json:"vac8"`
	Vac9   string `json:"vac9"`
	Test1  string `json:"test1"`
	Test2  string `json:"test2"`
	Test3  string `json:"test3"`
	Test4  string `json:"test4"`
	Test5  string `json:"test5"`
	Test6  string `json:"test6"`
	Test7  string `json:"test7"`
	Test8  string `json:"test8"`
	// VetName is the member id of the vet who performed the procedure,
	// chosen from the clinic's staff (PHP's "ვეტერინარი" dropdown).
	// Defaults to the caller. Must be a vet at the caller's clinic.
	VetName string `json:"vetname"`
	// Address and Sax are test-result columns on the dog test (Giardia and
	// Erlichia canis, see procedure_forms.go). On every other tp they hold
	// the owner address / pet sex and are filled from the pet instead.
	Address string `json:"address"`
	Sax     string `json:"sax"`
	// Chip is the microchip number for tp=115. It is written to the pet
	// record as well as the procedure (PHP stores it in `coment` too).
	Chip string `json:"chip"`
}

// ProcedureTypeItem represents a procedure type option.
type ProcedureTypeItem struct {
	TP   int    `json:"tp" validate:"required"`
	Name string `json:"name" validate:"required"`
}

// SelectOption represents a value/label option.
type SelectOption struct {
	Value string `json:"value" validate:"required"`
	Label string `json:"label" validate:"required"`
}

// VaccineOptionsResponse is the response for vaccine options.
type VaccineOptionsResponse struct {
	Vaccines []SelectOption `json:"vaccines" validate:"required"`
	Brands   []SelectOption `json:"brands" validate:"required"`
}

// EctoOptionsResponse is the response for ectoparasite options.
type EctoOptionsResponse struct {
	Drops   []SelectOption `json:"drops" validate:"required"`
	Collars []SelectOption `json:"collars" validate:"required"`
	Tablets []SelectOption `json:"tablets" validate:"required"`
	Sprays  []SelectOption `json:"sprays" validate:"required"`
}

// List returns procedures (daily register).
// @Summary List procedures
// @Tags procedures
// @Produce json
// @Security BearerAuth
// @Param clinic query string false "Clinic code"
// @Param date_from query string false "Start date (YYYY-MM-DD)"
// @Param date_to query string false "End date (YYYY-MM-DD)"
// @Param vet_id query string false "Vet member ID"
// @Param pet_id query string false "Pet ID"
// @Param tp query int false "Procedure type code"
// @Param page query int false "Page number (default 1)"
// @Param pageSize query int false "Rows per page (default 50, max 200)"
// @Success 200 {object} PaginatedResponse
// @Failure 500 {object} ErrorResponse
// @Router /procedures [get]
func (h *ProcedureHandler) List(w http.ResponseWriter, r *http.Request) {
	clinic, ok := resolveClinic(w, r)
	if !ok {
		return
	}
	query := h.db.Model(&models.Procedure{}).Where("sk = ?", clinic)

	// Date range
	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		query = query.Where("date >= ?", dateFrom)
	}
	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		query = query.Where("date <= ?", dateTo)
	}

	// Filter by vet
	if vetID := r.URL.Query().Get("vet_id"); vetID != "" {
		query = query.Where("vetname = ?", vetID)
	}

	// Filter by pet
	if petID := r.URL.Query().Get("pet_id"); petID != "" {
		query = query.Where("uuid = ?", petID)
	}

	// Filter by procedure type
	if tp := r.URL.Query().Get("tp"); tp != "" {
		query = query.Where("tp = ?", tp)
	}

	// Unpaid only — the "today's unpaid items" panel on the PHP pet page.
	if r.URL.Query().Get("unpaid") == "1" {
		query = query.Where("phone = ?", "0")
	}

	// Paginated: unbounded, this returned every procedure the clinic
	// had ever recorded — 84,847 rows (~24.5 MB) for the busiest clinic
	// in production, held open on one connection for the duration.
	page := ParsePageParams(r)

	var total int64
	query.Count(&total)

	var procedures []models.Procedure
	if err := page.Paginate(query).Order("date DESC, id DESC").Find(&procedures).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch procedures"})
		return
	}

	writeJSON(w, http.StatusOK, NewPaginatedResponse(procedures, total, page))
}

// findOwnProcedure loads a procedure by id, restricted to the caller's
// clinic. A procedure at another clinic is reported as not found.
func (h *ProcedureHandler) findOwnProcedure(r *http.Request, id string) (models.Procedure, bool) {
	var proc models.Procedure
	err := ownedByCaller(h.db, r, "sk").Where("id = ?", id).First(&proc).Error
	return proc, err == nil
}

// Get returns a single procedure by ID.
// @Summary Get procedure by ID
// @Tags procedures
// @Produce json
// @Security BearerAuth
// @Param id path int true "Procedure ID"
// @Success 200 {object} models.Procedure
// @Failure 404 {object} ErrorResponse
// @Router /procedures/{id} [get]
func (h *ProcedureHandler) Get(w http.ResponseWriter, r *http.Request) {
	proc, ok := h.findOwnProcedure(r, chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "procedure not found"})
		return
	}
	writeJSON(w, http.StatusOK, proc)
}

// Create adds a new procedure.
//
// Everything that describes the pet or the owner on the row
// (pname/owner/ownern/pet/sax) is copied from the pets table rather than
// taken from the request, and the paid flag always starts at "0": the
// only way to mark a procedure paid is POST /payments/record.
//
// Side effects, matching the PHP forms and done in the same transaction:
//   - tp=115 (microchip, vet/addprocedure4.php) sets pets.chip/chipd
//   - tp=110 (sterilisation, vet/addprocedure3.php) sets pets.cast/castdate
//
// The pet may be registered at another clinic: any clinic can treat any
// pet, as in PHP, and the record belongs to the clinic that wrote it.
//
// @Summary Create procedure
// @Tags procedures
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreateProcedureRequest true "Procedure data"
// @Success 201 {object} models.Procedure
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /procedures [post]
func (h *ProcedureHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	log := middleware.RequestLogger(r)

	var req CreateProcedureRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if claims.Zip == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "procedures are recorded by a clinic; this account has no clinic"})
		return
	}

	tpName := procedureNameForTP(req.TP)
	if tpName == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "unknown procedure type"})
		return
	}
	if req.TPName == "" {
		req.TPName = tpName
	}

	if req.Date == "" {
		req.Date = todayGeorgia()
	}
	if !isISODate(req.Date) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date must be YYYY-MM-DD"})
		return
	}
	if req.Date2 != "" && !isISODate(req.Date2) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date2 must be YYYY-MM-DD"})
		return
	}
	// Revenue reports sum this column; free text ("40 ლარი") would make
	// them skip the row.
	if req.Price != "" && !isMoney(req.Price) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "price must be a number, e.g. 40 or 12.50"})
		return
	}

	// The pet must be one this clinic may open, or the request must name
	// its owner's personal ID (proof of the exact lookup). A guessed pet id
	// alone must not let a clinic write — and so gain access — to a pet.
	if !canAccessPetWithProof(h.db, r, req.UUID, req.Owner) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}
	var pet models.Pet
	if err := h.db.Where("id = ?", req.UUID).First(&pet).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	vetName, ok := clinicVet(h.db, claims.Zip, claims.UserID, req.VetName)
	if !ok {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "vet is not a member of this clinic"})
		return
	}

	chip := strings.TrimSpace(req.Chip)
	if req.TP == tpMicrochip {
		if chip == "" {
			chip = strings.TrimSpace(req.Coment)
		}
		if chip == "" {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "microchip number is required"})
			return
		}
		// PHP keeps the chip number in the procedure's comment.
		req.Coment = chip
	}

	proc := models.Procedure{
		UUID:       strconv.Itoa(int(pet.ID)),
		TP:         req.TP,
		Date:       req.Date,
		Date2:      req.Date2,
		Date3:      legacyDate3(req.Date2),
		TPName:     req.TPName,
		Vac:        req.Vac,
		VacN:       req.VacN,
		Phone:      "0",
		Price:      req.Price,
		PName:      pet.Name,
		Owner:      pet.UUID,
		OwnerN:     pet.FirstName,
		PetSpecies: pet.Pet,
		Sax:        pet.Sex,
		Address:    "",
		Anam:       req.Anam,
		Diagn:      req.Diagn,
		Nout:       req.Nout,
		Koment:     req.Koment,
		Coment:     req.Coment,
		Dani:       req.Dani,
		Ser:        req.Ser,
		Deh:        req.Deh,
		Vac1:       req.Vac1,
		Vac2:       req.Vac2,
		Vac3:       req.Vac3,
		Vac4:       req.Vac4,
		Vac5:       req.Vac5,
		Vac6:       req.Vac6,
		Vac7:       req.Vac7,
		Vac8:       req.Vac8,
		Vac9:       req.Vac9,
		Test1:      req.Test1,
		Test2:      req.Test2,
		Test3:      req.Test3,
		Test4:      req.Test4,
		Test5:      req.Test5,
		Test6:      req.Test6,
		Test7:      req.Test7,
		Test8:      req.Test8,
		SK:         claims.Zip,
		VetName:    vetName,
	}

	if isTestTP(req.TP) {
		proc.Sax, proc.Address = req.Sax, req.Address
	}

	var replayed bool
	err := h.db.Transaction(func(tx *gorm.DB) error {
		prior, err := claimIdempotency(tx, r, "procedures", req)
		if err != nil {
			return err
		}
		if prior != 0 {
			replayed = true
			return tx.First(&proc, prior).Error
		}
		if err := tx.Create(&proc).Error; err != nil {
			return err
		}
		if err := recordIdempotency(tx, r, proc.ID); err != nil {
			return err
		}
		switch req.TP {
		case tpMicrochip:
			return tx.Model(&models.Pet{}).Where("id = ?", pet.ID).
				Updates(map[string]interface{}{"chip": chip, "chipd": req.Date}).Error
		case tpSterilisation:
			if req.Vac != "" {
				return tx.Model(&models.Pet{}).Where("id = ?", pet.ID).
					Updates(map[string]interface{}{"cast": req.Vac, "castdate": req.Date}).Error
			}
		}
		return nil
	})
	if errors.Is(err, errIdempotencyMismatch) {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: err.Error()})
		return
	}
	if replayed && errors.Is(err, gorm.ErrRecordNotFound) {
		// The original record was created and has since been deleted.
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "this procedure was already saved and has since been deleted"})
		return
	}
	if replayed && err == nil {
		writeJSON(w, http.StatusCreated, proc)
		return
	}
	if err != nil {
		log.Error("procedure_create_failed", "error", err, "type", req.TP, "pet_id", req.UUID)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create procedure"})
		return
	}

	log.Info("procedure_created", "id", proc.ID, "type", proc.TP, "type_name", proc.TPName, "pet_id", proc.UUID)

	writeJSON(w, http.StatusCreated, proc)
}

// procedureEditableFields are the columns a clinic may change on an
// existing record — the ones the PHP update*.php forms rewrite. The pet,
// clinic, type and paid flag are fixed once written.
var procedureEditableFields = []string{
	"date", "date2", "vac", "vacn", "ser", "deh",
	"vac1", "vac2", "vac3", "vac4", "vac5", "vac6", "vac7", "vac8", "vac9",
	"test1", "test2", "test3", "test4", "test5", "test6", "test7", "test8",
	"anam", "diagn", "nout", "koment", "coment", "dani", "price", "vetname",
}

// Update edits an existing procedure.
// @Summary Update procedure
// @Tags procedures
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Procedure ID"
// @Param body body object true "Fields to update"
// @Success 200 {object} models.Procedure
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /procedures/{id} [put]
func (h *ProcedureHandler) Update(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	proc, ok := h.findOwnProcedure(r, chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "procedure not found"})
		return
	}

	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	editable := procedureEditableFields
	if isTestTP(proc.TP) {
		editable = append(editable[:len(editable):len(editable)], "address", "sax")
	}
	updates := pickFields(body, editable...)
	for k, v := range updates {
		if _, isString := v.(string); !isString {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: k + " must be a string"})
			return
		}
	}

	if d, ok := updates["date"].(string); ok && !isISODate(d) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date must be YYYY-MM-DD"})
		return
	}
	if d2, ok := updates["date2"].(string); ok {
		if d2 != "" && !isISODate(d2) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date2 must be YYYY-MM-DD"})
			return
		}
		updates["date3"] = legacyDate3(d2)
	}
	if price, ok := updates["price"].(string); ok && price != "" && !isMoney(price) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "price must be a number, e.g. 40 or 12.50"})
		return
	}
	if price, ok := updates["price"].(string); ok && proc.Phone == "1" && price != proc.Price {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "a paid procedure's price cannot change"})
		return
	}
	if v, ok := updates["vetname"].(string); ok {
		vet, valid := clinicVet(h.db, proc.SK, claims.UserID, v)
		if !valid {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "vet is not a member of this clinic"})
			return
		}
		updates["vetname"] = vet
	}
	if len(updates) == 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "no editable fields in request"})
		return
	}

	// PHP's update pages stamp who changed the record and when.
	updates["name"] = todayGeorgia() + " _ " + claims.FirstName

	// A price change applies only while the record is still unpaid —
	// checked in the UPDATE itself, so a payment committing between the
	// read above and this write cannot be followed by a price change.
	q := h.db.Model(&models.Procedure{}).Where("id = ?", proc.ID)
	if price, ok := updates["price"].(string); ok {
		// Allowed only while unpaid, or when the stored price already is
		// this value — decided on the row as it is now, not as read above.
		q = q.Where("(phone <> ? OR price = ?)", "1", price)
	}
	res := q.Updates(updates)
	if res.Error != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update procedure"})
		return
	}
	if res.RowsAffected == 0 {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "a paid procedure's price cannot change"})
		return
	}
	h.db.First(&proc, proc.ID)

	writeJSON(w, http.StatusOK, proc)
}

// Delete removes an unpaid procedure — the "remove item" action next to
// today's unpaid list in vet/procedures.php. Paid records are part of the
// clinic's revenue history and cannot be deleted.
// @Summary Delete procedure
// @Tags procedures
// @Produce json
// @Security BearerAuth
// @Param id path int true "Procedure ID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /procedures/{id} [delete]
func (h *ProcedureHandler) Delete(w http.ResponseWriter, r *http.Request) {
	proc, ok := h.findOwnProcedure(r, chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "procedure not found"})
		return
	}
	if proc.Phone == "1" {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "paid procedures cannot be deleted"})
		return
	}

	// Conditional on still being unpaid, so a payment racing this delete
	// cannot lose a paid row.
	res := h.db.Where("id = ? AND phone <> ?", proc.ID, "1").Delete(&models.Procedure{})
	if res.Error != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete procedure"})
		return
	}
	if res.RowsAffected == 0 {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "paid procedures cannot be deleted"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "procedure deleted"})
}

// Types returns all procedure type codes and names.
//
// IMPORTANT: this list mirrors the legacy PHP app's tp scheme exactly,
// because production data has been written by PHP for years and the new
// app must read/write into the same buckets. See the per-tp PHP file
// references in the comments below; if you change a number you'll silently
// orphan records.
//
//	tp=1            Vaccination          (vet/addvac.php)
//	tp=2,22,222     Test (dog/cat/other) (vet/addtest.php / addtest1.php / addtest2.php)
//	tp=11           Ectoparasite         (vet/addecto.php) — NOT 4
//	tp=12           Dehelminization      (vet/adddeh.php) — NOT 3
//	tp=101..109,202,203  Sub-specialties (vet/addprocedure.php?tp=…)
//	tp=110          Sterilization        (vet/addprocedure3.php) + side `steril` table
//	tp=115          Microchip            (vet/addprocedure4.php)
//	tp=116          Laboratory           (vet/addprocedure5/6.php)
//
// tp values 3, 4, 5, 555 are legacy orphans — no PHP code emits them; data
// will be migrated to canonical tps in a one-shot SQL pass.
//
// @Summary List procedure types
// @Tags procedures
// @Produce json
// @Security BearerAuth
// @Success 200 {array} ProcedureTypeItem
// @Router /procedures/types [get]
func (h *ProcedureHandler) Types(w http.ResponseWriter, r *http.Request) {
	types := []ProcedureTypeItem{
		{TP: 1, Name: "ვაქცინაცია"},
		{TP: 2, Name: "ანალიზი (ძაღლი)"},
		{TP: 22, Name: "ანალიზი (კატა)"},
		{TP: 222, Name: "ანალიზი (სხვა)"},
		{TP: 11, Name: "ექტოპარაზიტების პრევენცია"},
		{TP: 12, Name: "დეჰელმინთიზაცია"},
		{TP: 101, Name: "სტომატოლოგია"},
		{TP: 102, Name: "კარდიოლოგია"},
		{TP: 103, Name: "ოქსიგენოთერაპია"},
		{TP: 104, Name: "ტრავმატოლოგია"},
		{TP: 105, Name: "დერმატოლოგია"},
		{TP: 106, Name: "ქირურგია"},
		{TP: 107, Name: "სხვა პროცედურა"},
		{TP: 108, Name: "კონსულტაცია"},
		{TP: 109, Name: "რადიოლოგია"},
		{TP: 110, Name: "სტერილიზაცია/კასტრაცია"},
		{TP: 115, Name: "მიკროჩიპი"},
		{TP: 116, Name: "ლაბორატორია"},
		{TP: 202, Name: "თერაპია"},
		{TP: 203, Name: "ოფთალმოლოგია"},
	}
	writeJSON(w, http.StatusOK, types)
}

// VaccineOptions returns vaccine types and brands.
// @Summary List vaccine options
// @Tags procedures
// @Produce json
// @Security BearerAuth
// @Success 200 {object} VaccineOptionsResponse
// @Router /procedures/vaccine-options [get]
func (h *ProcedureHandler) VaccineOptions(w http.ResponseWriter, r *http.Request) {
	options := VaccineOptionsResponse{
		Vaccines: []SelectOption{
			{Value: "კომპლექსური ვაქცინა", Label: "კომპლექსური ვაქცინა"},
			{Value: "ცოფის საწინააღმდეგო ვაქცინა", Label: "ცოფის საწინააღმდეგო ვაქცინა"},
			{Value: "ვოლიერული ხველის საწინააღმდეგო ვაქცინა", Label: "ვოლიერული ხველის საწინააღმდეგო ვაქცინა"},
			{Value: "ანტიმიკოზური ვაქცინა", Label: "ანტიმიკოზური ვაქცინა"},
		},
		Brands: []SelectOption{
			{Value: "Eurican DHPPi2-L", Label: "Eurican DHPPi2-L"},
			{Value: "Nobivac DHPPi+L", Label: "Nobivac DHPPi+L"},
			{Value: "Nobivac Puppy DP", Label: "Nobivac Puppy DP"},
			{Value: "Vanguard plus 5/CV-L", Label: "Vanguard plus 5/CV-L"},
			{Value: "Biocan DHPPi+L", Label: "Biocan DHPPi+L"},
			{Value: "Biocan Novel DHPPi/L4", Label: "Biocan Novel DHPPi/L4"},
			{Value: "Biocan Novel DHPPi/L4R", Label: "Biocan Novel DHPPi/L4R"},
			{Value: "Biocan Novel Puppy", Label: "Biocan Novel Puppy"},
			{Value: "Biocan Puppy", Label: "Biocan Puppy"},
			{Value: "Nobivac Rabies", Label: "Nobivac Rabies"},
			{Value: "Biocan R", Label: "Biocan R"},
			{Value: "Rabisin", Label: "Rabisin"},
			{Value: "Defensor 3", Label: "Defensor 3"},
			{Value: "Nobivac KC", Label: "Nobivac KC"},
			{Value: "Biocan M plus", Label: "Biocan M plus"},
			{Value: "Vacderm", Label: "Vacderm"},
			{Value: "NobivacR Tricat Trio", Label: "NobivacR Tricat Trio"},
			{Value: "Biofel PCH", Label: "Biofel PCH"},
		},
	}
	writeJSON(w, http.StatusOK, options)
}

// TestOptions returns test panel types.
// @Summary List test options
// @Tags procedures
// @Produce json
// @Security BearerAuth
// @Success 200 {array} SelectOption
// @Router /procedures/test-options [get]
func (h *ProcedureHandler) TestOptions(w http.ResponseWriter, r *http.Request) {
	options := []SelectOption{
		{Value: "ტესტი", Label: "ტესტი"},
	}
	writeJSON(w, http.StatusOK, options)
}

// DehelOptions returns dehelminization drug options.
// @Summary List dehelminization options
// @Tags procedures
// @Produce json
// @Security BearerAuth
// @Success 200 {array} SelectOption
// @Router /procedures/dehel-options [get]
func (h *ProcedureHandler) DehelOptions(w http.ResponseWriter, r *http.Request) {
	options := []SelectOption{
		{Value: "Drontal", Label: "Drontal"},
		{Value: "Caniverm", Label: "Caniverm"},
		{Value: "Brovanol", Label: "Brovanol"},
		{Value: "Cestal Plus", Label: "Cestal Plus"},
		{Value: "Milbemax", Label: "Milbemax"},
		{Value: "Caniquantel", Label: "Caniquantel"},
		{Value: "Prazitel", Label: "Prazitel"},
		{Value: "Profender", Label: "Profender"},
	}
	writeJSON(w, http.StatusOK, options)
}

// EctoOptions returns ectoparasite product options.
// @Summary List ectoparasite options
// @Tags procedures
// @Produce json
// @Security BearerAuth
// @Success 200 {object} EctoOptionsResponse
// @Router /procedures/ecto-options [get]
func (h *ProcedureHandler) EctoOptions(w http.ResponseWriter, r *http.Request) {
	options := EctoOptionsResponse{
		Drops: []SelectOption{
			{Value: "Frontline(TRI-ACT)", Label: "Frontline(TRI-ACT)"},
			{Value: "Frontline(Combo)", Label: "Frontline(Combo)"},
			{Value: "Advantix", Label: "Advantix"},
			{Value: "Advocate", Label: "Advocate"},
			{Value: "Advantage", Label: "Advantage"},
			{Value: "Bars", Label: "Bars"},
			{Value: "Chistotel", Label: "Chistotel"},
			{Value: "K9 Advantix II", Label: "K9 Advantix II"},
			{Value: "Dana stop-on", Label: "Dana stop-on"},
			{Value: "Lega", Label: "Lega"},
		},
		Collars: []SelectOption{
			{Value: "Scalibor", Label: "Scalibor"},
			{Value: "Foresto", Label: "Foresto"},
			{Value: "Kiltix", Label: "Kiltix"},
			{Value: "Bars", Label: "Bars"},
			{Value: "Beaphar", Label: "Beaphar"},
			{Value: "Bio", Label: "Bio"},
			{Value: "Protecto", Label: "Protecto"},
			{Value: "Dana", Label: "Dana"},
		},
		Tablets: []SelectOption{
			{Value: "NexGuard(SPECTRA)", Label: "NexGuard(SPECTRA)"},
			{Value: "Bravecto", Label: "Bravecto"},
			{Value: "Simparica", Label: "Simparica"},
		},
		Sprays: []SelectOption{
			{Value: "Frontline(SPRAY)", Label: "Frontline(SPRAY)"},
			{Value: "Bloxnet", Label: "Bloxnet"},
			{Value: "Chistotel", Label: "Chistotel"},
			{Value: "Insektol", Label: "Insektol"},
		},
	}
	writeJSON(w, http.StatusOK, options)
}

// formatUint converts a uint to string.
func formatUint(n uint) string {
	return strconv.FormatUint(uint64(n), 10)
}
