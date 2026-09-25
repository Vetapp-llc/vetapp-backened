package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// AdminHandler serves the super-admin portal's account pages
// (superadmin/owners.php, dep.php, delateowner.php, delateadmin.php,
// trans.php) and the clinic's promo-code page (vet/promo.php).
type AdminHandler struct {
	db *gorm.DB
}

// NewAdminHandler creates an AdminHandler.
func NewAdminHandler(db *gorm.DB) *AdminHandler {
	return &AdminHandler{db: db}
}

// MemberItem is one account in an admin or promo listing.
type MemberItem struct {
	ID          uint    `json:"id" validate:"required"`
	GroupID     int     `json:"group_id" validate:"required"`
	FirstName   string  `json:"first_name" validate:"required"`
	PersonalID  string  `json:"personal_id" validate:"required"`
	Email       string  `json:"email" validate:"required"`
	Phone       string  `json:"phone" validate:"required"`
	Zip         string  `json:"zip" validate:"required"`
	CompanyName string  `json:"company_name" validate:"required"`
	Status      string  `json:"status" validate:"required"`
	Created     *string `json:"created"`
	LastLogin   *string `json:"last_login"`
	PetCount    int64   `json:"pet_count" validate:"required"`
}

type memberRow struct {
	ID          uint
	GroupID     int
	FirstName   string
	LastName    string
	Email       string
	Phone       string
	Zip         string
	CompanyName string
	Status      string
	Created     *time.Time
	LastLogin   *time.Time
	PetCount    int64
}

func (m memberRow) item() MemberItem {
	it := MemberItem{
		ID: m.ID, GroupID: m.GroupID, FirstName: m.FirstName, PersonalID: m.LastName,
		Email: m.Email, Phone: m.Phone, Zip: m.Zip, CompanyName: m.CompanyName,
		Status: m.Status, PetCount: m.PetCount,
	}
	if m.Created != nil {
		s := m.Created.Format("2006-01-02")
		it.Created = &s
	}
	if m.LastLogin != nil {
		s := m.LastLogin.Format("2006-01-02 15:04")
		it.LastLogin = &s
	}
	return it
}

// listMembers pages through accounts matching base, with an optional
// search over name, personal ID, email and phone. Owners also get their
// pet count (one grouped subquery for the page, not one per row).
func (h *AdminHandler) listMembers(w http.ResponseWriter, r *http.Request, base *gorm.DB) {
	if search := strings.TrimSpace(r.URL.Query().Get("search")); search != "" {
		like := "%" + search + "%"
		base = base.Where("(first_name ILIKE ? OR last_name ILIKE ? OR email ILIKE ? OR phone ILIKE ?)", like, like, like, like)
	}
	page := ParsePageParams(r)

	var total int64
	base.Session(&gorm.Session{}).Count(&total)

	var rows []memberRow
	err := page.Paginate(base.Session(&gorm.Session{})).
		Select(`m.id, m.group_id, m.first_name, m.last_name, m.email, m.phone, m.zip, m.company_name,
		        m.status, m.created, m.last_login,
		        (SELECT COUNT(*) FROM pets p WHERE p.uuid = m.last_name AND m.group_id = 1 AND m.last_name <> '') AS pet_count`).
		Order("m.id DESC").
		Scan(&rows).Error
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch accounts"})
		return
	}

	items := make([]MemberItem, len(rows))
	for i, m := range rows {
		items[i] = m.item()
	}
	writeJSON(w, http.StatusOK, NewPaginatedResponse(items, total, page))
}

// Members lists accounts of one role for the super admin.
// @Summary List accounts (admin)
// @Tags admin
// @Produce json
// @Security BearerAuth
// @Param group query int false "1 owner (default), 2 vet, 3 department, 4 admin"
// @Param search query string false "Name, personal ID, email or phone"
// @Param page query int false "Page"
// @Param pageSize query int false "Page size"
// @Success 200 {object} PaginatedResponse
// @Router /admin/members [get]
func (h *AdminHandler) Members(w http.ResponseWriter, r *http.Request) {
	group, _ := strconv.Atoi(r.URL.Query().Get("group"))
	if group == 0 {
		group = models.RoleOwner
	}
	base := h.db.Table("memberlogin_members m").Where("m.group_id = ?", group)
	if r.URL.Query().Get("include_removed") != "1" {
		base = base.Where("m.status = ?", "T")
	}
	h.listMembers(w, r, base)
}

