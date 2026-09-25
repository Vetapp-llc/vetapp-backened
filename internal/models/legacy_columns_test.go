// Column-mapping tests for the legacy PHP-era tables.
//
// These exist because of a bug class that is invisible in review and
// only shows up as a 500 at runtime: the Go field name and the actual
// Postgres column name disagree, GORM derives the wrong column, and
// every query against that table dies with
// `column "..." does not exist` (SQLSTATE 42703).
//
// It cost the clinic its payment history, its retail sales list, its
// daily card/cash split, AND the ability to record a payment at all —
// because `paymethod` is (id, zip, date, uuid, sum, pay) while the
// struct assumed (sk, amount, method, vet_id, owner).
//
// The legacy schema is NOT consistent: `vaccination`, `alergy`, `eals`
// and `operationdate` really do have an `sk` column, while `shop`,
// `paymethod` and `prices` use `zip`. So "just rename them all" is
// wrong — each table has to be pinned individually, which is what
// these tests do. They use GORM's schema parser rather than a live
// database, so they run offline.
package models

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// columnFor returns the database column GORM would use for a given
// struct field.
func columnFor(t *testing.T, model any, fieldName string) string {
	t.Helper()
	s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("schema.Parse(%T): %v", model, err)
	}
	f := s.LookUpField(fieldName)
	if f == nil {
		t.Fatalf("%T has no field %q", model, fieldName)
	}
	return f.DBName
}

// assertColumns pins each Go field to the column it must map to.
func assertColumns(t *testing.T, model any, want map[string]string) {
	t.Helper()
	for field, col := range want {
		if got := columnFor(t, model, field); got != col {
			t.Errorf("%T.%s maps to column %q, want %q", model, field, got, col)
		}
	}
}

// TestPaymentColumns pins paymethod (id, zip, date, uuid, sum, pay).
func TestPaymentColumns(t *testing.T) {
	assertColumns(t, &Payment{}, map[string]string{
		"ID":     "id",
		"UUID":   "uuid",
		"Date":   "date",
		"Method": "pay",
		"Amount": "sum",
		"SK":     "zip",
	})
}

// TestShopColumns pins shop (id, date, name, coment, price, zip, pay).
func TestShopColumns(t *testing.T) {
	assertColumns(t, &Shop{}, map[string]string{
		"ID":      "id",
		"Name":    "name",
		"Price":   "price",
		"Date":    "date",
		"SK":      "zip",
		"Method":  "pay",
		"Comment": "coment",
	})
}

// TestPriceColumns guards the mapping that was already correct, so a
// well-meaning "consistency" refactor can't quietly break it.
func TestPriceColumns(t *testing.T) {
	assertColumns(t, &Price{}, map[string]string{
		"SK": "zip",
	})
}

// TestSKTablesKeepSK is the other half of the guard: these tables
// genuinely have an `sk` column, so their SK field must NOT be
// remapped to `zip`.
func TestSKTablesKeepSK(t *testing.T) {
	assertColumns(t, &Procedure{}, map[string]string{"SK": "sk"})
	assertColumns(t, &Allergy{}, map[string]string{"SK": "sk"})
	assertColumns(t, &Appointment{}, map[string]string{"SK": "sk"})
}

// TestNormalizePayMethod covers the ASCII → Georgian translation that
// keeps the clinic's existing revenue reports (which GROUP BY `pay`)
// working regardless of which frontend recorded the payment.
func TestNormalizePayMethod(t *testing.T) {
	cases := []struct{ in, want string }{
		{"card", PayMethodCard},
		{"Card", PayMethodCard},
		{"CARD", PayMethodCard},
		{"cash", PayMethodCash},
		{"Cash", PayMethodCash},
		{"CASH", PayMethodCash},
		// Already-stored Georgian values pass through untouched.
		{PayMethodCard, PayMethodCard},
		{PayMethodCash, PayMethodCash},
		// Unknown values are not silently coerced to "card".
		{"", ""},
		{"transfer", "transfer"},
	}
	for _, tc := range cases {
		if got := NormalizePayMethod(tc.in); got != tc.want {
			t.Errorf("NormalizePayMethod(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAppointmentColumns pins operationdate. The table is
// (id, uuid, date, date2, date3, operation, sk, pname, owner, ownern,
// coment, tp, price, sax, pet, vetname, time) — no phone, tpname,
// koment or status column, and getting this wrong meant clinics could
// not book an appointment at all.
// Date is the appointment day, which PHP keeps in date2; `date` is the
// day the booking was made. Mapping Date to `date` put every appointment
// on its booking day.
func TestAppointmentColumns(t *testing.T) {
	assertColumns(t, &Appointment{}, map[string]string{
		"ID":       "id",
		"UUID":     "uuid",
		"Date":     "date2",
		"BookedOn": "date",
		"Date3":    "date3",
		"Time":     "time",
		"SK":       "sk",
		"VetName":  "vetname",
		"PName":    "pname",
		"Owner":    "owner",
		"OwnerN":   "ownern",
		"TPName":   "operation",
		"Koment":   "coment",
	})
}

// Status and Phone must never reach the database.
func TestAppointmentIgnoresAbsentColumns(t *testing.T) {
	s, err := schema.Parse(&Appointment{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("schema.Parse: %v", err)
	}
	for _, name := range s.DBNames {
		if name == "status" || name == "phone" || name == "tpname" || name == "koment" {
			t.Errorf("column %q is persisted but does not exist on operationdate", name)
		}
	}
}

// eals keeps the allergy in `vac` and the note in `ser`
// (vet/addeals.php); `name` is empty on every production row.
func TestAllergyColumns(t *testing.T) {
	assertColumns(t, &Allergy{}, map[string]string{
		"Name":    "vac",
		"Comment": "ser",
		"SK":      "sk",
	})
}

func TestPetDateColumns(t *testing.T) {
	assertColumns(t, &Pet{}, map[string]string{
		"ChipDate": "chipd",
		"CastDate": "castdate",
	})
}
