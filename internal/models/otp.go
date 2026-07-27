package models

import "time"

// OTP stores one-time password codes for phone verification.
type OTP struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Phone     string    `json:"phone"`
	Code      string    `json:"code"`
	Type      string    `json:"type"`       // "register" or "recovery"
	Used      bool      `json:"used"`
	// Tries counts wrong-code attempts; we lock the OTP after 5 to
	// prevent brute-force against the 6-digit space within the 60s TTL.
	Tries     int       `gorm:"column:tries" json:"tries"`
	ExpiresAt time.Time `gorm:"column:expires_at" json:"expires_at"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (OTP) TableName() string { return "otp_codes" }
