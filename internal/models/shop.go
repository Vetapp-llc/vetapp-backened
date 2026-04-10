package models

// Shop maps to the shop table (retail sales).
type Shop struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Name    string `json:"name"`    // Product name
	Price   string `json:"price"`   // Sale price
	Date    string `json:"date"`    // Sale date
	SK      string `json:"sk"`      // Clinic zip
	VetName string `gorm:"column:vetname" json:"vetname"` // Staff who sold
}

func (Shop) TableName() string { return "shop" }
