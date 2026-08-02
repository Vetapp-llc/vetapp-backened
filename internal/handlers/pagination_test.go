package handlers

import (
	"net/http/httptest"
	"testing"
)

// Page params are clamped rather than rejected: an out-of-range value
// should still return a usable page instead of a 400, but must never
// widen the query. The cap is the only thing preventing a clinic-scoped
// list from returning every row it has ever produced — 84,847 for the
// busiest clinic's procedures before these endpoints were paginated.
func TestParsePageParamsClamping(t *testing.T) {
	cases := []struct {
		query        string
		wantPage     int
		wantPageSize int
		wantOffset   int
	}{
		{"", 1, 50, 0},
		{"?page=1&pageSize=50", 1, 50, 0},
		{"?page=3&pageSize=20", 3, 20, 40},
		// Below range → defaults.
		{"?page=0", 1, 50, 0},
		{"?page=-5", 1, 50, 0},
		{"?pageSize=0", 1, 50, 0},
		{"?pageSize=-1", 1, 50, 0},
		// Above the cap → clamped, never honoured.
		{"?pageSize=201", 1, 200, 0},
		{"?pageSize=100000", 1, 200, 0},
		// Garbage → defaults, not an error.
		{"?page=abc&pageSize=xyz", 1, 50, 0},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/api/procedures"+tc.query, nil)
		got := ParsePageParams(r)
		if got.Page != tc.wantPage || got.PageSize != tc.wantPageSize || got.Offset != tc.wantOffset {
			t.Errorf("ParsePageParams(%q) = {page:%d size:%d offset:%d}, want {page:%d size:%d offset:%d}",
				tc.query, got.Page, got.PageSize, got.Offset,
				tc.wantPage, tc.wantPageSize, tc.wantOffset)
		}
	}
}

// No input may produce a page size above the cap — this is the property
// that actually bounds the response, so assert it directly rather than
// relying on the table above staying exhaustive.
func TestParsePageParamsNeverExceedsCap(t *testing.T) {
	for _, q := range []string{
		"?pageSize=201", "?pageSize=999999", "?pageSize=9223372036854775807",
		"?pageSize=1e10", "?pageSize=+500", "?pageSize=%20300",
	} {
		r := httptest.NewRequest("GET", "/x"+q, nil)
		if got := ParsePageParams(r); got.PageSize > maxPageSize {
			t.Errorf("ParsePageParams(%q) returned pageSize %d, above the %d cap",
				q, got.PageSize, maxPageSize)
		}
	}
}

func TestNewPaginatedResponse(t *testing.T) {
	cases := []struct {
		total      int64
		pageSize   int
		wantPages  int
	}{
		{0, 50, 0},
		{1, 50, 1},
		{50, 50, 1},
		{51, 50, 2},
		{84847, 50, 1697}, // the real procedures count for the busiest clinic
	}
	for _, tc := range cases {
		p := PageParams{Page: 1, PageSize: tc.pageSize}
		got := NewPaginatedResponse([]string{}, tc.total, p)
		if got.TotalPages != tc.wantPages {
			t.Errorf("total=%d pageSize=%d: TotalPages = %d, want %d",
				tc.total, tc.pageSize, got.TotalPages, tc.wantPages)
		}
		if got.Total != tc.total {
			t.Errorf("Total = %d, want %d", got.Total, tc.total)
		}
	}
}
