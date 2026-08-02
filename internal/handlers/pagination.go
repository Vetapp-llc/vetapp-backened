package handlers

import (
	"net/http"
	"strconv"

	"gorm.io/gorm"
)

// Pagination defaults. The cap matters more than the default: it is the
// only thing standing between a clinic-scoped list query and a response
// containing every row that clinic has ever produced.
//
// Measured against production data (2026-08-02), the busiest clinic's
// unbounded lists would have returned:
//
//	procedures  84,847 rows  (~24.5 MB)
//	payments    52,225 rows
//	shop         9,260 rows
//
// Each of those is one request holding a database connection, a
// server-side buffer and a client-side parse for seconds. At any real
// concurrency that exhausts the connection pool long before the
// database itself struggles.
const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// PageParams is a validated page/pageSize pair.
type PageParams struct {
	Page     int
	PageSize int
	Offset   int
}

// ParsePageParams reads `page` and `pageSize` from the query string and
// clamps them into a safe range.
//
// Both are clamped rather than rejected: a client asking for page 0 or
// pageSize 10000 gets a sensible response instead of a 400, which keeps
// older clients working while still bounding the query.
func ParsePageParams(r *http.Request) PageParams {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	switch {
	case size < 1:
		size = defaultPageSize
	case size > maxPageSize:
		size = maxPageSize
	}
	return PageParams{Page: page, PageSize: size, Offset: (page - 1) * size}
}

// Paginate applies a page window to a query.
//
// Usage is deliberately two-step — Count first on the *unpaginated*
// query, then Paginate — because GORM mutates the statement, so
// counting after applying Offset/Limit returns the page size rather
// than the total.
func (p PageParams) Paginate(q *gorm.DB) *gorm.DB {
	return q.Offset(p.Offset).Limit(p.PageSize)
}

// NewPaginatedResponse builds the standard envelope, computing the page
// count so every endpoint reports it the same way.
func NewPaginatedResponse(data interface{}, total int64, p PageParams) PaginatedResponse {
	totalPages := 0
	if p.PageSize > 0 {
		totalPages = int((total + int64(p.PageSize) - 1) / int64(p.PageSize))
	}
	return PaginatedResponse{
		Data:       data,
		Total:      total,
		Page:       p.Page,
		PageSize:   p.PageSize,
		TotalPages: totalPages,
	}
}
