package models

// Subscription maps to the payments_ipay table (covers both iPay and Apple IAP).
type Subscription struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	UUID      string `json:"uuid"`                              // Pet ID
	Amount    string `json:"amount"`                            // Payment amount
	Status    string `json:"status"`                            // "pending", "success", "failed"
	OrderID   string `gorm:"column:order_id" json:"order_id"`   // iPay order ID or Apple transaction ID
	TransID   string `gorm:"column:trans_id" json:"trans_id"`   // iPay transaction ID
	Date      string `json:"date"`                              // Payment date
	Package   string `json:"package"`                           // Package name
	Provider  string `json:"provider"`                          // "ipay" or "apple"
	Receipt   string `json:"receipt,omitempty"`                  // Apple IAP receipt data
	ProductID string `gorm:"column:product_id" json:"product_id,omitempty"` // Apple IAP product ID
}

func (Subscription) TableName() string { return "payments_ipay" }
