package handlers

import (
	"net/http"
	"strings"
)

// Procedure form catalogue.
//
// The `vaccination` table is polymorphic: what each column means depends
// on the procedure type (tp) and, for vaccines, tests, deworming and
// ectoparasite treatment, on the species — the PHP clinic portal has a
// separate form per species (vet/addvac.php dog, addvac1.php cat,
// addvac2.php other, and likewise addtest/adddeh/addecto). This file is
// the single description of those forms, transcribed from the PHP pages.
// It drives the clinic web form (GET /procedures/forms), the procedure
// display for owners, and the columns Create accepts.
//
// Column names are the real `vaccination` columns. Several are reused
// for unrelated data on some forms — e.g. on the dog test `ser` holds the
// CCov result, `address` Giardia and `sax` Ehrlichia canis (see
// vet/view2.php) — so never infer a column's meaning without the tp.

// FormField is one input on a procedure form.
type FormField struct {
	Column string `json:"column" validate:"required"`
	Label  string `json:"label" validate:"required"`
	// Kind: text | textarea | select | result (positive/negative) | date | money
	Kind  string `json:"kind" validate:"required"`
	Group string `json:"group,omitempty"` // panel heading for grouped test results
	// Options for a select. With DependsOn set, OptionsBy holds one list
	// per value of that field (vaccine brand by vaccine type).
	Options   []string            `json:"options,omitempty"`
	DependsOn string              `json:"depends_on,omitempty"`
	OptionsBy map[string][]string `json:"options_by,omitempty"`
	Required  bool                `json:"required,omitempty"`
}

// ProcedureForm is the form for one procedure type and species.
type ProcedureForm struct {
	TP      int         `json:"tp" validate:"required"`
	Name    string      `json:"name" validate:"required"`
	Species string      `json:"species,omitempty"` // dog | cat | other; empty = any
	Fields  []FormField `json:"fields" validate:"required"`
}

// Test results are stored as the Georgian words the PHP radios post.
const (
	resultNegative = "უარყოფითი"
	resultPositive = "დადებითი"
)

// otherOption is PHP's "სხვა" (other) choice; the companion free-text
// field then holds the product name.
const otherOption = "სხვა"

var (
	fComment  = FormField{Column: "coment", Label: "კომენტარი", Kind: "textarea"}
	fPrice    = FormField{Column: "price", Label: "ღირებულება", Kind: "money"}
	fNextDate = FormField{Column: "date2", Label: "შემდეგი ვიზიტი", Kind: "date"}
	fAnam     = FormField{Column: "anam", Label: "ანამნეზი", Kind: "textarea"}
	fDiagn    = FormField{Column: "diagn", Label: "დიაგნოზი", Kind: "textarea"}
	fNout     = FormField{Column: "nout", Label: "მკურნალობა (ვეტ)", Kind: "textarea"}
	fKoment   = FormField{Column: "koment", Label: "შენიშვნა (ვეტ)", Kind: "textarea"}
	fDani     = FormField{Column: "dani", Label: "დანიშნულება", Kind: "textarea"}
	fSerial   = FormField{Column: "ser", Label: "სერია", Kind: "text"}
)

func result(col, label, group string) FormField {
	return FormField{Column: col, Label: label, Kind: "result", Group: group}
}

// Vaccine types (vaccination.vac) and brands (vacn) per type.
const (
	vacTypeComplex    = "კომპლექსური ვაქცინა"
	vacTypeRabies     = "ცოფის საწინააღმდეგო ვაქცინა"
	vacTypeKennel     = "ვოლიერული ხველის საწინააღმდეგო ვაქცინა"
	vacTypeAntifungal = "ანტიმიკოზური ვაქცინა"
)

var rabiesBrands = []string{"Nobivac Rabies", "Biocan R", "Rabisin", "Defensor 3", "PRO-VACTM Rabies-Fc", "Canvac R", "CaniShot® Rabies Cavac"}

