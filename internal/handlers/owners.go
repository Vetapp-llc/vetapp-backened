package handlers

import (
	"math"
	"net/http"
	"strconv"

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
	PersonalID string `json:"personalId"`
	Name       string `json:"name"`
	Phone      string `json:"phone"`
	Email      string `json:"email"`
	PetCount   int    `json:"petCount"`
}

type OwnerWithPets struct {
	OwnerItem
	Pets []PetListItem `json:"pets"`
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
		searchCond = "AND (first_name ILIKE ? OR phone ILIKE ? OR email ILIKE ? OR code ILIKE ?)"
		like := "%" + search + "%"
		args = append(args, like, like, like, like)
	}

	// Count distinct owners
	var total int64
	h.db.Raw(
		`SELECT COUNT(DISTINCT COALESCE(NULLIF(code,''), uuid::text)) FROM pets WHERE vet = ? `+searchCond,
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
			COALESCE(NULLIF(code,''), uuid::text) AS personal_id,
			MAX(first_name) AS name,
			MAX(phone) AS phone,
			MAX(email) AS email,
			COUNT(*)::int AS pet_count
		FROM pets
		WHERE vet = ? `+searchCond+`
		GROUP BY COALESCE(NULLIF(code,''), uuid::text)
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

	var pets []models.Pet
	if err := h.db.Where("vet = ? AND (code = ? OR uuid::text = ?)", claims.Zip, personalID, personalID).
		Order("id DESC").Find(&pets).Error; err != nil || len(pets) == 0 {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "owner not found"})
		return
	}

	first := pets[0]
	petItems := make([]PetListItem, len(pets))
	for i, p := range pets {
		petItems[i] = petToListItem(p)
	}

	owner := OwnerWithPets{
		OwnerItem: OwnerItem{
			PersonalID: func() string {
				if first.Code != "" {
					return first.Code
				}
				return personalID
			}(),
			Name:     first.FirstName,
			Phone:    first.Phone,
			Email:    first.Email,
			PetCount: len(pets),
		},
		Pets: petItems,
	}

	writeJSON(w, http.StatusOK, owner)
}
