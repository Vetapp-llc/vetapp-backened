package models

// Package maps to the mprice table (subscription packages).
type Package struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `json:"name"`
	Price    string `json:"price"`
	Duration int    `json:"duration"` // days
}

func (Package) TableName() string { return "mprice" }
