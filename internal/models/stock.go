package models

import "time"

// ShopProduct is one product in a clinic's stock (migration 020).
type ShopProduct struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Zip       string    `json:"-"`
	Name      string    `json:"name"`
	Quantity  int       `json:"quantity"`
	UnitPrice string    `gorm:"type:numeric(12,2)" json:"unitPrice"`
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

func (ShopProduct) TableName() string { return "shop_products" }

// ShopSaleItem links a sale (a shop row) to the product and units it
// took from stock, so deleting the sale can return them.
type ShopSaleItem struct {
	ShopID    uint `gorm:"primaryKey;autoIncrement:false"`
	ProductID uint
	Quantity  int
}

func (ShopSaleItem) TableName() string { return "shop_sale_items" }