var procedureForms = []ProcedureForm{
	// ---- vaccination (tp 1) ----
	{TP: 1, Name: "ვაქცინაცია", Species: "dog", Fields: []FormField{
		{Column: "vac", Label: "ვაქცინის ტიპი", Kind: "select", Required: true,
			Options: []string{vacTypeComplex, vacTypeRabies, vacTypeKennel, vacTypeAntifungal}},
		{Column: "vacn", Label: "პრეპარატი", Kind: "select", Required: true, DependsOn: "vac", OptionsBy: map[string][]string{
			vacTypeComplex: {"Eurican DHPPi2-L", "Nobivac DHPPi+L", "Nobivac Puppy DP", "Vanguard plus 5/CV-L", "Biocan DHPPi+L",
				"Biocan Novel DHPPi/L4", "Biocan Novel DHPPi/L4R", "Biocan Novel Puppy", "Biocan Puppy", "Vencomax 12", "Cancav DHP",
				"Hexacanivac", "PRO-VAC DHPPL", "Hipradog  7", "Hirpadog DHLP", "Duramune Max 5/4L"},
			vacTypeRabies:     rabiesBrands,
			vacTypeKennel:     {"Nobivac KC"},
			vacTypeAntifungal: {"Biocan M plus", "Vacderm ", "Polivak-TM"},
		}},
		fSerial, fComment, fPrice, fNextDate,
	}},
	{TP: 1, Name: "ვაქცინაცია", Species: "cat", Fields: []FormField{
		{Column: "vac", Label: "ვაქცინის ტიპი", Kind: "select", Required: true,
			Options: []string{vacTypeComplex, vacTypeRabies, vacTypeAntifungal}},
		{Column: "vacn", Label: "პრეპარატი", Kind: "select", Required: true, DependsOn: "vac", OptionsBy: map[string][]string{
			vacTypeComplex:    {"NobivacR Tricat Trio", "Biofel PCH", "Felocell 4"},
			vacTypeRabies:     rabiesBrands,
			vacTypeAntifungal: {"Biocan M plus", "Vacderm", "Polivak-TM"},
		}},
		fSerial, fComment, fPrice, fNextDate,
	}},
	{TP: 1, Name: "ვაქცინაცია", Species: "other", Fields: []FormField{
		{Column: "vac", Label: "ვაქცინის დასახელება", Kind: "text", Required: true},
		fSerial, fComment, fPrice, fNextDate,
	}},

	// ---- tests (tp 2 dog, 22 cat, 222 other) ----
	{TP: 2, Name: "ტესტი", Species: "dog", Fields: []FormField{
		result("vacn", "Leishmania", ""),
		result("deh", "Canine Babesia", ""),
		result("vac1", "GiarDia duodenalis", ""),
		result("vac2", "Canine distemper", ""),
		result("vac3", "Heartworm", "Caniv4"),
		result("vac4", "Lyme", "Caniv4"),
		result("vac5", "Anaplasma", "Caniv4"),
		result("vac6", "E.Canis", "Caniv4"),
		result("test1", "Ehrlichia", "Caniv 4DX"),
		result("test2", "Babesia", "Caniv 4DX"),
		result("test3", "Anaplasma", "Caniv 4DX"),
		result("test4", "Heartworm", "Caniv 4DX"),
		result("test5", "CDV Ag", "CDV/CAV Ag Combo"),
		result("test6", "ACAV-II Ag", "CDV/CAV Ag Combo"),
		result("test7", "cCRP Ag", ""),
		result("test8", "RLN Test", ""),
		result("vac7", "CPV", "CPV/CCov/Giardia"),
		result("ser", "CCov", "CPV/CCov/Giardia"),
		result("address", "Giardia", "CPV/CCov/Giardia"),
		result("sax", "Erlichia Canis", ""),
		fAnam, fNout, fKoment, fComment, fDani, fPrice,
	}},
	{TP: 22, Name: "ტესტი", Species: "cat", Fields: []FormField{
		result("vac", "Feline leukemia (FeLV)", ""),
		result("deh", "Feline immunodeficiency (FIV)", ""),
		result("vac7", "FPV", ""),
		result("ser", "FCOV", ""),
		result("vac6", "Giardia", ""),
		result("vac1", "GiarDia", ""),
		result("vac2", "Toxoplasmosis", ""),
		result("vacn", "Feline infectious peritonitis", ""),
		fAnam, fNout, fKoment, fComment, fDani, fPrice,
	}},
	{TP: 222, Name: "ტესტი", Species: "other", Fields: []FormField{
		{Column: "vac", Label: "ტესტის დასახელება", Kind: "text", Required: true},
		{Column: "ser", Label: "შედეგი", Kind: "text"},
		fAnam, fNout, fKoment, fComment, fDani, fPrice,
	}},

	// ---- dehelminization (tp 12) ----
	{TP: 12, Name: "დეჰელმინთიზაცია", Species: "dog", Fields: dehelFields()},
	{TP: 12, Name: "დეჰელმინთიზაცია", Species: "cat", Fields: dehelFields()},
	{TP: 12, Name: "დეჰელმინთიზაცია", Species: "other", Fields: []FormField{
		{Column: "vac", Label: "პრეპარატი", Kind: "text", Required: true}, fComment, fPrice, fNextDate,
	}},

	// ---- ectoparasite prevention (tp 11) ----
	{TP: 11, Name: "ექტოპარაზიტების პრევენცია", Species: "dog", Fields: ectoFields(
		[]string{"Frontline(TRI-ACT)", "Advantix", "Bars", "Chistotel", "K9 Advantix II", "Dana stop-on", "Lega", otherOption},
		[]string{"NexGuard(SPECTRA)", "Bravecto", otherOption},
		[]string{"Scalibor", "Foresto", "Kiltix", "Bars", "Beaphar", "Bio", "Protecto", "Dana", otherOption},
		[]string{"Frontline(SPRAY)", "Bloxnet", "Chistotel", "Insektol", otherOption},
	)},
	{TP: 11, Name: "ექტოპარაზიტების პრევენცია", Species: "cat", Fields: ectoFields(
		[]string{"Frontline(Combo)", "Advocate", "Bars", "Advantage", "Chistotel", otherOption},
		[]string{otherOption},
		[]string{"Foresto", "Beaphar", "Bio", "Protecto", otherOption},
		[]string{"Frontline", "Bloxnet", "Chistotel", "Insektol", "Solpreme", otherOption},
	)},
	{TP: 11, Name: "ექტოპარაზიტების პრევენცია", Species: "other", Fields: []FormField{
		{Column: "vac", Label: "პრეპარატი", Kind: "text", Required: true}, fComment, fPrice, fNextDate,
	}},

	// ---- single-form procedures ----
	{TP: 110, Name: "სტერილიზაცია/კასტრაცია", Fields: []FormField{
		{Column: "vac", Label: "ტიპი", Kind: "select", Required: true, Options: []string{"კასტრაცია", "სტერილიზაცია"}},
		fComment, fDani, fPrice,
	}},
	{TP: 115, Name: "მიკროჩიპი", Fields: []FormField{
		{Column: "chip", Label: "ჩიპის ნომერი", Kind: "text", Required: true}, fPrice,
	}},
	{TP: 116, Name: "ლაბორატორია", Fields: []FormField{fComment, fPrice}},
}