// DisableMember removes an account the way the clinic portal removes a
// vet: the row is kept (records keep pointing at it), the address is
// freed by prefixing it, and status F blocks sign-in. The PHP admin
// pages hard-deleted instead, which cannot be undone.
// @Summary Disable an account (admin)
// @Tags admin
// @Produce json
// @Security BearerAuth
// @Param id path int true "Member ID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /admin/members/{id} [delete]
func (h *AdminHandler) DisableMember(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	var u models.User
	if err := h.db.Where("id = ? AND status = ?", chi.URLParam(r, "id"), "T").First(&u).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "account not found"})
		return
	}
	if u.ID == claims.UserID {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "you cannot disable your own account"})
		return
	}
	updates := map[string]interface{}{"status": "F"}
	if u.Email != "" && !strings.HasPrefix(u.Email, removedEmailPrefix) {
		updates["email"] = removedEmailPrefix + u.Email
	}
	if err := h.db.Model(&u).Updates(updates).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to disable account"})
		return
	}
	middleware.InvalidateAccount(u.ID)
	middleware.RequestLogger(r).Info("member_disabled", "member_id", u.ID, "group_id", u.GroupID)
	writeJSON(w, http.StatusOK, MessageResponse{Message: "account disabled"})
}

// TransactionItem is one row of the iPay / BOG / Apple payment log.
type TransactionItem struct {
	ID        uint    `json:"id" validate:"required"`
	PetID     int     `json:"pet_id" validate:"required"`
	PetName   string  `json:"pet_name" validate:"required"`
	Price     string  `json:"price" validate:"required"`
	Currency  string  `json:"currency" validate:"required"`
	Status    string  `json:"status" validate:"required"`
	Provider  string  `json:"provider" validate:"required"`
	OrderID   string  `json:"order_id" validate:"required"`
	CreatedAt *string `json:"created_at"`
}

// Transactions lists subscription payments (superadmin/trans.php).
// @Summary Subscription payment log (admin)
// @Tags admin
// @Produce json
// @Security BearerAuth
// @Param status query string false "Filter by status"
// @Param page query int false "Page"
// @Param pageSize query int false "Page size"
// @Success 200 {object} PaginatedResponse
// @Router /admin/transactions [get]
func (h *AdminHandler) Transactions(w http.ResponseWriter, r *http.Request) {
	q := h.db.Table("payments_ipay t")
	if st := r.URL.Query().Get("status"); st != "" {
		q = q.Where("t.status = ?", st)
	}
	page := ParsePageParams(r)
	var total int64
	q.Session(&gorm.Session{}).Count(&total)

	var rows []struct {
		ID        uint
		PetID     int
		PetName   string
		Price     string
		Currency  string
		Status    string
		Provider  string
		OrderID   string
		CreatedAt *time.Time
	}
	err := page.Paginate(q.Session(&gorm.Session{})).
		Select(`t.id, COALESCE(t.pet_id, 0) AS pet_id, COALESCE(p.name, '') AS pet_name,
		        COALESCE(t.price::text, '') AS price, COALESCE(t.currency, '') AS currency,
		        COALESCE(t.status, '') AS status, COALESCE(t.provider, '') AS provider,
		        COALESCE(NULLIF(t.transaction_order_id, ''), t.order_id, '') AS order_id, t.created_at`).
		Joins("LEFT JOIN pets p ON p.id = t.pet_id").
		Order("t.id DESC").
		Scan(&rows).Error
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch transactions"})
		return
	}
	items := make([]TransactionItem, len(rows))
	for i, t := range rows {
		items[i] = TransactionItem{ID: t.ID, PetID: t.PetID, PetName: t.PetName, Price: t.Price,
			Currency: t.Currency, Status: t.Status, Provider: t.Provider, OrderID: t.OrderID}
		if t.CreatedAt != nil {
			s := t.CreatedAt.Format("2006-01-02 15:04")
			items[i].CreatedAt = &s
		}
	}
	writeJSON(w, http.StatusOK, NewPaginatedResponse(items, total, page))
}

// Promo lists the pet owners who signed up with a promo code of one of the
// caller's clinic's vets (vet/promo.php). The PHP page also let a clinic
// delete those owner accounts; that is deliberately not carried over —
// an owner's account is not the clinic's to delete.
// @Summary Owners who registered with this clinic's promo code
// @Tags clinic
// @Produce json
// @Security BearerAuth
// @Param search query string false "Name, personal ID, email or phone"
// @Success 200 {object} PaginatedResponse
// @Router /promo [get]
func (h *AdminHandler) Promo(w http.ResponseWriter, r *http.Request) {
	clinic, ok := resolveClinic(w, r)
	if !ok {
		return
	}
	if clinic == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "this account has no clinic"})
		return
	}
	// A promo code belongs to a vet: vet/promo.php reads the signed-in
	// vet's own `website` value (e.g. "vetapp777") and lists the owners
	// who registered with it. This lists sign-ups for every active vet of
	// the clinic, which includes the caller's own.
	base := h.db.Table("memberlogin_members m").
		Where(`m.group_id = ? AND m.status = ? AND m.website IN (
			SELECT website FROM memberlogin_members
			WHERE zip = ? AND group_id = ? AND status = ? AND COALESCE(website, '') <> '')`,
			models.RoleOwner, "T", clinic, models.RoleVet, "T")
	h.listMembers(w, r, base)
}
