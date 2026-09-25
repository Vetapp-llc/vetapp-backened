package models

// Allergy maps to `eals` — allergies and chronic diseases recorded by a
// clinic (vet/addeals.php) or by the owner.
//
// The PHP form writes the allergy itself to `vac` and the free-text note
// to `ser`; `name` is always empty in production. Reading `name` (as
// this model once did) showed every allergy with a blank title.
type Allergy struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	UUID    string `json:"uuid"`                      // Pet ID
	Name    string `gorm:"column:vac" json:"name"`    // Allergy / disease
	Comment string `gorm:"column:ser" json:"comment"` // Free-text note
	Date    string `json:"date"`                      // Date recorded
	SK      string `json:"sk"`                        // Clinic zip; empty = owner-entered
}

func (Allergy) TableName() string { return "eals" }
