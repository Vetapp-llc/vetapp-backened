package handlers

import (
	"net/http"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"gorm.io/gorm"
)

// StatsHandler handles clinic statistics endpoints.
type StatsHandler struct {
	db *gorm.DB
}

// NewStatsHandler creates a new StatsHandler.
func NewStatsHandler(db *gorm.DB) *StatsHandler {
	return &StatsHandler{db: db}
}

// --- Response types matching frontend ClinicStats ---

type ClinicStats struct {
	TotalPets        int                `json:"totalPets"`
	TotalOwners      int                `json:"totalOwners"`
	TotalRecords     int                `json:"totalRecords"`
	SpeciesBreakdown []SpeciesCount     `json:"speciesBreakdown"`
	TopBreeds        []BreedCount       `json:"topBreeds"`
	SexDistribution  []SexCount         `json:"sexDistribution"`
	TopProcedures    []ProcedureCount   `json:"topProcedures"`
	TopVaccines      []VaccineCount     `json:"topVaccines"`
	MonthlyTrends    []MonthlyTrendItem `json:"monthlyTrends"`
}

type SpeciesCount struct {
	Species string `json:"species"`
	Count   int    `json:"count"`
}
type BreedCount struct {
	Breed string `json:"breed"`
	Count int    `json:"count"`
}
type SexCount struct {
	Sex   string `json:"sex"`
	Count int    `json:"count"`
}
type ProcedureCount struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}
type VaccineCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}
type MonthlyTrendItem struct {
	Month   string `json:"month"`
	Records int    `json:"records"`
}

// Clinic returns clinic stats matching the frontend's ClinicStats interface.
// @Summary Get clinic statistics
// @Tags stats
// @Produce json
// @Security BearerAuth
// @Success 200 {object} ClinicStats
// @Failure 500 {object} ErrorResponse
// @Router /stats/clinic [get]
func (h *StatsHandler) Clinic(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	zip := claims.Zip

	var species []SpeciesCount
	h.db.Raw(`SELECT TRIM(pet) AS species, COUNT(*)::int AS count FROM pets WHERE vet = ? GROUP BY TRIM(pet)`, zip).Scan(&species)

	// Filter out empty species and compute total
	filtered := make([]SpeciesCount, 0)
	totalPets := 0
	for _, s := range species {
		if s.Species != "" {
			filtered = append(filtered, s)
			totalPets += s.Count
		}
	}

	var breeds []BreedCount
	h.db.Raw(`SELECT TRIM(variety) AS breed, COUNT(*)::int AS count FROM pets WHERE vet = ? AND TRIM(COALESCE(variety,'')) != '' GROUP BY TRIM(variety) ORDER BY count DESC LIMIT 10`, zip).Scan(&breeds)

	var sexDist []SexCount
	h.db.Raw(`SELECT TRIM(sex) AS sex, COUNT(*)::int AS count FROM pets WHERE vet = ? AND TRIM(COALESCE(sex,'')) != '' GROUP BY TRIM(sex)`, zip).Scan(&sexDist)

	var totalOwners int
	h.db.Raw(`SELECT COUNT(DISTINCT COALESCE(NULLIF(code,''), uuid::text))::int FROM pets WHERE vet = ?`, zip).Scan(&totalOwners)

	var totalRecords int
	h.db.Raw(`SELECT COUNT(*)::int FROM vaccination WHERE sk = ?`, zip).Scan(&totalRecords)

	var procs []ProcedureCount
	h.db.Raw(`SELECT TRIM(tp::text) AS type, TRIM(tpname) AS name, COUNT(*)::int AS count FROM vaccination WHERE sk = ? GROUP BY TRIM(tp::text), TRIM(tpname) ORDER BY count DESC LIMIT 10`, zip).Scan(&procs)

	var vaccines []VaccineCount
	h.db.Raw(`SELECT TRIM(vac_name) AS name, COUNT(*)::int AS count FROM (SELECT unnest(ARRAY[vac,vac1,vac2,vac3,vac4,vac5,vac6,vac7,vac8,vac9]) AS vac_name FROM vaccination WHERE sk = ?) sub WHERE TRIM(COALESCE(vac_name,'')) != '' GROUP BY TRIM(vac_name) ORDER BY count DESC LIMIT 10`, zip).Scan(&vaccines)

	var trends []MonthlyTrendItem
	h.db.Raw(`SELECT LEFT(date, 7) AS month, COUNT(*)::int AS records FROM vaccination WHERE sk = ? AND LENGTH(date) >= 7 GROUP BY LEFT(date, 7) ORDER BY month DESC LIMIT 12`, zip).Scan(&trends)

	// Ensure non-nil slices for JSON
	if filtered == nil {
		filtered = []SpeciesCount{}
	}
	if breeds == nil {
		breeds = []BreedCount{}
	}
	if sexDist == nil {
		sexDist = []SexCount{}
	}
	if procs == nil {
		procs = []ProcedureCount{}
	}
	if vaccines == nil {
		vaccines = []VaccineCount{}
	}
	if trends == nil {
		trends = []MonthlyTrendItem{}
	}

	writeJSON(w, http.StatusOK, ClinicStats{
		TotalPets:        totalPets,
		TotalOwners:      totalOwners,
		TotalRecords:     totalRecords,
		SpeciesBreakdown: filtered,
		TopBreeds:        breeds,
		SexDistribution:  sexDist,
		TopProcedures:    procs,
		TopVaccines:      vaccines,
		MonthlyTrends:    trends,
	})
}