func dehelFields() []FormField {
	return []FormField{
		{Column: "deh", Label: "პრეპარატი", Kind: "select",
			Options: []string{"Drontal", "Caniverm", "Brovanol", "Cestal Plus", "Caniquantel Plus", otherOption}},
		{Column: "vac", Label: "პრეპარატის დასახელება", Kind: "text"},
		fComment, fPrice, fNextDate,
	}
}

// ectoFields: each product kind is a dropdown plus a free-text name
// (used with "სხვა"), in the column pairs vet/addecto.php posts.
func ectoFields(drops, pills, collars, sprays []string) []FormField {
	return []FormField{
		{Column: "vac1", Label: "ანტიპარაზიტული წვეთები", Kind: "select", Group: "წვეთები", Options: drops},
		{Column: "vac", Label: "პრეპარატის დასახელება", Kind: "text", Group: "წვეთები"},
		{Column: "vac3", Label: "ანტიპარაზიტული აბები", Kind: "select", Group: "აბები", Options: pills},
		{Column: "vac2", Label: "პრეპარატის დასახელება", Kind: "text", Group: "აბები"},
		{Column: "vac5", Label: "ანტიპარაზიტული საყელო", Kind: "select", Group: "საყელო", Options: collars},
		{Column: "vac4", Label: "პრეპარატის დასახელება", Kind: "text", Group: "საყელო"},
		{Column: "vac7", Label: "ანტიპარაზიტული სპრეი", Kind: "select", Group: "სპრეი", Options: sprays},
		{Column: "vac6", Label: "პრეპარატის დასახელება", Kind: "text", Group: "სპრეი"},
		fComment, fPrice, fNextDate,
	}
}

// genericProcedureTPs use vet/addprocedure.php?tp=…: a name plus the
// clinical note fields.
var genericProcedureTPs = []int{108, 106, 202, 203, 104, 102, 105, 109, 101, 103, 107}

func init() {
	for _, tp := range genericProcedureTPs {
		procedureForms = append(procedureForms, ProcedureForm{TP: tp, Name: procedureNameForTP(tp), Fields: []FormField{
			{Column: "vac", Label: "პროცედურის დასახელება", Kind: "text"},
			fAnam, fDiagn, fNout, fKoment, fComment, fDani, fPrice,
		}})
	}
}

// speciesKey maps pets.pet (ძაღლი / კატა / სხვა) onto a form species.
func speciesKey(pet string) string {
	switch strings.TrimSpace(pet) {
	case "ძაღლი":
		return "dog"
	case "კატა":
		return "cat"
	default:
		return "other"
	}
}

// formFor returns the form for a tp and species, or nil.
func formFor(tp int, species string) *ProcedureForm {
	var anySpecies *ProcedureForm
	for i := range procedureForms {
		f := &procedureForms[i]
		if f.TP != tp {
			continue
		}
		if f.Species == species {
			return f
		}
		if f.Species == "" {
			anySpecies = f
		}
	}
	return anySpecies
}

// testFormFor returns the test form for a test tp (2, 22, 222).
func testFormFor(tp int) *ProcedureForm {
	switch tp {
	case 2:
		return formFor(2, "dog")
	case 22:
		return formFor(22, "cat")
	case 222:
		return formFor(222, "other")
	}
	return nil
}

// Forms returns the procedure forms for one species — what the clinic
// web app renders when recording a visit.
// @Summary Procedure forms for a species
// @Tags procedures
// @Produce json
// @Security BearerAuth
// @Param species query string false "dog | cat | other (or the Georgian species name)"
// @Success 200 {array} ProcedureForm
// @Router /procedures/forms [get]
func (h *ProcedureHandler) Forms(w http.ResponseWriter, r *http.Request) {
	species := r.URL.Query().Get("species")
	switch species {
	case "dog", "cat", "other":
	default:
		species = speciesKey(species)
	}
	out := make([]ProcedureForm, 0, len(procedureForms))
	for _, f := range procedureForms {
		if f.Species == "" || f.Species == species {
			out = append(out, f)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
