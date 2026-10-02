package requests

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The editor fields a probe or save error can point at, and the Sonarr and
// Radarr plugin's capability sub-id.
const (
	fieldBaseURL    = "base_url"
	fieldAPIKey     = "api_key_ref"
	arrCapabilityID = "arr"
)

// Host-written messages for a request server the editor cannot use. The
// plugin's own error text comes from the HTTP client talking to the server
// (status lines, dial errors, response bodies) and never reaches the admin;
// these sentences are chosen by classifying it instead.
const (
	integrationAddressMessage = "Enter the server's address, like http://192.168.1.10:8989."
	integrationKeyMissing     = "Enter the server's API key."
	integrationKeyRejected    = "The server rejected this API key."
	integrationNotArr         = "That address answered, but not as Sonarr or Radarr. Check the URL, including a URL base such as /sonarr."
	integrationNotService     = "That address answered, but not as the expected server. Check the URL, including any URL base."
	integrationHTTPNotHTTPS   = "That port serves http, not https."
	integrationRefused        = "Nothing answered at that address. Check the host and port."
	integrationNoSuchHost     = "That host name couldn't be found."
	integrationTimedOut       = "The server didn't answer in time."
	integrationBadCertificate = "The server's HTTPS certificate wasn't accepted."
)

// IntegrationUnreachableError is a request server the host could not reach,
// with a host-written sentence saying why. It matches
// ErrIntegrationUnreachable. Detail is safe to show an admin; Error also
// carries the underlying cause, for logs only.
type IntegrationUnreachableError struct {
	Detail string
	Err    error
}

func (e *IntegrationUnreachableError) Error() string {
	parts := []string{ErrIntegrationUnreachable.Error()}
	if e.Detail != "" {
		parts = append(parts, e.Detail)
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	return strings.Join(parts, ": ")
}

func (e *IntegrationUnreachableError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrIntegrationUnreachable}
	}
	return []error{ErrIntegrationUnreachable, e.Err}
}

// normalizeIntegrationBaseURL turns what an admin typed into the address the
// plugin is given: http:// is assumed when no scheme is given, and a trailing
// slash is dropped. It refuses anything that is not a plain http(s) address.
func normalizeIntegrationBaseURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%w: base_url is required", ErrInvalidInput)
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: base_url must be an http or https address", ErrInvalidInput)
	}
	return parsed.Scheme + "://" + parsed.Host + strings.TrimRight(parsed.EscapedPath(), "/"), nil
}

// ProbeValidationError is a failed options probe the host classified into a
// field or form error for the v2 editor. It unwraps to the ValidationError, so
// v2 renders it like any other; the frozen v1 route recognizes it and keeps its
// original answer. A ValidationError the router returned itself is not one.
type ProbeValidationError struct {
	*ValidationError
}

func (e *ProbeValidationError) Unwrap() error { return e.ValidationError }

func probeValidation(ve *ValidationError) *ProbeValidationError {
	return &ProbeValidationError{ValidationError: ve}
}

// NormalizeIntegrationBaseURL is normalizeIntegrationBaseURL for the v2
// options probe and save, so the saved address is the one the probe used. A
// refused address is a field error on base_url. The frozen v1 routes do not
// call it.
func NormalizeIntegrationBaseURL(raw string) (string, error) {
	baseURL, err := normalizeIntegrationBaseURL(raw)
	if err != nil {
		return "", &ValidationError{FieldErrors: map[string]string{fieldBaseURL: integrationAddressMessage}}
	}
	return baseURL, nil
}

