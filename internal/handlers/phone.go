package handlers

import (
	"regexp"
	"strings"
)

var nonDigits = regexp.MustCompile(`[^0-9]`)

// normalizePhone returns the form phones are stored in, or "" when the
// input is not a usable number.
//
// Georgian mobiles are stored the way the legacy PHP app stored them —
// nine local digits, "5XXXXXXXX" — which is what 644 of ~700 owner rows
// and nearly every pets.phone already hold, and what the SMS reminders
// send to. Accepted inputs: "555123456", "0555123456", "995555123456",
// "+995 555 12-34-56". Anything else with a leading "+" or a known
// international prefix is kept as "+<digits>".
func normalizePhone(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	plus := strings.HasPrefix(s, "+") || strings.HasPrefix(s, "00")
	d := nonDigits.ReplaceAllString(s, "")
	if strings.HasPrefix(s, "00") {
		d = strings.TrimPrefix(d, "00")
	}
	switch {
	case len(d) == 9 && d[0] == '5':
		return d
	case len(d) == 10 && strings.HasPrefix(d, "05"):
		return d[1:]
	case len(d) == 12 && strings.HasPrefix(d, "9955"):
		return d[3:]
	case plus && strings.HasPrefix(d, "995"):
		// +995 but not a mobile (landline, wrong length): keep as typed.
		if len(d) >= 11 && len(d) <= 12 {
			return "+" + d
		}
		return ""
	case plus && len(d) >= 7 && len(d) <= 15:
		return "+" + d
	}
	return ""
}

// phoneMatchSQL matches a stored phone against a normalized one,
// canonicalising the stored value the way normalizePhone does for
// Georgian mobiles ("+995…", "00995…", "995…", "05…", spaces, dashes).
// Use with two args: the normalized phone twice. (No literal "?" in the
// SQL: GORM would take it for a placeholder.)
const phoneMatchSQL = `(phone = ? OR regexp_replace(regexp_replace(phone, '[^0-9+]', '', 'g'),
	'^([+]|00){0,1}(995|0){0,1}(5[0-9]{8})$', '\3') = ?)`

// smsDestination is the number format the SMS gateway expects: the
// legacy app sent Georgian numbers as nine local digits, which works;
// international numbers go without the "+".
func smsDestination(normalized string) string {
	return strings.TrimPrefix(normalized, "+")
}
