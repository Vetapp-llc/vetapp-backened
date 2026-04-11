package models

// Price maps to the prices table.
type Price struct {
	ID    uint   `gorm:"primaryKey" json:"id"`
	Name  string `json:"name"`   // Service name
	Price string `json:"price"`  // Price in GEL
	SK    string `gorm:"column:zip" json:"sk"` // Clinic zip
}

func (Price) TableName() string { return "prices" }
