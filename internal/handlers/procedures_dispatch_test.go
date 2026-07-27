// Tests for the pure-logic helpers around the polymorphic
// `vaccination` table — tp dispatch, ecto extraction, test panel
// extraction, and the owner item builder. Database-free.
package handlers

import (
	"reflect"
	"testing"

	"vetapp-backend/internal/models"
)

func TestProcedureNameForTP(t *testing.T) {
	cases := []struct {
		tp   int
		want string
	}{
		{1, "ვაქცინაცია"},
		{2, "ანალიზი (ძაღლი)"},
		{22, "ანალიზი (კატა)"},
		{222, "ანალიზი (სხვა)"},
		{11, "ექტოპარაზიტების პრევენცია"},
		{12, "დეჰელმინთიზაცია"},
		{106, "ქირურგია"},
		{107, "სხვა პროცედურა"},
		{109, "რადიოლოგია"},
		{116, "ლაბორატორია"},
		{202, "თერაპია"},
		{203, "ოფთალმოლოგია"},
		// Orphan / unknown tps return empty rather than a fabricated
		// label so the UI knows to fall back to its own default.
		{3, ""},
		{4, ""},
		{5, ""},
		{555, ""},
		{0, ""},
	}
	for _, tc := range cases {
		got := procedureNameForTP(tc.tp)
		if got != tc.want {
			t.Errorf("procedureNameForTP(%d) = %q, want %q", tc.tp, got, tc.want)
		}
	}
}

func TestIsGenericProcedureTP(t *testing.T) {
	for _, tp := range []int{101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 115, 116, 202, 203} {
		if !isGenericProcedureTP(tp) {
			t.Errorf("isGenericProcedureTP(%d) = false, want true", tp)
		}
	}
	for _, tp := range []int{1, 2, 11, 12, 22, 222, 3, 4, 5, 555, 0} {
		if isGenericProcedureTP(tp) {
			t.Errorf("isGenericProcedureTP(%d) = true, want false", tp)
		}
	}
}

func TestIsTestTP(t *testing.T) {
	for _, tp := range []int{2, 22, 222} {
		if !isTestTP(tp) {
			t.Errorf("isTestTP(%d) = false, want true", tp)
		}
	}
	for _, tp := range []int{1, 11, 12, 106, 107, 0} {
		if isTestTP(tp) {
			t.Errorf("isTestTP(%d) = true, want false", tp)
		}
	}
}

