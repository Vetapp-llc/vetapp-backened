package models

// Payment maps to the paymethod table.
//
// The legacy PHP schema names these columns differently from the Go
// field names, and the table is narrower than it looks: it has exactly
// (id, zip, date, uuid, sum, pay). Without the explicit column tags
// below GORM derives `sk`, `amount`, `method` — none of which exist —
// and every read AND write against this table fails with
// `column "..." does not exist` (SQLSTATE 42703). That took out the
// clinic payment history and, worse, payment recording itself.
//
// Column mapping (see admin/paystatus.php in the legacy frontend:
// `insert into paymethod (zip, date, uuid, sum, pay)`):
//
//	zip  → clinic code           (SK)
//	sum  → amount in GEL         (Amount)
//	pay  → payment method, in Georgian: "ბარათი" (card) /
//	       "ნაღდი ანგარიშწორება" (cash)  (Method)
//
// There is no vet or owner column on this table — attribution is done
// via the pet (`uuid`) and the clinic (`zip`).
type Payment struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	UUID   string `json:"uuid"`                     // Pet ID
	Date   string `json:"date"`                     // Payment date
	Method string `gorm:"column:pay" json:"method"` // "ბარათი" (card) / "ნაღდი ანგარიშწორება" (cash)
	Amount string `gorm:"column:sum" json:"amount"` // Amount in GEL
	SK     string `gorm:"column:zip" json:"sk"`     // Clinic zip
}

func (Payment) TableName() string { return "paymethod" }

// Payment method values as stored by the legacy PHP frontend. New
// writes must use these exact strings so the clinic's existing reports
// (which group by `pay`) keep working.
const (
	PayMethodCard = "ბარათი"
	PayMethodCash = "ნაღდი ანგარიშწორება"
)

// NormalizePayMethod maps the API's ASCII method names onto the
// Georgian values the column actually stores. Anything already in the
// stored form, or unrecognised, is passed through untouched so a future
// method doesn't get silently rewritten to "card".
func NormalizePayMethod(m string) string {
	switch m {
	case "card", "CARD", "Card":
		return PayMethodCard
	case "cash", "CASH", "Cash":
		return PayMethodCash
	default:
		return m
	}
}
