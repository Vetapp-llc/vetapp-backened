package models

// Shop maps to the shop table (retail sales).
//
// As with Payment, the legacy column names differ from the Go field
// names and the table is narrower than the struct implied: it has
// exactly (id, date, name, coment, price, zip, pay). The previous
// definition mapped SK → `sk` and declared a `vetname` column, neither
// of which exists, so listing clinic sales failed outright.
//
// Column mapping (see admin/shopadd.php in the legacy frontend:
// `insert into shop (date, name, price, coment, zip, pay)`):
//
//	zip    → clinic code   (SK)
//	pay    → payment method, same Georgian values as paymethod.pay
//	coment → free-text quantity/notes ("1 ფირფიტა", "8 ტაბლეტი")
//
// There is no staff column — a sale is attributed to the clinic, not
// to an individual seller.
type Shop struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Name    string `json:"name"`                         // Product name
	Price   string `json:"price"`                        // Sale price
	Date    string `json:"date"`                         // Sale date
	SK      string `gorm:"column:zip" json:"sk"`         // Clinic zip
	Method  string `gorm:"column:pay" json:"method"`     // "ბარათი" / "ნაღდი ანგარიშწორება"
	Comment string `gorm:"column:coment" json:"comment"` // Quantity / notes
}

func (Shop) TableName() string { return "shop" }