// TestExtractEctoItems covers the legacy 8-slot custom/choice scheme:
//
//	drops:  Vac1 (choice) / Vac (custom)
//	pills:  Vac3 (choice) / Vac2 (custom)
//	collar: Vac5 (choice) / Vac4 (custom)
//	spray:  Vac7 (choice) / Vac6 (custom)
//
// Plus the PHP `addecto.php:459` typo bug where `$vac4` is mistakenly
// written into the `vac6` column.
func TestExtractEctoItems(t *testing.T) {
	cases := []struct {
		name string
		proc models.Procedure
		want []OwnerEctoItem
	}{
		{
			name: "all four types from owner-app dropdown selection",
			proc: models.Procedure{
				Vac1: "Frontline",
				Vac3: "Bravecto",
				Vac5: "Seresto",
				Vac7: "Frontline Spray",
			},
			want: []OwnerEctoItem{
				{Type: "drops", Name: "Frontline"},
				{Type: "pills", Name: "Bravecto"},
				{Type: "collar", Name: "Seresto"},
				{Type: "spray", Name: "Frontline Spray"},
			},
		},
		{
			name: "custom-typed name preferred over dropdown choice",
			proc: models.Procedure{
				Vac:  "Owner-typed brand",
				Vac1: "Frontline (dropdown)",
			},
			want: []OwnerEctoItem{
				{Type: "drops", Name: "Owner-typed brand"},
			},
		},
		{
			name: "empty record returns empty",
			proc: models.Procedure{},
			want: nil,
		},
		{
			name: "whitespace-only values treated as empty",
			proc: models.Procedure{
				Vac1: "   ",
				Vac3: "\t\n",
			},
			want: nil,
		},
		{
			name: "PHP bug: vac4 duplicated into vac6 — collar shown, spray suppressed",
			proc: models.Procedure{
				Vac4: "სკალიბორ მცირე", // collar custom
				Vac6: "სკალიბორ მცირე", // duplicated by PHP bug
			},
			want: []OwnerEctoItem{
				{Type: "collar", Name: "სკალიბორ მცირე"},
			},
		},
		{
			name: "PHP bug: when both bug and a real spray dropdown choice exist, dropdown still surfaces",
			proc: models.Procedure{
				Vac4: "სკალიბორ მცირე",  // collar custom
				Vac6: "სკალიბორ მცირე",  // duplicated by bug
				Vac7: "Frontline Spray", // legitimate spray dropdown
			},
			want: []OwnerEctoItem{
				{Type: "collar", Name: "სკალიბორ მცირე"},
				{Type: "spray", Name: "Frontline Spray"},
			},
		},
		{
			name: "Vac6 set without Vac4 — not bug, real spray-custom value",
			proc: models.Procedure{
				Vac6: "Custom spray",
			},
			want: []OwnerEctoItem{
				{Type: "spray", Name: "Custom spray"},
			},
		},
		{
			name: "Vac4 and Vac6 differ — both are real",
			proc: models.Procedure{
				Vac4: "Custom collar",
				Vac6: "Custom spray",
			},
			want: []OwnerEctoItem{
				{Type: "collar", Name: "Custom collar"},
				{Type: "spray", Name: "Custom spray"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractEctoItems(&tc.proc)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("extractEctoItems = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestExtractTestResults sanity-checks the dog-test column → label
// table from `vet/addtest.php` plus the cat/other fallback labelling.
func TestExtractTestResults(t *testing.T) {
	t.Run("dog test (tp=2) maps PHP columns to labels", func(t *testing.T) {
		p := models.Procedure{
			TP:    2,
			VacN:  "უარყოფითი",
			Deh:   "დადებითი",
			Vac1:  "უარყოფითი",
			Test1: "დადებითი",
		}
		got := extractTestResults(&p)
		want := []OwnerTestResult{
			{Label: "Leishmania", Result: "უარყოფითი"},
			{Label: "Canine Babesia", Result: "დადებითი"},
			{Label: "GiarDia duodenalis", Result: "უარყოფითი"},
			{Label: "Caniv 4DX — Ehrlichia", Result: "დადებითი"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("extractTestResults = %+v, want %+v", got, want)
		}
	})

	t.Run("cat test (tp=22) uses generic labels", func(t *testing.T) {
		p := models.Procedure{
			TP:   22,
			VacN: "უარყოფითი",
			Vac1: "დადებითი",
		}
		got := extractTestResults(&p)
		want := []OwnerTestResult{
			{Label: "Test 1", Result: "უარყოფითი"},
			{Label: "Test 3", Result: "დადებითი"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("extractTestResults = %+v, want %+v", got, want)
		}
	})

	t.Run("non-test tp returns nil", func(t *testing.T) {
		p := models.Procedure{TP: 1, VacN: "anything"}
		if got := extractTestResults(&p); got != nil {
			t.Errorf("extractTestResults on non-test tp = %v, want nil", got)
		}
	})

	t.Run("test record with all empty columns returns nil", func(t *testing.T) {
		p := models.Procedure{TP: 2}
		if got := extractTestResults(&p); got != nil {
			t.Errorf("extractTestResults on empty test record = %v, want nil", got)
		}
	})
}

func TestBuildOwnerProcedureItem(t *testing.T) {
	t.Run("vaccination tp=1 fills vaccine type, preparat, serial", func(t *testing.T) {
		p := models.Procedure{
			ID:     42,
			TP:     1,
			TPName: "ვაქცინაცია",
			Date:   "2026-01-15",
			Date2:  "2027-01-15",
			Vac:    "კომპლექსური ვაქცინა",
			VacN:   "Eurican DHPPi2-L",
			Ser:    "ABC123",
			Coment: "comment text",
		}
		got := buildOwnerProcedureItem(&p, nil)
		if got.VaccineType != "კომპლექსური ვაქცინა" {
			t.Errorf("VaccineType = %q", got.VaccineType)
		}
		if got.Preparat != "Eurican DHPPi2-L" {
			t.Errorf("Preparat = %q", got.Preparat)
		}
		if got.Serial != "ABC123" {
			t.Errorf("Serial = %q", got.Serial)
		}
		if got.Comment != "comment text" {
			t.Errorf("Comment = %q", got.Comment)
		}
		if got.ProcedureName != "ვაქცინაცია" {
			t.Errorf("ProcedureName = %q", got.ProcedureName)
		}
		if got.NextDate == nil || *got.NextDate != "2027-01-15" {
			t.Errorf("NextDate = %v", got.NextDate)
		}
		if !got.AddedByOwner {
			t.Errorf("expected AddedByOwner=true with empty vetname")
		}
	})

	t.Run("ectoparasite tp=11 routes through extractEctoItems", func(t *testing.T) {
		p := models.Procedure{
			TP:   11,
			Vac1: "Frontline",
		}
		got := buildOwnerProcedureItem(&p, nil)
		if len(got.EctoItems) != 1 || got.EctoItems[0].Type != "drops" {
			t.Errorf("EctoItems = %+v, want one drops entry", got.EctoItems)
		}
	})

	t.Run("dehelminization tp=12 prefers Deh over Vac", func(t *testing.T) {
		p := models.Procedure{TP: 12, Deh: "Drontal", Vac: "ignored"}
		got := buildOwnerProcedureItem(&p, nil)
		if got.Preparat != "Drontal" {
			t.Errorf("Preparat = %q, want Drontal", got.Preparat)
		}
	})

	t.Run("dehelminization tp=12 falls back to Vac when Deh empty", func(t *testing.T) {
		p := models.Procedure{TP: 12, Vac: "Custom dewormer"}
		got := buildOwnerProcedureItem(&p, nil)
		if got.Preparat != "Custom dewormer" {
			t.Errorf("Preparat = %q, want Custom dewormer", got.Preparat)
		}
	})

	t.Run("generic procedure tp=106 maps Vac1/Vac2/Vac3 to anamnesis/diagnosis/treatment", func(t *testing.T) {
		p := models.Procedure{
			TP:   106,
			Vac:  "კასტრაცია",
			Vac1: "history of mild allergy",
			Vac2: "ovarian cyst",
			Vac3: "surgical removal",
		}
		got := buildOwnerProcedureItem(&p, nil)
		if got.Anamnesis != "history of mild allergy" {
			t.Errorf("Anamnesis = %q", got.Anamnesis)
		}
		if got.Diagnosis != "ovarian cyst" {
			t.Errorf("Diagnosis = %q", got.Diagnosis)
		}
		if got.Treatment != "surgical removal" {
			t.Errorf("Treatment = %q", got.Treatment)
		}
		// Per-record `Vac` value overrides the canonical category label.
		// This is the partner-spec'd behaviour: the procedure name
		// field is editable per record, so a non-empty `Vac` is the
		// user's chosen name and wins over "ქირურგია".
		if got.ProcedureName != "კასტრაცია" {
			t.Errorf("ProcedureName = %q, want კასტრაცია (Vac override)", got.ProcedureName)
		}
	})

	t.Run("generic procedure name falls back to canonical label when Vac empty", func(t *testing.T) {
		p := models.Procedure{TP: 106}
		got := buildOwnerProcedureItem(&p, nil)
		// No per-record name set → use the localized category label
		// from procedureTypeNames[106] = "ქირურგია".
		if got.ProcedureName != "ქირურგია" {
			t.Errorf("ProcedureName = %q, want ქირურგია (canonical fallback)", got.ProcedureName)
		}
	})

	t.Run("generic procedure falls back to Anam/Diagn/Nout when Vac1/2/3 empty", func(t *testing.T) {
		p := models.Procedure{
			TP:    106,
			Anam:  "fallback anam",
			Diagn: "fallback diagn",
			Nout:  "fallback nout",
		}
		got := buildOwnerProcedureItem(&p, nil)
		if got.Anamnesis != "fallback anam" {
			t.Errorf("Anamnesis = %q", got.Anamnesis)
		}
		if got.Diagnosis != "fallback diagn" {
			t.Errorf("Diagnosis = %q", got.Diagnosis)
		}
		if got.Treatment != "fallback nout" {
			t.Errorf("Treatment = %q", got.Treatment)
		}
	})

	t.Run("test tp=2 populates TestResults", func(t *testing.T) {
		p := models.Procedure{TP: 2, VacN: "უარყოფითი"}
		got := buildOwnerProcedureItem(&p, nil)
		if len(got.TestResults) != 1 || got.TestResults[0].Label != "Leishmania" {
			t.Errorf("TestResults = %+v", got.TestResults)
		}
	})

	t.Run("vetname numeric resolved via JOIN map", func(t *testing.T) {
		p := models.Procedure{TP: 1, VetName: "47"}
		got := buildOwnerProcedureItem(&p, map[string]string{"47": "Dr. Maia"})
		if got.VetFullName != "Dr. Maia" {
			t.Errorf("VetFullName = %q, want Dr. Maia", got.VetFullName)
		}
		if got.AddedByOwner {
			t.Errorf("expected AddedByOwner=false for vet-attributed record")
		}
	})

	t.Run("vetname with trailing whitespace trimmed before lookup", func(t *testing.T) {
		p := models.Procedure{TP: 1, VetName: "47  "}
		got := buildOwnerProcedureItem(&p, map[string]string{"47": "Dr. Maia"})
		if got.VetFullName != "Dr. Maia" {
			t.Errorf("VetFullName = %q (trim broken?)", got.VetFullName)
		}
		if got.VetName != "47" {
			t.Errorf("VetName = %q, want trimmed", got.VetName)
		}
	})

	t.Run("vetname '0' counts as owner-added", func(t *testing.T) {
		p := models.Procedure{TP: 1, VetName: "0"}
		got := buildOwnerProcedureItem(&p, nil)
		if !got.AddedByOwner {
			t.Errorf("expected AddedByOwner=true for vetname='0'")
		}
	})

	t.Run("prescription column (dani) surfaces on every category", func(t *testing.T) {
		// dani is the legacy prescription column. Per partner spec it
		// should appear on the owner accordion regardless of category.
		p := models.Procedure{TP: 106, Dani: "Take 1 tablet/day for 7 days"}
		got := buildOwnerProcedureItem(&p, nil)
		if got.Prescription != "Take 1 tablet/day for 7 days" {
			t.Errorf("Prescription = %q", got.Prescription)
		}
	})

	t.Run("comment field has <br /> stripped", func(t *testing.T) {
		p := models.Procedure{
			TP:     1,
			Coment: "line one<br />line two<br/>line three",
		}
		got := buildOwnerProcedureItem(&p, nil)
		if got.Comment != "line one\nline two\nline three" {
			t.Errorf("Comment = %q (cleanText not applied?)", got.Comment)
		}
	})

	t.Run("legacy tpname='1' replaced with canonical label", func(t *testing.T) {
		p := models.Procedure{TP: 1, TPName: "1"}
		got := buildOwnerProcedureItem(&p, nil)
		if got.ProcedureName != "ვაქცინაცია" {
			t.Errorf("ProcedureName = %q, want ვაქცინაცია (numeric tpname not normalized)", got.ProcedureName)
		}
	})

	t.Run("vaccinations array always non-nil JSON-friendly", func(t *testing.T) {
		p := models.Procedure{TP: 1}
		got := buildOwnerProcedureItem(&p, nil)
		if got.Vaccinations == nil {
			t.Errorf("Vaccinations = nil, expected empty []string{} for JSON marshalling")
		}
		if len(got.Vaccinations) != 0 {
			t.Errorf("Vaccinations = %v, expected empty", got.Vaccinations)
		}
	})
}

func TestOwnerWriteAllowedTPs(t *testing.T) {
	// Whitelist must match the mobile app's OWNER_WRITE_ALLOWED_NAMES set.
	// Owners can write to every procedure category EXCEPT tests (which
	// need a structured panel) and any unrecognized tp.
	allowed := []int{1, 11, 12, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 115, 116, 202, 203}
	for _, tp := range allowed {
		if !ownerWriteAllowedTPs[tp] {
			t.Errorf("expected tp=%d to be allowed for owner writes", tp)
		}
	}
	denied := []int{2, 22, 222, 0, 999, 3, 4, 5, 555}
	for _, tp := range denied {
		if ownerWriteAllowedTPs[tp] {
			t.Errorf("expected tp=%d to be denied for owner writes", tp)
		}
	}
}

// commaDate converts the reminder date into the legacy comma format
// stored in vaccination.date3. Legacy sentinels must survive untouched
// — the clinic's PHP tooling still reads these columns, and rewriting
// ",-1," or "--" into something "cleaner" would corrupt live rows.
func TestCommaDate(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// ISO dates convert.
		{"2027-03-18", "2027,03,18"},
		{"2026-12-31", "2026,12,31"},
		{"2000-01-01", "2000,01,01"},
		// Legacy sentinels and junk pass through unchanged.
		{"", ""},
		{"--", "--"},
		{",-1,", ",-1,"},
		{"2027,03,18", "2027,03,18"},
		// Wrong shape or non-digits: leave alone rather than mangle.
		{"2027-3-18", "2027-3-18"},
		{"not-a-date", "not-a-date"},
		{"20270318", "20270318"},
		{"abcd-ef-gh", "abcd-ef-gh"},
	}
	for _, tc := range cases {
		if got := commaDate(tc.in); got != tc.want {
			t.Errorf("commaDate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
