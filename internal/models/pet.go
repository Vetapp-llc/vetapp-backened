package models

// Pet maps to the pets table.
type Pet struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	UUID      string `json:"uuid"`                              // Owner personal ID (FK to user.last_name)
	Name      string `json:"name"`                              // Pet name
	Pet       string `json:"pet"`                               // Species: ძაღლი, კატა, სხვა
	Sex       string `json:"sex"`
	Variety   string `json:"variety"`                           // Breed
	Chip      string `json:"chip"`                              // Microchip number
	Date      string `json:"date"`                              // DOB as string (YYYY-MM-DD)
	Vet       string `json:"vet"`                               // Clinic zip code
	Status    int    `json:"status"`                            // 1=active, 2+=inactive
	Code      string `json:"code"`                              // 4-digit access code
	Phone     string `json:"phone"`                             // Owner phone (denormalized)
	Email     string `json:"email"`                             // Owner email (denormalized)
	FirstName string `gorm:"column:first_name" json:"first_name"` // Owner name (denormalized)
	Color     string `json:"color"`
	Cast      string `json:"cast"`                              // Neutering type
	Birth2    string `gorm:"column:birth2" json:"birth2"`       // Subscription expiry
	PetStatus string `gorm:"column:petStatus" json:"petStatus"` // INHABITANT, ADOPTED, WORKMATE
}

func (Pet) TableName() string { return "pets" }
