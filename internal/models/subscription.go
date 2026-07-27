package models

// Subscription maps to the payments_ipay table (covers card gateways
// and Apple IAP).
//
// As with Payment and Shop, the legacy column names differ from the Go
// field names, and the previous definition also invented columns the
// table does not have. Real schema:
//
//	id, pet_id, price, currency, transaction_hash, transaction_order_id,
//	transaction_payment_id, transaction_payment_method,
//	transaction_card_type, transaction_pan, gc_transaction_id, status,
//	created_at, updated_at, provider, receipt, product_id, order_id,
//	trans_id
//
// So `uuid` → pet_id and `amount` → price, and there is no `date` or
// `package` column at all. Without the tags below GORM emitted those
// names and EVERY subscription insert failed with SQLSTATE 42703 —
// no purchase could be recorded through any provider, Apple IAP
// included.
//
// Package and Date are kept as Go-side fields because the checkout and
// callback flows pass them around (the callback looks the package up by
// name to compute an expiry), but they are `gorm:"-"` so they are never
// written to or read from the database. Which package a purchase
// relates to stays recoverable from price + product_id.
type Subscription struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	UUID      string `gorm:"column:pet_id" json:"uuid"`                     // Pet ID
	Amount    string `gorm:"column:price" json:"amount"`                    // Payment amount
	Status    string `json:"status"`                                        // "pending", "success", "failed"
	OrderID   string `gorm:"column:order_id" json:"order_id"`               // Gateway order ID / Apple transaction ID
	TransID   string `gorm:"column:trans_id" json:"trans_id"`               // Gateway transaction ID
	Provider  string `json:"provider"`                                      // "bog" | "ipay" | "apple"
	Receipt   string `json:"receipt,omitempty"`                             // Apple IAP receipt data
	ProductID string `gorm:"column:product_id" json:"product_id,omitempty"` // Apple IAP product ID
	Currency  string `json:"currency,omitempty"`                            // "GEL"

	// Not persisted — payments_ipay has no such columns.
	Package string `gorm:"-" json:"package,omitempty"`
	Date    string `gorm:"-" json:"date,omitempty"`
}

func (Subscription) TableName() string { return "payments_ipay" }
