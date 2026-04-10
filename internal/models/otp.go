package models

import "time"

// OTP stores one-time password codes for phone verification.
type OTP struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Phone     string    `json:"phone"`
	Code      string    `json:"code"`
	Type      string    `json:"type"`       // "register" or "recovery"
	Used      bool      `json:"used"`
	ExpiresAt time.Time `gorm:"column:expires_at" json:"expires_at"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (OTP) TableName() string { return "otp_codes" }
