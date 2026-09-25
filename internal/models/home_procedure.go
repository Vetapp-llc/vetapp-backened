package models

// HomeProcedure maps to `homepro` — a treatment the owner gives at home
// over a period, e.g. "tablet twice a day" (owner/addhomeprocedure.php).
//
// Legacy column semantics: puuid is the pet id, date the start date,
// date2 the owner's personal ID, and rao the period in days minus one
// (the PHP select posts 0 for "1 დღე").
type HomeProcedure struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	PetID      string `gorm:"column:puuid" json:"pet_id"`
	StartDate  string `gorm:"column:date" json:"start_date"`
	OwnerID    string `gorm:"column:date2" json:"-"`
	PeriodCode string `gorm:"column:rao" json:"-"`
	Name       string `gorm:"column:prname" json:"name"`
}

func (HomeProcedure) TableName() string { return "homepro" }

// HomeProcedureDone maps to `pro` — one row each time the owner marks a
// home procedure as done (calendar2/addprocedure.php).
type HomeProcedureDone struct {
	ID              uint   `gorm:"primaryKey" json:"id"`
	HomeProcedureID string `gorm:"column:pruuid" json:"-"`
	DoneAt          string `gorm:"column:gaketebuli" json:"done_at"` // "YYYY-MM-DD HH:MM"
}

func (HomeProcedureDone) TableName() string { return "pro" }
