package models

// Appointment maps to the operationdate table.
//
// Real schema is:
//
//	id, uuid, date, date2, date3, operation, sk, pname, owner, ownern,
//	coment, tp, price, sax, pet, vetname, time
//
// The previous definition declared `phone`, `tpname`, `koment` and
// `status`, none of which exist, so every INSERT died with SQLSTATE
// 42703 — clinics could not book an appointment at all, and the owner
// visits screen had nothing to show.
//
// Mapping notes:
//   - `operation` is the procedure name ("ვაქცინაცია", "ქირურგია"),
//     exposed as TPName to match how the rest of the codebase names it.
//   - `coment` (single 'm') is the legacy spelling for notes.
//   - There is no status column. Status is derived from the date:
//     a visit in the future is upcoming, one in the past has happened.
//     Status is kept as a Go-only field so existing callers compile,
//     but it is never persisted.
//   - There is no phone column either; the owner's phone lives on
//     memberlogin_members and is reachable via `owner`.
type Appointment struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	UUID    string `json:"uuid"`                           // Pet ID
	Date    string `json:"date"`                           // Appointment date
	Time    string `json:"time"`                           // Time slot
	SK      string `json:"sk"`                             // Clinic zip
	VetName string `gorm:"column:vetname" json:"vetname"`  // Vet member ID
	PName   string `gorm:"column:pname" json:"pname"`      // Pet name / free-text description
	Owner   string `json:"owner"`                          // Owner personal ID
	OwnerN  string `gorm:"column:ownern" json:"ownern"`    // Owner name
	TPName  string `gorm:"column:operation" json:"tpname"` // Procedure name
	Koment  string `gorm:"column:coment" json:"koment"`    // Notes
	TP      string `json:"tp,omitempty"`                   // Legacy type code
	Price   string `json:"price,omitempty"`

	// Not persisted — operationdate has no status or phone column.
	Status string `gorm:"-" json:"status,omitempty"`
	Phone  string `gorm:"-" json:"phone,omitempty"`
}

func (Appointment) TableName() string { return "operationdate" }
