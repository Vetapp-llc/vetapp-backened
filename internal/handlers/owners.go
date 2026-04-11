package handlers

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// OwnerHandler handles owner-related endpoints.
type OwnerHandler struct {
	db *gorm.DB
}

// NewOwnerHandler creates a new OwnerHandler.
func NewOwnerHandler(db *gorm.DB) *OwnerHandler {
	return &OwnerHandler{db: db}
}

// --- Response types ---

type OwnerItem struct {
	PersonalID string `json:"personalId" validate:"required"`
	Name       string `json:"name" validate:"required"`
	Phone      string `json:"phone" validate:"required"`
	Email      string `json:"email" validate:"required"`
	PetCount   int    `json:"petCount" validate:"required"`
}

type OwnerWithPets struct {
	OwnerItem
	Pets []PetListItem `json:"pets" validate:"required"`
}

// List returns owners grouped from the pets table.
// @Summary List owners
// @Tags owners
// @Produce json
// @Security BearerAuth
// @Param search query string false "Search by name, phone, email"
// @Param page query int false "Page number" default(1)
// @Param pageSize query int false "Page size" default(20)
// @Success 200 {object} PaginatedResponse
// @Failure 500 {object} ErrorResponse
// @Router /owners [get]
func (h *OwnerHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	search := r.URL.Query().Get("search")
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 50 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	// Build search condition
	searchCond := ""
	args := []interface{}{claims.Zip}
	if search != "" {
		// Strip leading zeros from search term for uuid numeric comparison
		// (uuid column is bigint — leading zeros are dropped on storage)
		trimmed := strings.TrimLeft(search, "0")
		if trimmed == "" {
			trimmed = "0"
		}
		like := "%" + search + "%"
		trimmedLike := "%" + trimmed + "%"
		searchCond = "AND (first_name ILIKE ? OR phone ILIKE ? OR email ILIKE ? OR uuid::text ILIKE ? OR uuid::text ILIKE ? OR code ILIKE ?)"
		args = append(args, like, like, like, like, trimmedLike, like)
	}

	// Count distinct owners (group by uuid — the owner's actual personal ID)
	var total int64
	h.db.Raw(
		`SELECT COUNT(DISTINCT uuid) FROM pets WHERE vet = ? `+searchCond,
		args...,
	).Scan(&total)

	// Fetch grouped owners
	type ownerRow struct {
		PersonalID string `gorm:"column:personal_id"`
		Name       string
		Phone      string
		Email      string
		PetCount   int `gorm:"column:pet_count"`
	}
	var rows []ownerRow
	h.db.Raw(
		`SELECT
			uuid::text AS personal_id,
			MAX(first_name) AS name,
			MAX(phone) AS phone,
			MAX(email) AS email,
			COUNT(*)::int AS pet_count
		FROM pets
		WHERE vet = ? `+searchCond+`
		GROUP BY uuid
		ORDER BY MAX(first_name) ASC
		LIMIT ? OFFSET ?`,
		append(args, pageSize, offset)...,
	).Scan(&rows)

	owners := make([]OwnerItem, len(rows))
	for i, r := range rows {
		owners[i] = OwnerItem{
			PersonalID: r.PersonalID,
			Name:       r.Name,
			Phone:      r.Phone,
			Email:      r.Email,
			PetCount:   r.PetCount,
		}
	}

	writeJSON(w, http.StatusOK, PaginatedResponse{
		Data:       owners,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: int(math.Ceil(float64(total) / float64(pageSize))),
	})
}

// Get returns an owner's details and their pets.
// @Summary Get owner by personal ID
// @Tags owners
// @Produce json
// @Security BearerAuth
// @Param personalId path string true "Owner personal ID"
// @Success 200 {object} OwnerWithPets
// @Failure 404 {object} ErrorResponse
// @Router /owners/{personalId} [get]
func (h *OwnerHandler) Get(w http.ResponseWriter, r *http.Request) {
	personalID := chi.URLParam(r, "personalId")
	claims := middleware.GetClaims(r)

	// Strip leading zeros for uuid numeric comparison (bigint strips them on storage)
	trimmedID := strings.TrimLeft(personalID, "0")
	if trimmedID == "" {
		trimmedID = "0"
	}

	// Look up by uuid (the owner's actual personal ID) — scoped to current clinic
	var pets []models.Pet
	if err := h.db.Where(
		"vet = ? AND (uuid::text = ? OR uuid::text = ?)",
		claims.Zip, personalID, trimmedID,
	).Order("id DESC").Find(&pets).Error; err != nil || len(pets) == 0 {
		log := middleware.RequestLogger(r)
		log.Warn("owner_not_found", "personal_id", personalID)
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "owner not found"})
		return
	}

	petItems := make([]PetListItem, len(pets))
	for i, p := range pets {
		petItems[i] = petToListItem(p)
	}

	// Derive owner info using MAX (consistent with List handler's aggregation)
	bestName, bestPhone, bestEmail := "", "", ""
	for _, p := range pets {
		if p.FirstName > bestName {
			bestName = p.FirstName
		}
		if p.Phone > bestPhone {
			bestPhone = p.Phone
		}
		if p.Email > bestEmail {
			bestEmail = p.Email
		}
	}

	owner := OwnerWithPets{
		OwnerItem: OwnerItem{
			PersonalID: personalID,
			Name:       bestName,
			Phone:      bestPhone,
			Email:      bestEmail,
			PetCount:   len(pets),
		},
		Pets: petItems,
	}

	writeJSON(w, http.StatusOK, owner)
}
