package models

// Subscription maps to the payments_ipay table.
type Subscription struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	UUID      string `json:"uuid"`       // Pet ID
	Amount    string `json:"amount"`     // Payment amount
	Status    string `json:"status"`     // Payment status
	OrderID   string `gorm:"column:order_id" json:"order_id"`
	TransID   string `gorm:"column:trans_id" json:"trans_id"`
	Date      string `json:"date"`       // Payment date
	Package   string `json:"package"`    // Package type
}

func (Subscription) TableName() string { return "payments_ipay" }