// sameIntegrationBaseURL reports whether two addresses name the same server,
// so a saved row written before normalization still matches its normalized
// form.
func sameIntegrationBaseURL(a, b string) bool {
	na, errA := normalizeIntegrationBaseURL(a)
	nb, errB := normalizeIntegrationBaseURL(b)
	if errA != nil || errB != nil {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return na == nb
}

var integrationHTTPStatus = regexp.MustCompile(`\bHTTP (\d{3})\b`)

// classifyIntegrationError turns a failed options probe into something the
// admin can act on. Errors the router already classifies (plugin validation
// results and the request-domain sentinels) pass through untouched. A message
// the plugin wrote itself as InvalidArgument or FailedPrecondition is shown as
// a form error. Anything else is matched against the plugin's transport text
// and answered with a fixed host sentence: a field error for a bad address or
// key, or an unreachable error with a detail.
func classifyIntegrationError(err error, capabilityID string) error {
	if err == nil {
		return nil
	}
	var validation *ValidationError
	if errors.As(err, &validation) {
		return err
	}
	for _, sentinel := range []error{
		ErrInvalidInput,
		ErrInvalidMediaType,
		ErrRequestsDisabled,
		ErrUserBlocked,
		ErrQuotaExceeded,
		ErrAlreadyAvailable,
		ErrAlreadyRequested,
		ErrNotFound,
		ErrForbidden,
		ErrInvalidState,
		ErrIntegrationUnreachable,
	} {
		if errors.Is(err, sentinel) {
			return err
		}
	}

	// The plugin's own status, even when the host wrapped it (status.FromError
	// would answer the whole wrapped text as the message).
	message := err.Error()
	var grpcErr interface{ GRPCStatus() *status.Status }
	if errors.As(err, &grpcErr) {
		st := grpcErr.GRPCStatus()
		switch st.Code() {
		case codes.InvalidArgument, codes.FailedPrecondition:
			if text := strings.TrimSpace(st.Message()); text != "" {
				return probeValidation(&ValidationError{FormError: text})
			}
		case codes.DeadlineExceeded:
			return &IntegrationUnreachableError{Detail: integrationTimedOut, Err: err}
		}
		message = st.Message()
	}
	lower := strings.ToLower(message)

	fieldError := func(field, text string) error {
		return probeValidation(&ValidationError{FieldErrors: map[string]string{field: text}})
	}
	notTheService := func() error {
		if strings.TrimSpace(capabilityID) == arrCapabilityID {
			return fieldError(fieldBaseURL, integrationNotArr)
		}
		return fieldError(fieldBaseURL, integrationNotService)
	}
	if match := integrationHTTPStatus.FindStringSubmatch(message); match != nil {
		switch match[1] {
		case "401", "403":
			return fieldError(fieldAPIKey, integrationKeyRejected)
		case "404":
			return notTheService()
		}
	}
	switch {
	// A web page where the API should be: Sonarr and Radarr serve their UI
	// for any unknown path, so a missing URL base answers HTML, not JSON. A
	// truncated or oddly shaped JSON body is not this.
	case strings.Contains(lower, "decode response") && strings.Contains(lower, "invalid character '<'"):
		return notTheService()
	case strings.Contains(lower, "server gave http response to https client"):
		return fieldError(fieldBaseURL, integrationHTTPNotHTTPS)
	case strings.Contains(lower, "unsupported protocol scheme"),
		strings.Contains(lower, "base url is required"):
		return fieldError(fieldBaseURL, integrationAddressMessage)
	case strings.Contains(lower, "api key is required"):
		return fieldError(fieldAPIKey, integrationKeyMissing)
	}

	detail := ""
	switch {
	case strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "no route to host"),
		strings.Contains(lower, "network is unreachable"):
		detail = integrationRefused
	case strings.Contains(lower, "no such host"):
		detail = integrationNoSuchHost
	case strings.Contains(lower, "timeout"),
		strings.Contains(lower, "deadline exceeded"):
		detail = integrationTimedOut
	case strings.Contains(lower, "x509"),
		strings.Contains(lower, "certificate"),
		strings.Contains(lower, "tls:"):
		detail = integrationBadCertificate
	}
	return &IntegrationUnreachableError{Detail: detail, Err: err}
}
