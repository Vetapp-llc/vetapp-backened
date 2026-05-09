package models

// Procedure maps to the legacy `vaccination` table (universal medical
// records — vaccinations, tests, ectoparasite, dehel, surgery, every
// specialty). The `TP` field determines what `Vac*` and `Test*` columns
// mean. See `internal/handlers/procedures.go:Types()` for the canonical
// tp → category map.
//
// Important: column semantics are *polymorphic* on tp. Examples:
//   - tp=1   (vaccination) → Vac=type, VacN=brand, Ser=batch
//   - tp=11  (ectoparasite) → Vac+Vac1 = drops, Vac2+Vac3 = pills, etc.
//   - tp=12  (dehel) → Deh=drug-from-list, Vac=custom-drug-text
//   - tp=2/22/222 (test) → VacN/Deh/Vac1..Vac6/Test1..Test8 = panel results
//   - tp=10x..20x (generic) → Vac=name, Vac1=anamnesis, Vac2=diagnosis, Vac3=treatment
//
// Always dispatch on TP before interpreting these fields.
type Procedure struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	UUID    string `json:"uuid"`                          // Pet ID (as string)
	Date    string `json:"date"`                          // Procedure date
	Date2   string `json:"date2"`                         // Next due date
	Date3   string `json:"date3"`                         // SMS reminder trigger date (= date2 - 1 month)
	TP      int    `json:"tp"`                            // Procedure type code
	TPName  string `gorm:"column:tpname" json:"tpname"`   // Procedure type display name
	Vac     string `json:"vac"`                           // Vaccine/procedure name OR tp=11 drops-custom OR tp=12 custom drug
	VacN    string `gorm:"column:vacn" json:"vacn"`       // Brand OR tp=2 Leishmania result
	SK      string `json:"sk"`                            // Clinic zip
	Phone   string `json:"phone"`                         // Payment status: "0"=unpaid, "1"=paid
	Price   string `json:"price"`                         // Price in GEL
	PName   string `gorm:"column:pname" json:"pname"`     // Pet name (denormalized)
	Owner   string `json:"owner"`                         // Owner personal ID
	OwnerN  string `gorm:"column:ownern" json:"ownern"`   // Owner name (denormalized)
	VetName string `gorm:"column:vetname" json:"vetname"` // Vet member ID (numeric string) — empty/0 means owner-added
	Anam    string `json:"anam"`                          // Anamnesis
	Diagn   string `json:"diagn"`                         // Diagnosis
	Nout    string `json:"nout"`                          // Treatment
	Koment  string `json:"koment"`                        // Vet-internal notes
	Coment  string `json:"coment"`                        // Comment visible to owner
	Dani    string `json:"dani"`                          // Prescription
	Ser     string `json:"ser"`                           // Vaccine serial / batch number
	Deh     string `json:"deh"`                           // Dehel drug-from-list (tp=12) OR Canine Babesia result (tp=2)

	// Polymorphic content slots. For tp=11 (ecto): drops/pills/collar/
	// spray with custom-text in Vac/Vac2/Vac4/Vac6 and dropdown choice
	// in Vac1/Vac3/Vac5/Vac7. For tp=2/22/222: test panel results. For
	// tp=10x/20x (generic): Vac1=anamnesis, Vac2=diagnosis, Vac3=treatment.
	// Vac8/Vac9 are unused in production data (empty in all rows we've
	// inspected) but kept here for legacy compatibility.
	Vac1 string `json:"vac1"`
	Vac2 string `json:"vac2"`
	Vac3 string `json:"vac3"`
	Vac4 string `json:"vac4"`
	Vac5 string `json:"vac5"`
	Vac6 string `json:"vac6"`
	Vac7 string `json:"vac7"`
	Vac8 string `json:"vac8"`
	Vac9 string `json:"vac9"`

	// Extra test-result columns specific to dog tests (tp=2). PHP form
	// at vet/addtest.php uses these for the Caniv 4DX panel + CDV/CAV
	// test slots. Other tps leave them empty.
	Test1 string `json:"test1"`
	Test2 string `json:"test2"`
	Test3 string `json:"test3"`
	Test4 string `json:"test4"`
	Test5 string `json:"test5"`
	Test6 string `json:"test6"`
	Test7 string `json:"test7"`
	Test8 string `json:"test8"`

	// Denormalized identity / housekeeping columns the legacy table also
	// stores. Mostly redundant with Pet but the PHP forms set them on
	// every save and some queries depend on them.
	Address string `json:"address"`
	Company string `json:"company"`            // payment method label e.g. "ბარათი", "ნაღდი"
	Sax     string `json:"sax"`                // pet sex denormalized
	Pn      string `gorm:"column:pn" json:"pn"`// owner personal-id copy
	Name    string `json:"name"`               // edit-log message column (NOT the pet name — that's PName)
}

func (Procedure) TableName() string { return "vaccination" }
