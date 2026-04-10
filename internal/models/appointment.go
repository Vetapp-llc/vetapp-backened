package models

// Appointment maps to the operationdate table.
type Appointment struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	UUID     string `json:"uuid"`      // Pet ID
	Date     string `json:"date"`      // Appointment date
	Time     string `json:"time"`      // Time slot
	SK       string `json:"sk"`        // Clinic zip
	VetName  string `gorm:"column:vetname" json:"vetname"` // Vet ID
	PName    string `gorm:"column:pname" json:"pname"`     // Pet name
	Owner    string `json:"owner"`     // Owner personal ID
	OwnerN   string `gorm:"column:ownern" json:"ownern"`   // Owner name
	Phone    string `json:"phone"`     // Owner phone
	TPName   string `gorm:"column:tpname" json:"tpname"`   // Procedure type
	Koment   string `json:"koment"`    // Notes
	Status   string `json:"status"`    // Appointment status
}

func (Appointment) TableName() string { return "operationdate" }
