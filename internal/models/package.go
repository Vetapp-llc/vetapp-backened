package models

// Package maps to the mprice table (subscription packages).
type Package struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `json:"name"`
	Price    string `json:"price"`
	Duration int    `json:"duration"` // days
	// AppleProductID matches the App Store Connect product identifier
	// (e.g. com.vetapp.subscription.monthly). The Apple IAP verify
	// flow asserts the receipt's productId equals this value, so a
	// receipt for a cheaper SKU can't be presented to activate a more
	// expensive plan. Empty during rollout — after backfill, treat as
	// fail-closed.
	AppleProductID string `gorm:"column:apple_product_id" json:"apple_product_id,omitempty"`
}

func (Package) TableName() string { return "mprice" }
