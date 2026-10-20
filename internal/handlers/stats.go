package handlers

import (
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"vetapp-backend/internal/models"

	"golang.org/x/sync/errgroup"
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
	TotalPets        int                `json:"totalPets" validate:"required"`
	TotalOwners      int                `json:"totalOwners" validate:"required"`
	TotalRecords     int                `json:"totalRecords" validate:"required"`
	SpeciesBreakdown []SpeciesCount     `json:"speciesBreakdown" validate:"required"`
	TopBreeds        []BreedCount       `json:"topBreeds" validate:"required"`
	SexDistribution  []SexCount         `json:"sexDistribution" validate:"required"`
	TopProcedures    []ProcedureCount   `json:"topProcedures" validate:"required"`
	TopVaccines      []VaccineCount     `json:"topVaccines" validate:"required"`
	MonthlyTrends    []MonthlyTrendItem `json:"monthlyTrends" validate:"required"`
}

type SpeciesCount struct {
	Species string `json:"species" validate:"required"`
	Count   int    `json:"count" validate:"required"`
}
type BreedCount struct {
	Breed string `json:"breed" validate:"required"`
	Count int    `json:"count" validate:"required"`
}
type SexCount struct {
	Sex   string `json:"sex" validate:"required"`
	Count int    `json:"count" validate:"required"`
}
type ProcedureCount struct {
	Type  string `json:"type" validate:"required"`
	Name  string `json:"name" validate:"required"`
	Count int    `json:"count" validate:"required"`
}
type VaccineCount struct {
	Name  string `json:"name" validate:"required"`
	Count int    `json:"count" validate:"required"`
}
type MonthlyTrendItem struct {
	Month   string `json:"month" validate:"required"`
	Records int    `json:"records" validate:"required"`
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
	zip, ok := resolveClinic(w, r)
	if !ok {
		return
	}

	// Run the 7 independent aggregations concurrently. Each query
	// hits a different cache locality and the wall-time was previously
	// the sum, not the max — typically 3–5× faster on a busy clinic.
	var (
		species  []SpeciesCount
		breeds   []BreedCount
		sexDist  []SexCount
		procs    []ProcedureCount
		vaccines []VaccineCount
		trends   []MonthlyTrendItem
		totalOwners,
		totalRecords int
	)

	g := new(errgroup.Group)
	g.Go(func() error {
		return h.db.Raw(`SELECT TRIM(pet) AS species, COUNT(*)::int AS count FROM pets WHERE vet = ? GROUP BY TRIM(pet)`, zip).Scan(&species).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT TRIM(variety) AS breed, COUNT(*)::int AS count FROM pets WHERE vet = ? AND TRIM(COALESCE(variety,'')) != '' GROUP BY TRIM(variety) ORDER BY count DESC LIMIT 10`, zip).Scan(&breeds).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT TRIM(sex) AS sex, COUNT(*)::int AS count FROM pets WHERE vet = ? AND TRIM(COALESCE(sex,'')) != '' GROUP BY TRIM(sex)`, zip).Scan(&sexDist).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT COUNT(DISTINCT COALESCE(NULLIF(code,''), uuid::text))::int FROM pets WHERE vet = ?`, zip).Scan(&totalOwners).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT COUNT(*)::int FROM vaccination WHERE sk = ?`, zip).Scan(&totalRecords).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT TRIM(tp::text) AS type, TRIM(tpname) AS name, COUNT(*)::int AS count FROM vaccination WHERE sk = ? GROUP BY TRIM(tp::text), TRIM(tpname) ORDER BY count DESC LIMIT 10`, zip).Scan(&procs).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT TRIM(vac_name) AS name, COUNT(*)::int AS count FROM (SELECT unnest(ARRAY[vac,vac1,vac2,vac3,vac4,vac5,vac6,vac7,vac8,vac9]) AS vac_name FROM vaccination WHERE sk = ?) sub WHERE TRIM(COALESCE(vac_name,'')) != '' GROUP BY TRIM(vac_name) ORDER BY count DESC LIMIT 10`, zip).Scan(&vaccines).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT LEFT(date, 7) AS month, COUNT(*)::int AS records FROM vaccination WHERE sk = ? AND LENGTH(date) >= 7 GROUP BY LEFT(date, 7) ORDER BY month DESC LIMIT 12`, zip).Scan(&trends).Error
	})
	// We tolerate partial failures: the dashboard renders gracefully
	// with zeroes / empty arrays. Errors are noisy in logs already via
	// the GORM logger.
	_ = g.Wait()

	filtered := make([]SpeciesCount, 0)
	totalPets := 0
	for _, s := range species {
		if s.Species != "" {
			filtered = append(filtered, s)
			totalPets += s.Count
		}
	}

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

// --- Daily Clinic Stats ---

// ProcedureTypeRevenue is the revenue per procedure type for a given day.
type ProcedureTypeRevenue struct {
	TP     int    `json:"tp"`
	TPName string `json:"tpname"`
	Count  int    `json:"count"`
	Total  string `json:"total"`
}

// DailyClinicStats is the daily revenue/procedure breakdown for a clinic.
type DailyClinicStats struct {
	Date       string                 `json:"date"`
	Card       string                 `json:"card"`
	Cash       string                 `json:"cash"`
	Total      string                 `json:"total"`
	Procedures []ProcedureTypeRevenue `json:"procedures"`
}

// DailyClinic returns daily procedure revenue and cash/card breakdown.
// @Summary Get daily clinic statistics
// @Tags stats
// @Produce json
// @Security BearerAuth
// @Param date query string true "Date (YYYY-MM-DD)"
// @Param clinic query string false "Clinic code (admin only)"
// @Success 200 {object} DailyClinicStats
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /stats/clinic/daily [get]
func (h *StatsHandler) DailyClinic(w http.ResponseWriter, r *http.Request) {
	zip, ok := resolveClinic(w, r)
	if !ok {
		return
	}

	date := r.URL.Query().Get("date")
	if !isISODate(date) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date is required (YYYY-MM-DD)"})
		return
	}

	var result DailyClinicStats
	result.Date = date

	var procs []ProcedureTypeRevenue
	var payments struct {
		Card  string
		Cash  string
		Total string
	}

	// Run both queries concurrently
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		h.db.Raw(
			`SELECT tp, TRIM(tpname) AS tp_name, COUNT(*)::int AS count,
			        COALESCE(SUM(CASE WHEN price ~ '^[0-9]+(\.[0-9]+){0,1}$' THEN price::numeric END)::text, '0') AS total
			 FROM vaccination WHERE sk = ? AND date = ?
			 GROUP BY tp, TRIM(tpname) ORDER BY count DESC`, zip, date,
		).Scan(&procs)
	}()

	go func() {
		defer wg.Done()
		// paymethod's real columns are (zip, date, uuid, sum, pay); the
		// method labels are Georgian. Using method/amount/sk here made
		// the clinic's daily card/cash split silently report zero, which
		// then fell through to the procedure-price estimate below.
		// `sum` is TEXT and legacy rows contain non-numeric junk, so
		// guard the cast with a numeric-shape test.
		// `{0,1}` not `?` — GORM treats a literal '?' in the SQL as a
		// bind placeholder and the query would fail with
		// "unused argument".
		const numericSum = `sum ~ '^[0-9]+(\.[0-9]+){0,1}$'`
		h.db.Raw(
			`SELECT COALESCE(SUM(CASE WHEN pay = ? AND `+numericSum+` THEN sum::numeric ELSE 0 END)::text, '0') AS card,
			        COALESCE(SUM(CASE WHEN pay = ? AND `+numericSum+` THEN sum::numeric ELSE 0 END)::text, '0') AS cash,
			        COALESCE(SUM(CASE WHEN `+numericSum+` THEN sum::numeric ELSE 0 END)::text, '0') AS total
			 FROM paymethod WHERE zip = ? AND date = ?`,
			models.PayMethodCard, models.PayMethodCash, zip, date,
		).Scan(&payments)
	}()

	wg.Wait()

	if procs == nil {
		procs = []ProcedureTypeRevenue{}
	}
	result.Procedures = procs
	result.Card = payments.Card
	result.Cash = payments.Cash
	result.Total = payments.Total

	// Default to "0" if empty (e.g. no paymethod rows)
	if result.Card == "" {
		result.Card = "0"
	}
	if result.Cash == "" {
		result.Cash = "0"
	}
	// If paymethod total is empty/0, compute from procedure prices
	if result.Total == "" || result.Total == "0" {
		var procTotal string
		h.db.Raw(`SELECT COALESCE(SUM(CASE WHEN price ~ '^[0-9]+(\.[0-9]+){0,1}$' THEN price::numeric END)::text, '0') FROM vaccination WHERE sk = ? AND date = ?`, zip, date).Scan(&procTotal)
		if procTotal != "" {
			result.Total = procTotal
		} else {
			result.Total = "0"
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// --- Monthly Clinic Stats ---

// MonthlyClinicStats is the monthly revenue/procedure breakdown for a clinic.
type MonthlyClinicStats struct {
	Month          string                 `json:"month"`
	Total          string                 `json:"total"`
	Procedures     []ProcedureTypeRevenue `json:"procedures"`
	DailyBreakdown []DailyTotal           `json:"dailyBreakdown"`
}

// DailyTotal is a single day's total revenue.
type DailyTotal struct {
	Date  string `json:"date"`
	Total string `json:"total"`
	Count int    `json:"count"`
}

// MonthlyClinic returns monthly procedure revenue breakdown.
// @Summary Get monthly clinic statistics
// @Tags stats
// @Produce json
// @Security BearerAuth
// @Param month query string true "Month (YYYY-MM)"
// @Param clinic query string false "Clinic code (admin only)"
// @Success 200 {object} MonthlyClinicStats
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /stats/clinic/monthly [get]
func (h *StatsHandler) MonthlyClinic(w http.ResponseWriter, r *http.Request) {
	zip, ok := resolveClinic(w, r)
	if !ok {
		return
	}

	month := r.URL.Query().Get("month")
	if _, err := time.Parse("2006-01", month); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "month is required (YYYY-MM)"})
		return
	}

	var result MonthlyClinicStats
	result.Month = month

	var procs []ProcedureTypeRevenue
	var daily []DailyTotal

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		h.db.Raw(
			`SELECT tp, TRIM(tpname) AS tp_name, COUNT(*)::int AS count,
			        COALESCE(SUM(CASE WHEN price ~ '^[0-9]+(\.[0-9]+){0,1}$' THEN price::numeric END)::text, '0') AS total
			 FROM vaccination WHERE sk = ? AND date BETWEEN ? || '-01' AND ? || '-31'
			 GROUP BY tp, TRIM(tpname) ORDER BY count DESC`, zip, month, month,
		).Scan(&procs)
	}()

	go func() {
		defer wg.Done()
		h.db.Raw(
			`SELECT date, COALESCE(SUM(CASE WHEN price ~ '^[0-9]+(\.[0-9]+){0,1}$' THEN price::numeric END)::text, '0') AS total, COUNT(*)::int AS count
			 FROM vaccination WHERE sk = ? AND date BETWEEN ? || '-01' AND ? || '-31'
			 GROUP BY date ORDER BY date DESC`, zip, month, month,
		).Scan(&daily)
	}()

	wg.Wait()

	if procs == nil {
		procs = []ProcedureTypeRevenue{}
	}
	if daily == nil {
		daily = []DailyTotal{}
	}

	// Compute total from procedures
	var totalStr string
	h.db.Raw(`SELECT COALESCE(SUM(CASE WHEN price ~ '^[0-9]+(\.[0-9]+){0,1}$' THEN price::numeric END)::text, '0') FROM vaccination WHERE sk = ? AND date BETWEEN ? || '-01' AND ? || '-31'`, zip, month, month).Scan(&totalStr)
	if totalStr == "" {
		totalStr = "0"
	}

	result.Total = totalStr
	result.Procedures = procs
	result.DailyBreakdown = daily

	writeJSON(w, http.StatusOK, result)
}

// --- Yearly Clinic Stats ---

// YearlyClinicStats is the yearly revenue/procedure breakdown for a clinic.
type YearlyClinicStats struct {
	Year             string                 `json:"year"`
	Total            string                 `json:"total"`
	Procedures       []ProcedureTypeRevenue `json:"procedures"`
	MonthlyBreakdown []MonthlyTotal         `json:"monthlyBreakdown"`
}

// MonthlyTotal is a single month's total revenue.
type MonthlyTotal struct {
	Month string `json:"month"`
	Total string `json:"total"`
	Count int    `json:"count"`
}

// YearlyClinic returns yearly procedure revenue breakdown.
// @Summary Get yearly clinic statistics
// @Tags stats
// @Produce json
// @Security BearerAuth
// @Param year query string true "Year (YYYY)"
// @Param clinic query string false "Clinic code (admin only)"
// @Success 200 {object} YearlyClinicStats
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /stats/clinic/yearly [get]
func (h *StatsHandler) YearlyClinic(w http.ResponseWriter, r *http.Request) {
	zip, ok := resolveClinic(w, r)
	if !ok {
		return
	}

	year := r.URL.Query().Get("year")
	if _, err := time.Parse("2006", year); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "year is required (YYYY)"})
		return
	}

	var result YearlyClinicStats
	result.Year = year

	var procs []ProcedureTypeRevenue
	var monthly []MonthlyTotal

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		h.db.Raw(
			`SELECT tp, TRIM(tpname) AS tp_name, COUNT(*)::int AS count,
			        COALESCE(SUM(CASE WHEN price ~ '^[0-9]+(\.[0-9]+){0,1}$' THEN price::numeric END)::text, '0') AS total
			 FROM vaccination WHERE sk = ? AND date BETWEEN ? || '-01-01' AND ? || '-12-31'
			 GROUP BY tp, TRIM(tpname) ORDER BY count DESC`, zip, year, year,
		).Scan(&procs)
	}()

	go func() {
		defer wg.Done()
		h.db.Raw(
			`SELECT LEFT(date, 7) AS month, COALESCE(SUM(CASE WHEN price ~ '^[0-9]+(\.[0-9]+){0,1}$' THEN price::numeric END)::text, '0') AS total, COUNT(*)::int AS count
			 FROM vaccination WHERE sk = ? AND date BETWEEN ? || '-01-01' AND ? || '-12-31'
			 GROUP BY LEFT(date, 7) ORDER BY month DESC`, zip, year, year,
		).Scan(&monthly)
	}()

	wg.Wait()

	if procs == nil {
		procs = []ProcedureTypeRevenue{}
	}
	if monthly == nil {
		monthly = []MonthlyTotal{}
	}

	var totalStr string
	h.db.Raw(`SELECT COALESCE(SUM(CASE WHEN price ~ '^[0-9]+(\.[0-9]+){0,1}$' THEN price::numeric END)::text, '0') FROM vaccination WHERE sk = ? AND date BETWEEN ? || '-01-01' AND ? || '-12-31'`, zip, year, year).Scan(&totalStr)
	if totalStr == "" {
		totalStr = "0"
	}

	result.Total = totalStr
	result.Procedures = procs
	result.MonthlyBreakdown = monthly

	writeJSON(w, http.StatusOK, result)
}

// --- Admin Stats ---

// AdminStats is the response for system-wide admin statistics.
type AdminStats struct {
	TotalOwners    int              `json:"total_owners" validate:"required"`
	TotalVets      int              `json:"total_vets" validate:"required"`
	TotalPets      int              `json:"total_pets" validate:"required"`
	TotalDogs      int              `json:"total_dogs" validate:"required"`
	TotalCats      int              `json:"total_cats" validate:"required"`
	TotalOther     int              `json:"total_other" validate:"required"`
	ActiveAccounts int              `json:"active_accounts" validate:"required"`
	PetsPerClinic  []ClinicPetCount `json:"pets_per_clinic" validate:"required"`
	PaymentTiers   []PaymentTier    `json:"payment_tiers" validate:"required"`
}

// ClinicPetCount is the pet count per clinic.
type ClinicPetCount struct {
	Clinic      string `json:"clinic" validate:"required"`
	CompanyName string `json:"company_name"`
	Count       int    `json:"count" validate:"required"`
}

// PaymentTier is the count of payments at a given price.
type PaymentTier struct {
	Price string `json:"price" validate:"required"`
	Count int    `json:"count" validate:"required"`
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

	// 9 admin-wide aggregations are independent — fire them in parallel.
	g := new(errgroup.Group)
	g.Go(func() error {
		return h.db.Raw(`SELECT COUNT(*)::int FROM memberlogin_members WHERE group_id = ?`, models.RoleOwner).Scan(&stats.TotalOwners).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT COUNT(*)::int FROM memberlogin_members WHERE group_id = ?`, models.RoleVet).Scan(&stats.TotalVets).Error
	})
	g.Go(func() error { return h.db.Raw(`SELECT COUNT(*)::int FROM pets`).Scan(&stats.TotalPets).Error })
	g.Go(func() error {
		return h.db.Raw(`SELECT COUNT(*)::int FROM pets WHERE TRIM(pet) = 'ძაღლი'`).Scan(&stats.TotalDogs).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT COUNT(*)::int FROM pets WHERE TRIM(pet) = 'კატა'`).Scan(&stats.TotalCats).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT COUNT(*)::int FROM pets WHERE TRIM(pet) NOT IN ('ძაღლი', 'კატა') AND TRIM(COALESCE(pet,'')) != ''`).Scan(&stats.TotalOther).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT COUNT(*)::int FROM pets WHERE birth2 >= CURRENT_DATE AND status = 1`).Scan(&stats.ActiveAccounts).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT p.vet AS clinic, COALESCE(m.company_name, '') AS company_name, COUNT(*)::int AS count
			FROM pets p
			LEFT JOIN (SELECT DISTINCT ON (zip) zip, company_name FROM memberlogin_members WHERE TRIM(COALESCE(zip,'')) != '' ORDER BY zip, id) m ON m.zip = p.vet
			WHERE TRIM(COALESCE(p.vet,'')) != ''
			GROUP BY p.vet, m.company_name ORDER BY count DESC`).Scan(&stats.PetsPerClinic).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT amount AS price, COUNT(*)::int AS count FROM payments_ipay WHERE status = 'success' GROUP BY amount ORDER BY count DESC`).Scan(&stats.PaymentTiers).Error
	})
	_ = g.Wait()

	if stats.PetsPerClinic == nil {
		stats.PetsPerClinic = []ClinicPetCount{}
	}
	if stats.PaymentTiers == nil {
		stats.PaymentTiers = []PaymentTier{}
	}

	writeJSON(w, http.StatusOK, stats)
}

// --- Clinic Stats for a Range ---

// RangeProcedure is one procedure type's count and revenue in a range.
type RangeProcedure struct {
	TP     string `json:"tp" validate:"required"`
	TPName string `json:"tpname" validate:"required"`
	Count  int    `json:"count" validate:"required"`
	Total  string `json:"total" validate:"required"`
}

// RangeBucket is one day's (or month's) count and revenue.
type RangeBucket struct {
	Period string `json:"period" validate:"required"`
	Count  int    `json:"count" validate:"required"`
	Total  string `json:"total" validate:"required"`
}

// RangeClinicStats is a clinic's procedures between two dates,
// optionally narrowed to some procedure types.
type RangeClinicStats struct {
	From  string `json:"from" validate:"required"`
	To    string `json:"to" validate:"required"`
	Count int    `json:"count" validate:"required"`
	Total string `json:"total" validate:"required"`
	// Procedures are the selected types (all when none were given).
	Procedures []RangeProcedure `json:"procedures" validate:"required"`
	// Types lists every type recorded in the range, for the filter.
	Types []RangeProcedure `json:"types" validate:"required"`
	// Breakdown is per day for ranges up to 92 days, per month beyond.
	Breakdown []RangeBucket `json:"breakdown" validate:"required"`
	ByMonth   bool          `json:"byMonth"`
}

// maxStatsTypes caps the tp filter; a clinic has a few dozen types.
const maxStatsTypes = 100

// RangeClinic returns the clinic's procedure counts and revenue between
// two dates, optionally for some procedure types only.
// @Summary Get clinic statistics for a date range
// @Tags stats
// @Produce json
// @Security BearerAuth
// @Param from query string true "First day (YYYY-MM-DD)"
// @Param to query string true "Last day (YYYY-MM-DD)"
// @Param tp query string false "Comma-separated procedure types"
// @Param clinic query string false "Clinic code (admin only)"
// @Success 200 {object} RangeClinicStats
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /stats/clinic/range [get]
func (h *StatsHandler) RangeClinic(w http.ResponseWriter, r *http.Request) {
	zip, ok := resolveClinic(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if !isISODate(from) || !isISODate(to) || from > to {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "from and to are required (YYYY-MM-DD), from <= to"})
		return
	}
	var tps []string
	for _, tp := range strings.Split(q.Get("tp"), ",") {
		if tp = strings.TrimSpace(tp); tp != "" {
			tps = append(tps, tp)
		}
	}
	if len(tps) > maxStatsTypes {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "too many procedure types"})
		return
	}

	fromT, _ := time.Parse("2006-01-02", from)
	toT, _ := time.Parse("2006-01-02", to)
	byMonth := toT.Sub(fromT) > 92*24*time.Hour
	period := "date"
	if byMonth {
		period = "LEFT(date, 7)"
	}

	const revenue = `COALESCE(SUM(CASE WHEN price ~ '^[0-9]+(\.[0-9]+){0,1}$' THEN price::numeric END), 0)::text`
	base := `FROM vaccination WHERE sk = ? AND date BETWEEN ? AND ?`
	args := []interface{}{zip, from, to}
	filtered, fargs := base, args
	if len(tps) > 0 {
		filtered = base + ` AND TRIM(tp) IN ?`
		fargs = append(append([]interface{}{}, args...), tps)
	}

	var (
		types, procs []RangeProcedure
		buckets      []RangeBucket
	)
	g := new(errgroup.Group)
	g.Go(func() error {
		return h.db.Raw(`SELECT TRIM(tp) AS tp, MAX(TRIM(tpname)) AS tp_name, COUNT(*)::int AS count, `+revenue+` AS total `+
			base+` GROUP BY TRIM(tp) ORDER BY count DESC`, args...).Scan(&types).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT TRIM(tp) AS tp, MAX(TRIM(tpname)) AS tp_name, COUNT(*)::int AS count, `+revenue+` AS total `+
			filtered+` GROUP BY TRIM(tp) ORDER BY count DESC`, fargs...).Scan(&procs).Error
	})
	g.Go(func() error {
		return h.db.Raw(`SELECT `+period+` AS period, COUNT(*)::int AS count, `+revenue+` AS total `+
			filtered+` GROUP BY 1 ORDER BY 1`, fargs...).Scan(&buckets).Error
	})
	if err := g.Wait(); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to load statistics"})
		return
	}

	out := RangeClinicStats{
		From: from, To: to, ByMonth: byMonth,
		Procedures: procs, Types: types, Breakdown: buckets, Total: "0",
	}
	if out.Procedures == nil {
		out.Procedures = []RangeProcedure{}
	}
	if out.Types == nil {
		out.Types = []RangeProcedure{}
	}
	if out.Breakdown == nil {
		out.Breakdown = []RangeBucket{}
	}
	// Totals come from the buckets, which cover the same rows.
	sum := new(big.Rat)
	for _, b := range out.Breakdown {
		out.Count += b.Count
		if v, ok := new(big.Rat).SetString(b.Total); ok {
			sum.Add(sum, v)
		}
	}
	out.Total = strings.TrimSuffix(strings.TrimRight(sum.FloatString(2), "0"), ".")
	writeJSON(w, http.StatusOK, out)
}
