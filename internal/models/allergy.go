package models

// Allergy maps to the eals table.
type Allergy struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	UUID string `json:"uuid"`  // Pet ID
	Name string `json:"name"`  // Allergy/disease name
	Date string `json:"date"`  // Date recorded
	SK   string `json:"sk"`    // Clinic zip
}

func (Allergy) TableName() string { return "eals" }
