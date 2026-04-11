package handlers

// ErrorResponse is the standard error format.
type ErrorResponse struct {
	Error string `json:"error" validate:"required"`
}

// MessageResponse is the standard success message format.
type MessageResponse struct {
	Message string `json:"message" validate:"required"`
}
