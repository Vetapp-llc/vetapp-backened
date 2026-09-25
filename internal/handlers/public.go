package handlers

import (
	"net/http"
	"strconv"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// PublicHandler handles unauthenticated public endpoints.
type PublicHandler struct {
	db *gorm.DB
}

// NewPublicHandler creates a new PublicHandler.
func NewPublicHandler(db *gorm.DB) *PublicHandler {
	return &PublicHandler{db: db}
}

// --- Response types ---

// PublicPetInfo is the public view of a pet (no sensitive data).
type PublicPetInfo struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Species   string  `json:"species"`
	Breed     string  `json:"breed"`
	Sex       string  `json:"sex"`
	Chip      string  `json:"chip"`
	Birth     *string `json:"birth"`
	Color     string  `json:"color"`
	Castrated bool    `json:"castrated"`
}

// ProcedureCategoryCount is a procedure type with its record count.
type ProcedureCategoryCount struct {
	TP    int    `json:"tp"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// PublicPetResponse is the full public pet profile.
type PublicPetResponse struct {
	Pet        PublicPetInfo            `json:"pet"`
	Categories []ProcedureCategoryCount `json:"categories"`
	// ShareToken (PIN lookups only) is what the page passes as `s` to
	// load the history.
	ShareToken string `json:"share_token,omitempty"`
}

// PIN guessing limits: per client IP, and overall — lookups made through
// the web app's server all arrive from Vercel, and X-Forwarded-For can
// be forged, so the global cap is what bounds a determined attacker
// (120/min against ~1M PINs, each live for an hour).
var (
	publicCodeLimiter       = middleware.NewRateLimiter(10, time.Minute)
	publicCodeGlobalLimiter = middleware.NewRateLimiter(120, time.Minute)
)

// procedureTypeNames maps TP codes to canonical Georgian display names.
//
// This mapping must mirror the legacy PHP app's scheme — see
// `ProcedureHandler.Types()` for the source-of-truth comment block. Any
// new tp added here must also be added in `procedureNameForTP` (used for
// auto-populating `tpname` on owner-created records) and in the mobile
// app's `CATEGORY_TP` constant.
var procedureTypeNames = map[int]string{
	1:   "ვაქცინაცია",
	2:   "ანალიზი (ძაღლი)",
	22:  "ანალიზი (კატა)",
	222: "ანალიზი (სხვა)",
	11:  "ექტოპარაზიტების პრევენცია",
	12:  "დეჰელმინთიზაცია",
	101: "სტომატოლოგია",
	102: "კარდიოლოგია",
	103: "ოქსიგენოთერაპია",
	104: "ტრავმატოლოგია",
	105: "დერმატოლოგია",
	106: "ქირურგია",
	107: "სხვა პროცედურა",
	108: "კონსულტაცია",
	109: "რადიოლოგია",
	110: "სტერილიზაცია/კასტრაცია",
	115: "მიკროჩიპი",
	116: "ლაბორატორია",
	202: "თერაპია",
	203: "ოფთალმოლოგია",
}

// GetPet returns public pet info and procedure category counts.
// @Summary Get public pet profile
// @Tags public
// @Produce json
// @Param id path int true "Pet ID"
// @Param s query string true "Share token (QR link token or PIN)"
// @Success 200 {object} PublicPetResponse
// @Failure 404 {object} ErrorResponse
// @Router /public/pets/{id} [get]
func (h *PublicHandler) GetPet(w http.ResponseWriter, r *http.Request) {
	pet, ok := h.sharedPet(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	h.writePetProfile(w, pet, "")
}

// sharedPet loads the {id} pet if the request carries a live share token
// for it. Missing, wrong and expired tokens all look like "not found".
func (h *PublicHandler) sharedPet(r *http.Request) (models.Pet, bool) {
	var pet models.Pet
	id := petIDParam(chi.URLParam(r, "id"))
	if id == 0 || !shareGranted(h.db, r, id) {
		return pet, false
	}
	if err := h.db.First(&pet, id).Error; err != nil {
		return pet, false
	}
	return pet, true
}

// LookupByCode returns the pet a live 6-digit share PIN opens.
// @Summary Look up pet by PIN code
// @Tags public
// @Produce json
// @Param code path string true "6-digit share PIN"
// @Success 200 {object} PublicPetResponse
// @Failure 404 {object} ErrorResponse
// @Router /public/pets/code/{code} [get]
func (h *PublicHandler) LookupByCode(w http.ResponseWriter, r *http.Request) {
	if !publicCodeLimiter.Allow(r.RemoteAddr) || !publicCodeGlobalLimiter.Allow("all") {
		writeJSON(w, http.StatusTooManyRequests, ErrorResponse{
			Error: "too many attempts", Code: codeRateLimited,
			RetryAfter: int(publicCodeLimiter.RetryAfter(r.RemoteAddr).Seconds()) + 1,
		})
		return
	}
	code := chi.URLParam(r, "code")
	if len(code) != 6 {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var petID uint
	h.db.Raw(`SELECT pet_id FROM pet_share_tokens WHERE token = ? AND kind = 'pin' AND expires_at > NOW()`, code).
		Scan(&petID)
	var pet models.Pet
	if petID == 0 || h.db.First(&pet, petID).Error != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	h.writePetProfile(w, pet, code)
}

// PublicProcedureItem is a single procedure in the public view.
type PublicProcedureItem struct {
	ID        string `json:"id"`
	Date      string `json:"date"`
	NextDate  string `json:"nextDate,omitempty"`
	Name      string `json:"name,omitempty"`
	Diagnosis string `json:"diagnosis,omitempty"`
	Notes     string `json:"notes,omitempty"`
	VetName   string `json:"vetName,omitempty"`
}

// GetPetProcedures returns public procedure list for a pet, filtered by tp.
// @Summary Get public pet procedures
// @Tags public
// @Produce json
// @Param id path int true "Pet ID"
// @Param s query string true "Share token (QR link token or PIN)"
// @Param tp query int false "Procedure type"
// @Success 200 {array} PublicProcedureItem
// @Failure 404 {object} ErrorResponse
// @Router /public/pets/{id}/procedures [get]
func (h *PublicHandler) GetPetProcedures(w http.ResponseWriter, r *http.Request) {
	pet, ok := h.sharedPet(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	petID := strconv.Itoa(int(pet.ID))
	tpStr := r.URL.Query().Get("tp")

	// Special case: tp=999 returns allergies
	if tpStr == "999" {
		var allergies []models.Allergy
		h.db.Where("uuid = ?", petID).Order("id DESC").Find(&allergies)

		items := make([]PublicProcedureItem, 0, len(allergies))
		for _, a := range allergies {
			items = append(items, PublicProcedureItem{
				ID:    strconv.Itoa(int(a.ID)),
				Date:  a.Date,
				Name:  a.Name,
				Notes: "",
			})
		}
		writeJSON(w, http.StatusOK, items)
		return
	}

	query := h.db.Where("uuid = ?", petID)
	if tpStr != "" {
		query = query.Where("tp = ?", tpStr)
	}

	var procs []models.Procedure
	query.Order("date DESC, id DESC").Find(&procs)

	items := make([]PublicProcedureItem, 0, len(procs))
	for _, p := range procs {
		item := PublicProcedureItem{
			ID:        strconv.Itoa(int(p.ID)),
			Date:      p.Date,
			NextDate:  p.Date2,
			Name:      p.Vac,
			Diagnosis: p.Diagn,
			Notes:     p.Nout,
			VetName:   p.PName,
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, items)
}

// writePetProfile builds and writes the public pet profile response.
// Category counts are produced by the shared `buildPetCategories`
// helper (parallel queries, single source of truth for the tp=999
// allergy bucket convention).
func (h *PublicHandler) writePetProfile(w http.ResponseWriter, pet models.Pet, shareToken string) {
	petID := strconv.Itoa(int(pet.ID))

	info := PublicPetInfo{
		ID:        petID,
		Name:      pet.Name,
		Species:   pet.Pet,
		Breed:     pet.Variety,
		Sex:       pet.Sex,
		Chip:      pet.Chip,
		Color:     pet.Color,
		Castrated: pet.Cast != "",
	}
	if pet.Date != "" {
		info.Birth = &pet.Date
	}

	writeJSON(w, http.StatusOK, PublicPetResponse{
		Pet:        info,
		Categories: buildPetCategories(h.db, petID),
		ShareToken: shareToken,
	})
}
