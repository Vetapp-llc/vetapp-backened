package models

// Appointment maps to the legacy `operationdate` table — procedures booked
// for a future day (vet/addoperationdate.php, vet/addproceduredate.php).
//
//	id, uuid, date, date2, date3, operation, sk, pname, owner, ownern,
//	coment, tp, price, sax, pet, vetname, time
//
// Column semantics, confirmed against the PHP writers and live data:
//   - date2 is the day of the appointment (every PHP reader filters on it)
//   - date  is the day the booking was made
//   - date3 is date2 in the PHP calendar's JavaScript-month encoding
//
// `Date` therefore maps to date2. It used to map to `date`, which put
// every appointment on the day it was booked.
type Appointment struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	UUID     string `json:"uuid"`                           // Pet ID (empty for walk-in bookings)
	Date     string `gorm:"column:date2" json:"date"`       // Appointment day
	BookedOn string `gorm:"column:date" json:"booked_on"`   // Day the booking was made
	Date3    string `gorm:"column:date3" json:"-"`          // date2, JS-month encoding
	Time     string `json:"time"`                           // HH:MM slot; empty = not yet assigned
	SK       string `json:"sk"`                             // Clinic zip
	VetName  string `gorm:"column:vetname" json:"vetname"`  // Vet member ID
	PName    string `gorm:"column:pname" json:"pname"`      // Pet name / free-text description
	Owner    string `json:"owner"`                          // Owner personal ID
	OwnerN   string `gorm:"column:ownern" json:"ownern"`    // Owner name
	TPName   string `gorm:"column:operation" json:"tpname"` // Procedure name
	Koment   string `gorm:"column:coment" json:"koment"`    // Notes
	TP       string `json:"tp,omitempty"`                   // Legacy type code
	Price    string `json:"price,omitempty"`
	Sax      string `json:"-"`                   // pet sex, denormalised
	Species  string `gorm:"column:pet" json:"-"` // pet species, denormalised

	Status string `gorm:"-" json:"status,omitempty"`
	Phone  string `gorm:"-" json:"phone,omitempty"`
}

func (Appointment) TableName() string { return "operationdate" }
