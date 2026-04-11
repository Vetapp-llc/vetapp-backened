package models

// Procedure maps to the vaccination table (universal medical records).
// The tp field determines the procedure type (1=vaccination, 2=test, 3=dehel, etc.)
type Procedure struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	UUID    string `json:"uuid"`                               // Pet ID (as string)
	Date    string `json:"date"`                               // Procedure date
	Date2   string `json:"date2"`                              // Next due date
	Date3   string `json:"date3"`                              // Reminder date
	TP      int    `json:"tp"`                                 // Procedure type code
	TPName  string `gorm:"column:tpname" json:"tpname"`        // Procedure type name
	Vac     string `json:"vac"`                                // Vaccine/procedure name
	VacN    string `gorm:"column:vacn" json:"vacn"`            // Brand
	SK      string `json:"sk"`                                 // Clinic zip
	Phone   string `json:"phone"`                              // Payment status: "0"=unpaid, "1"=paid
	Price   string `json:"price"`                              // Price in GEL
	PName   string `gorm:"column:pname" json:"pname"`          // Pet name (denormalized)
	Owner   string `json:"owner"`                              // Owner personal ID
	OwnerN  string `gorm:"column:ownern" json:"ownern"`        // Owner name (denormalized)
	VetName string `gorm:"column:vetname" json:"vetname"`      // Vet member ID
	Anam    string `json:"anam"`                               // Anamnesis
	Diagn   string `json:"diagn"`                              // Diagnosis
	Nout    string `json:"nout"`                               // Treatment
	Koment  string `json:"koment"`                             // Vet notes (internal)
	Coment  string `json:"coment"`                             // Comment (visible to owner)
	Dani    string `json:"dani"`                               // Prescription
	Ser     string `json:"ser"`                                // Vaccine serial/batch number
	Deh     string `json:"deh"`                                // Dehel drug / test result (Canine Babesia)

	// Test result fields
	Vac1 string `json:"vac1"`
	Vac2 string `json:"vac2"`
	Vac3 string `json:"vac3"`
	Vac4 string `json:"vac4"`
	Vac5 string `json:"vac5"`
	Vac6 string `json:"vac6"`
	Vac7 string `json:"vac7"`
	Vac8 string `json:"vac8"`
	Vac9 string `json:"vac9"`
}

func (Procedure) TableName() string { return "vaccination" }
