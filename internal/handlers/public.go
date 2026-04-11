package handlers

import (
	"net/http"
	"strconv"

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
	Pet        PublicPetInfo           `json:"pet"`
	Categories []ProcedureCategoryCount `json:"categories"`
}

// procedureTypeNames maps TP codes to display names.
var procedureTypeNames = map[int]string{
	1:   "ვაქცინაცია",
	101: "ცოფის ვაქცინა",
	2:   "ანალიზი",
	3:   "დეჰელმინთიზაცია",
	4:   "ექტოპარაზიტი",
	5:   "ქირურგია",
	6:   "სტომატოლოგია",
	7:   "რენტგენი",
	8:   "ულტრაბგერა",
	9:   "ელექტროკარდიოგრამა",
	10:  "ენდოსკოპია",
	100: "სტერილიზაცია",
	102: "ჩიპირება",
	103: "ევთანაზია",
	104: "ლაბორატორია",
	108: "კონსულტაცია",
	109: "მანიპულაცია",
}

// GetPet returns public pet info and procedure category counts.
// @Summary Get public pet profile
// @Tags public
// @Produce json
// @Param id path int true "Pet ID"
// @Success 200 {object} PublicPetResponse
// @Failure 404 {object} ErrorResponse
// @Router /public/pets/{id} [get]
func (h *PublicHandler) GetPet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var pet models.Pet
	if err := h.db.First(&pet, id).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	h.writePetProfile(w, pet)
}

// LookupByCode returns public pet info by the 4-digit access code.
// @Summary Look up pet by PIN code
// @Tags public
// @Produce json
// @Param code path string true "4-digit access code"
// @Success 200 {object} PublicPetResponse
// @Failure 404 {object} ErrorResponse
// @Router /public/pets/code/{code} [get]
func (h *PublicHandler) LookupByCode(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	if len(code) != 4 {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var pet models.Pet
	if err := h.db.Where("code = ?", code).First(&pet).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	h.writePetProfile(w, pet)
}

// writePetProfile builds and writes the public pet profile response.
func (h *PublicHandler) writePetProfile(w http.ResponseWriter, pet models.Pet) {
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

	// Count procedures by type
	type tpCount struct {
		TP    int
		Count int
	}
	var counts []tpCount
	h.db.Model(&models.Procedure{}).
		Select("tp as tp, COUNT(*) as count").
		Where("uuid = ?", petID).
		Group("tp").
		Scan(&counts)

	// Count allergies
	var allergyCount int64
	h.db.Model(&models.Allergy{}).Where("uuid = ?", petID).Count(&allergyCount)

	// Build categories — include all types that have records
	categories := make([]ProcedureCategoryCount, 0, len(counts)+1)
	for _, c := range counts {
		name := procedureTypeNames[c.TP]
		if name == "" {
			name = "სხვა"
		}
		categories = append(categories, ProcedureCategoryCount{
			TP:    c.TP,
			Name:  name,
			Count: c.Count,
		})
	}

	// Add allergies if any
	if allergyCount > 0 {
		categories = append(categories, ProcedureCategoryCount{
			TP:    999,
			Name:  "ალერგია / დაავადება",
			Count: int(allergyCount),
		})
	}

	writeJSON(w, http.StatusOK, PublicPetResponse{
		Pet:        info,
		Categories: categories,
	})
}
