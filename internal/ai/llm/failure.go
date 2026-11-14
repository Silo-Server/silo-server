package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
)

// ErrUnusableResponse marks a provider reply that arrived but cannot be
// used, such as a transcription without timed segments.
var ErrUnusableResponse = errors.New("AI endpoint returned an unusable response")

// unusableError is a provider reply that cannot be used, with its own message.
type unusableError struct{ message string }

func (e *unusableError) Error() string        { return e.message }
func (e *unusableError) Is(target error) bool { return target == ErrUnusableResponse }

func unusable(message string) error { return &unusableError{message: message} }

// FailureKind groups AI endpoint failures by what a person can do about
// them. Callers map a kind to copy for their own audience; Error() text of
// these errors carries provider-controlled details and must not be shown.
type FailureKind int

const (
	// FailureUnknown is any error this package cannot attribute to the
	// provider.
	FailureUnknown FailureKind = iota
	// FailureQuotaExhausted means the provider needs a billing change.
	FailureQuotaExhausted
	// FailurePaymentRequired is an HTTP 402 from the provider.
	FailurePaymentRequired
	// FailureUnauthorized is an HTTP 401: the API key was rejected.
	FailureUnauthorized
	// FailureForbidden is an HTTP 403: the key lacks access.
	FailureForbidden
	// FailureNotFound is an HTTP 404: unknown endpoint or model.
	FailureNotFound
	// FailureTooLarge is an HTTP 413: the request body was too large.
	FailureTooLarge
	// FailureRateLimited is an HTTP 429 that outlasted the retries.
	FailureRateLimited
	// FailureUnavailable is an HTTP 5xx that outlasted the retries.
	FailureUnavailable
	// FailureRejected is any other HTTP 4xx.
	FailureRejected
	// FailureTimeout means a deadline expired before the provider answered.
	FailureTimeout
	// FailureUnreachable means the request never got an HTTP response.
	FailureUnreachable
	// FailureUnusableResponse means the provider answered with content that
	// could not be used.
	FailureUnusableResponse
)

// Classify reports which kind of provider failure err represents. A
// provider status wins over a deadline joined onto it, so a rate limit that
// ran into a caller deadline still reads as a rate limit.
func Classify(err error) FailureKind {
	if err == nil {
		return FailureUnknown
	}
	if errors.Is(err, ErrQuotaExhausted) {
		return FailureQuotaExhausted
	}
	if errors.Is(err, ErrPlanIneligible) {
		return FailureForbidden
	}
	if status, ok := errors.AsType[*HTTPError](err); ok {
		switch code := status.StatusCode; {
		case code == http.StatusPaymentRequired:
			return FailurePaymentRequired
		case code == http.StatusUnauthorized:
			return FailureUnauthorized
		case code == http.StatusForbidden:
			return FailureForbidden
		case code == http.StatusNotFound:
			return FailureNotFound
		case code == http.StatusRequestEntityTooLarge:
			return FailureTooLarge
		case code == http.StatusTooManyRequests:
			return FailureRateLimited
		case code >= 500:
			return FailureUnavailable
		case code >= 400:
			return FailureRejected
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout
	}
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return FailureTimeout
	}
	if _, ok := errors.AsType[*url.Error](err); ok {
		return FailureUnreachable
	}
	if errors.Is(err, ErrUnusableResponse) {
		return FailureUnusableResponse
	}
	return FailureUnknown
}
