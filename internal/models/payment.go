package models

// Payment maps to the paymethod table.
type Payment struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	UUID   string `json:"uuid"`      // Pet ID
	Date   string `json:"date"`      // Payment date
	Method string `json:"method"`    // "card" or "cash"
	Amount string `json:"amount"`    // Amount in GEL
	SK     string `json:"sk"`        // Clinic zip
	VetID  string `gorm:"column:vet_id" json:"vet_id"` // Vet who processed
	Owner  string `json:"owner"`     // Owner personal ID
}

func (Payment) TableName() string { return "paymethod" }
