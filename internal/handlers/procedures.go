package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

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
// @Success 200 {array} models.Procedure
// @Failure 500 {object} ErrorResponse
// @Router /procedures [get]
func (h *ProcedureHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	query := h.db.Model(&models.Procedure{})

	// Default to user's clinic
	clinic := r.URL.Query().Get("clinic")
	if clinic == "" {
		clinic = claims.Zip
	}
	query = query.Where("sk = ?", clinic)

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

	var procedures []models.Procedure
	if err := query.Order("date DESC, id DESC").Find(&procedures).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch procedures"})
		return
	}

	writeJSON(w, http.StatusOK, procedures)
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
	id := chi.URLParam(r, "id")

	var proc models.Procedure
	if err := h.db.First(&proc, id).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "procedure not found"})
		return
	}

	writeJSON(w, http.StatusOK, proc)
}

// Create adds a new procedure.
// @Summary Create procedure
// @Tags procedures
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body CreateProcedureRequest true "Procedure data"
// @Success 201 {object} models.Procedure
// @Failure 400 {object} ErrorResponse
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

	proc := models.Procedure{
		UUID:    req.UUID,
		TP:      req.TP,
		Date:    req.Date,
		Date2:   req.Date2,
		Date3:   req.Date3,
		TPName:  req.TPName,
		Vac:     req.Vac,
		VacN:    req.VacN,
		Phone:   req.Phone,
		Price:   req.Price,
		PName:   req.PName,
		Owner:   req.Owner,
		OwnerN:  req.OwnerN,
		Anam:    req.Anam,
		Diagn:   req.Diagn,
		Nout:    req.Nout,
		Koment:  req.Koment,
		Coment:  req.Coment,
		Dani:    req.Dani,
		Ser:     req.Ser,
		Deh:     req.Deh,
		Vac1:    req.Vac1,
		Vac2:    req.Vac2,
		Vac3:    req.Vac3,
		Vac4:    req.Vac4,
		Vac5:    req.Vac5,
		Vac6:    req.Vac6,
		Vac7:    req.Vac7,
		Vac8:    req.Vac8,
		Vac9:    req.Vac9,
		SK:      claims.Zip,
		VetName: formatUint(claims.UserID),
	}

	// Default payment status to unpaid
	if proc.Phone == "" {
		proc.Phone = "0"
	}

	if err := h.db.Create(&proc).Error; err != nil {
		log.Error("procedure_create_failed", "error", err, "type", req.TP, "pet_id", req.UUID)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create procedure"})
		return
	}

	log.Info("procedure_created", "id", proc.ID, "type", proc.TP, "type_name", proc.TPName, "pet_id", proc.UUID)

	writeJSON(w, http.StatusCreated, proc)
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
// @Failure 500 {object} ErrorResponse
// @Router /procedures/{id} [put]
func (h *ProcedureHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var proc models.Procedure
	if err := h.db.First(&proc, id).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "procedure not found"})
		return
	}

	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	// Protect immutable fields
	delete(updates, "id")
	delete(updates, "sk")

	if err := h.db.Model(&proc).Updates(updates).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update procedure"})
		return
	}

	writeJSON(w, http.StatusOK, proc)
}

// Delete removes a procedure.
// @Summary Delete procedure
// @Tags procedures
// @Produce json
// @Security BearerAuth
// @Param id path int true "Procedure ID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /procedures/{id} [delete]
func (h *ProcedureHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var proc models.Procedure
	if err := h.db.First(&proc, id).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "procedure not found"})
		return
	}

	if err := h.db.Delete(&proc).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete procedure"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "procedure deleted"})
}

// Types returns all procedure type codes and names.
// @Summary List procedure types
// @Tags procedures
// @Produce json
// @Security BearerAuth
// @Success 200 {array} ProcedureTypeItem
// @Router /procedures/types [get]
func (h *ProcedureHandler) Types(w http.ResponseWriter, r *http.Request) {
	types := []ProcedureTypeItem{
		{TP: 1, Name: "ვაქცინაცია (Vaccination)"},
		{TP: 101, Name: "ცოფის ვაქცინა (Rabies)"},
		{TP: 2, Name: "ანალიზი (Test)"},
		{TP: 3, Name: "დეჰელმინთიზაცია (Dehelminization)"},
		{TP: 4, Name: "ექტოპარაზიტი (Ectoparasite)"},
		{TP: 5, Name: "ქირურგია (Surgery)"},
		{TP: 6, Name: "სტომატოლოგია (Dental)"},
		{TP: 7, Name: "რენტგენი (X-Ray)"},
		{TP: 8, Name: "ულტრაბგერა (Ultrasound)"},
		{TP: 9, Name: "ელექტროკარდიოგრამა (ECG)"},
		{TP: 10, Name: "ენდოსკოპია (Endoscopy)"},
		{TP: 100, Name: "სტერილიზაცია (Sterilization)"},
		{TP: 102, Name: "ჩიპირება (Microchipping)"},
		{TP: 103, Name: "ევთანაზია (Euthanasia)"},
		{TP: 104, Name: "ლაბორატორია (Laboratory)"},
		{TP: 108, Name: "კონსულტაცია (Consultation)"},
		{TP: 109, Name: "მანიპულაცია (Manipulation)"},
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
