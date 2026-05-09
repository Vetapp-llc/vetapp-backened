package models

import "time"

// EmailVerificationToken represents a single-use token sent to a user's
// email address. The link in the email points at
// `GET /api/auth/email/verify/confirm?token=<Token>`; tapping the link
// flips `users.email_verified = true` and marks the token used.
//
// Lifecycle:
//
//   1. SendEmailVerification creates a row with a fresh UUID token and
//      a 24h ExpiresAt.
//   2. The user clicks the link → ConfirmEmailVerification looks up
//      the row, validates ExpiresAt > now AND UsedAt IS NULL, sets
//      UsedAt = now, flips users.email_verified = true.
//
// Multiple outstanding tokens for the same user are allowed (e.g. user
// clicked "resend" twice) — only the latest one matters because the
// confirm step is idempotent against `users.email_verified`.
type EmailVerificationToken struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	UserID    uint       `gorm:"column:user_id;not null;index" json:"user_id"`
	// Token is the opaque string that travels in the email link.
	// 36-char UUID gives ~122 bits of entropy — well past brute-force
	// resistance for a 24h TTL.
	Token     string     `gorm:"column:token;not null;uniqueIndex;size:64" json:"token"`
	ExpiresAt time.Time  `gorm:"column:expires_at;not null" json:"expires_at"`
	UsedAt    *time.Time `gorm:"column:used_at" json:"used_at,omitempty"`
	CreatedAt time.Time  `gorm:"column:created_at;not null" json:"created_at"`
}

// TableName explicitly names the table since GORM's default
// pluralisation would produce `email_verification_tokens` anyway —
// declared here so a future rename is a one-line change.
func (EmailVerificationToken) TableName() string { return "email_verification_tokens" }
