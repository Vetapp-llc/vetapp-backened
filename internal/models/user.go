package models

import "time"

// User maps to the memberlogin_members table.
// group_id: 1=owner, 2=vet, 4=admin
type User struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	FirstName   string     `gorm:"column:first_name" json:"first_name"`
	LastName    string     `gorm:"column:last_name" json:"last_name"`       // Georgian personal ID
	Email       string     `json:"email"`
	Phone       string     `json:"phone"`
	Zip         string     `json:"zip"`                                      // Clinic code
	GroupID     int        `gorm:"column:group_id" json:"group_id"`          // 1=owner, 2=vet, 4=admin
	Password    []byte     `gorm:"type:bytea" json:"-"`                      // AES encrypted, never exposed in JSON
	CompanyName string     `gorm:"column:company_name" json:"company_name"`
	LastLogin   *time.Time `gorm:"column:last_login" json:"last_login"`
	Status      string     `gorm:"default:T" json:"status"`
}

func (User) TableName() string { return "memberlogin_members" }

// Role constants
const (
	RoleOwner = 1
	RoleVet   = 2
	RoleAdmin = 4
)

// IsOwner returns true if the user is a pet owner.
func (u *User) IsOwner() bool { return u.GroupID == RoleOwner }

// IsVet returns true if the user is a vet staff member.
func (u *User) IsVet() bool { return u.GroupID == RoleVet }

// IsAdmin returns true if the user is a super admin.
func (u *User) IsAdmin() bool { return u.GroupID == RoleAdmin }
