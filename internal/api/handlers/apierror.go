package handlers

import "net/http"

// APIError is a handler decision the transport has not rendered yet: the v1
// handler writes it as {error, message} with Status, the v2 listener as the
// Problem Details type of Status. Extracting business logic behind this type
// is what lets one function serve both surfaces without changing v1 bytes.
type APIError struct {
	Status  int
	Code    string
	Message string
	// Field names the request member a 400 bad_request rejected, so the v2
	// listener can render it as a 422 validation problem at body.<Field>.
	Field string
	// cause is the underlying error for callers that branch on it.
	cause error
}

// Unwrap exposes the cause to errors.Is.
func (e *APIError) Unwrap() error { return e.cause }

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func apiError(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

func fieldError(field, message string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: message, Field: field}
}