// --- Admin Stats ---

// AdminStats is the response for system-wide admin statistics.
type AdminStats struct {
	TotalOwners    int              `json:"total_owners"`
	TotalVets      int              `json:"total_vets"`
	TotalPets      int              `json:"total_pets"`
	TotalDogs      int              `json:"total_dogs"`
	TotalCats      int              `json:"total_cats"`
	TotalOther     int              `json:"total_other"`
	ActiveAccounts int              `json:"active_accounts"`
	PetsPerClinic  []ClinicPetCount `json:"pets_per_clinic"`
	PaymentTiers   []PaymentTier    `json:"payment_tiers"`
}

// ClinicPetCount is the pet count per clinic.
type ClinicPetCount struct {
	Clinic string `json:"clinic"`
	Count  int    `json:"count"`
}

// PaymentTier is the count of payments at a given price.
type PaymentTier struct {
	Price string `json:"price"`
	Count int    `json:"count"`
}

// Admin returns system-wide statistics (super admin only).
// @Summary Get admin statistics
// @Tags stats
// @Produce json
// @Security BearerAuth
// @Success 200 {object} AdminStats
// @Failure 500 {object} ErrorResponse
// @Router /stats/admin [get]
func (h *StatsHandler) Admin(w http.ResponseWriter, r *http.Request) {
	var stats AdminStats

	h.db.Raw(`SELECT COUNT(*)::int FROM memberlogin_members WHERE group_id = ?`, models.RoleOwner).Scan(&stats.TotalOwners)
	h.db.Raw(`SELECT COUNT(*)::int FROM memberlogin_members WHERE group_id = ?`, models.RoleVet).Scan(&stats.TotalVets)
	h.db.Raw(`SELECT COUNT(*)::int FROM pets`).Scan(&stats.TotalPets)
	h.db.Raw(`SELECT COUNT(*)::int FROM pets WHERE TRIM(pet) = 'ძაღლი'`).Scan(&stats.TotalDogs)
	h.db.Raw(`SELECT COUNT(*)::int FROM pets WHERE TRIM(pet) = 'კატა'`).Scan(&stats.TotalCats)
	h.db.Raw(`SELECT COUNT(*)::int FROM pets WHERE TRIM(pet) NOT IN ('ძაღლი', 'კატა') AND TRIM(COALESCE(pet,'')) != ''`).Scan(&stats.TotalOther)
	h.db.Raw(`SELECT COUNT(*)::int FROM pets WHERE birth2 >= CURRENT_DATE AND status = 1`).Scan(&stats.ActiveAccounts)

	h.db.Raw(`SELECT vet AS clinic, COUNT(*)::int AS count FROM pets WHERE TRIM(COALESCE(vet,'')) != '' GROUP BY vet ORDER BY count DESC`).Scan(&stats.PetsPerClinic)
	h.db.Raw(`SELECT amount AS price, COUNT(*)::int AS count FROM payments_ipay WHERE status = 'success' GROUP BY amount ORDER BY count DESC`).Scan(&stats.PaymentTiers)

	if stats.PetsPerClinic == nil {
		stats.PetsPerClinic = []ClinicPetCount{}
	}
	if stats.PaymentTiers == nil {
		stats.PaymentTiers = []PaymentTier{}
	}

	writeJSON(w, http.StatusOK, stats)
}
