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
