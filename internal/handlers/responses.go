package handlers

// ErrorResponse is the standard error format.
//
// Code is a stable machine-readable reason ("invalid_credentials",
// "otp_expired", ...) so clients can show a localized message instead
// of matching on the English text. RetryAfter (seconds) accompanies
// "rate_limited".
type ErrorResponse struct {
	Error      string `json:"error" validate:"required"`
	Code       string `json:"code,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
}

// MessageResponse is the standard success message format.
type MessageResponse struct {
	Message string `json:"message" validate:"required"`
}

// Machine-readable error codes returned in ErrorResponse.Code.
const (
	codeInvalidCredentials = "invalid_credentials"
	codeAccountDisabled    = "account_disabled"
	codeEmailTaken         = "email_taken"
	codePersonalIDTaken    = "personal_id_taken"
	codeInvalidPersonalID  = "invalid_personal_id"
	codeInvalidPhone       = "invalid_phone"
	codeOTPInvalid         = "otp_invalid"
	codeOTPExpired         = "otp_expired"
	codeRateLimited        = "rate_limited"
	codeUserNotFound       = "user_not_found"
	codeWrongPassword      = "wrong_password"
	codeIdentityUnverified = "identity_unverified"
)
