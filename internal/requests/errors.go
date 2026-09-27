package requests

import "errors"

var (
	ErrInvalidMediaType = errors.New("invalid media type")
	ErrInvalidInput     = errors.New("invalid request input")
	ErrRequestsDisabled = errors.New("requests are disabled")
	ErrUserBlocked      = errors.New("user is blocked from requesting")
	ErrQuotaExceeded    = errors.New("request quota exceeded")
	ErrAlreadyAvailable = errors.New("media is already available")
	ErrAlreadyRequested = errors.New("media is already requested")
	ErrNotFound         = errors.New("request not found")
	ErrForbidden        = errors.New("request forbidden")
	ErrInvalidState     = errors.New("invalid request state")
	// ErrIntegrationUnreachable reports that the configured request integration
	// (its plugin, or the service behind it) could not be reached. It is a
	// dependency failure, not a bug in the request, so the API layer answers an
	// upstream-unavailable status instead of a generic internal error.
	ErrIntegrationUnreachable = errors.New("request integration is unreachable")
)

type QuotaError struct {
	Used       int
	Limit      int
	WindowDays int
}

func (e QuotaError) Error() string {
	return ErrQuotaExceeded.Error()
}

func (e QuotaError) Unwrap() error {
	return ErrQuotaExceeded
}

// ValidationError carries plugin Validate results back to the API layer.
type ValidationError struct {
	FieldErrors map[string]string
	FormError   string
}

func (e *ValidationError) Error() string {
	if e.FormError != "" {
		return e.FormError
	}
	return "validation failed"
}
