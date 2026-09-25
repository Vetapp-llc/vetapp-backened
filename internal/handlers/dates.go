package handlers

import (
	"regexp"
	"strings"
	"time"
)

// Procedure type codes with side effects on the pet record.
const (
	tpSterilisation = 110 // vet/addprocedure3.php → pets.cast / castdate
	tpMicrochip     = 115 // vet/addprocedure4.php → pets.chip / chipd
)

// georgia is the clinics' local time. A fixed offset rather than
// time.LoadLocation: Georgia has had no DST since 2005, and a fixed zone
// cannot fail on a container without tzdata.
var georgia = time.FixedZone("Asia/Tbilisi", 4*60*60)

// todayGeorgia is today's date in the clinics' timezone, YYYY-MM-DD —
// the format the legacy text date columns hold.
func todayGeorgia() string {
	return time.Now().In(georgia).Format("2006-01-02")
}

// isISODate reports whether s is a real calendar date in YYYY-MM-DD form.
func isISODate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// legacyDate3 derives `vaccination.date3` from `date2` the way the PHP
// forms do.
//
// date3 is not a separate reminder date. The PHP calendar passes it
// straight into JavaScript's `new Date(y, m, d)`, whose month is
// zero-based, so the PHP forms store date2 with the month shifted back
// by one: vet/addvac.php does date("Y,m,d", strtotime("-1 month", date2)).
// Go's AddDate normalises month overflow exactly like strtotime
// (2026-03-31 → 2026,03,03), so the stored strings match byte for byte.
//
// An empty or non-ISO date2 yields "": legacy sentinels such as "--" are
// never produced for new rows.
func legacyDate3(date2 string) string {
	t, err := time.Parse("2006-01-02", date2)
	if err != nil {
		return ""
	}
	return t.AddDate(0, -1, 0).Format("2006,01,02")
}

// isMoney reports whether s is a non-negative amount with at most two
// decimals — the only shape the revenue aggregates can sum. Legacy rows
// hold free text ("50 ლარი", ""), which the reports skip; new rows must
// be summable.
func isMoney(s string) bool {
	return moneyRe.MatchString(s)
}

var moneyRe = regexp.MustCompile(`^[0-9]{1,9}(\.[0-9]{1,2})?$`)

// numericGuard is the SQL predicate the aggregates use to skip legacy
// free-text amounts before casting. `{0,1}` rather than `?`: GORM counts
// every literal '?' as a bind placeholder.
func numericGuard(col string) string {
	return col + ` ~ '^[0-9]+(\.[0-9]+){0,1}$'`
}

// normalizeSpecies and normalizeSex store the Georgian values every
// legacy row uses (ძაღლი / კატა, ხვადი male / ძუ female). The web and
// mobile forms send English keys; stored as-is they were invisible to the
// PHP pages and statistics, which filter on the Georgian words.
func normalizeSpecies(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "dog", "ძაღლი":
		return "ძაღლი"
	case "cat", "კატა":
		return "კატა"
	case "other", "სხვა":
		return "სხვა"
	}
	return strings.TrimSpace(s)
}

func normalizeSex(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "male", "m", "ხვადი":
		return "ხვადი"
	case "female", "f", "ძუ":
		return "ძუ"
	}
	return strings.TrimSpace(s)
}
