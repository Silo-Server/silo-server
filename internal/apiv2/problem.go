package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// ProblemTypeOrigin is the project-controlled documentation origin every
// problem `type` URI lives under. The final path segment of a type URI is the
// machine-readable problem identifier (docs/architecture/api-contract.md,
// "Problem Details").
const ProblemTypeOrigin = "https://siloserver.org/docs/api/v2/problems/"

// ProblemType is one entry of the shared problem catalog.
type ProblemType struct {
	// ID is the stable identifier and the final path segment of the type URI.
	ID string
	// Status is the HTTP status the type always uses.
	Status int
	// Title is the short summary fixed for the type.
	Title string
}

// URI is the stable `type` member.
func (t ProblemType) URI() string { return ProblemTypeOrigin + t.ID }

// The shared type catalog. Domain-specific types are added only when a client
// needs distinct corrective behavior.
var (
	TypeMalformedRequest                              = ProblemType{"malformed_request", http.StatusBadRequest, "Malformed request"}
	TypeInvalidCursor                                 = ProblemType{"invalid_cursor", http.StatusBadRequest, "Invalid cursor"}
	TypeAuthenticationRequired                        = ProblemType{"authentication_required", http.StatusUnauthorized, "Authentication required"}
	TypeInvalidToken                                  = ProblemType{"invalid_token", http.StatusUnauthorized, "Invalid token"}
	TypeSessionExpired                                = ProblemType{"session_expired", http.StatusUnauthorized, "Session expired"}
	TypePermissionDenied                              = ProblemType{"permission_denied", http.StatusForbidden, "Permission denied"}
	TypeProfileVerificationRequired                   = ProblemType{"profile_verification_required", http.StatusForbidden, "Profile verification required"}
	TypeNotFound                                      = ProblemType{"not_found", http.StatusNotFound, "Not found"}
	TypeMethodNotAllowed                              = ProblemType{"method_not_allowed", http.StatusMethodNotAllowed, "Method not allowed"}
	TypeNotAcceptable                                 = ProblemType{"not_acceptable", http.StatusNotAcceptable, "Not acceptable"}
	TypeRequestTimeout                                = ProblemType{"request_timeout", http.StatusRequestTimeout, "Request timeout"}
	TypeConflict                                      = ProblemType{"conflict", http.StatusConflict, "Conflict"}
	TypeIdempotencyConflict                           = ProblemType{"idempotency_conflict", http.StatusConflict, "Idempotency conflict"}
	TypeJobNotCancelable                              = ProblemType{"job_not_cancelable", http.StatusConflict, "Job not cancelable"}
	TypeCapabilityDisabled                            = ProblemType{"capability_disabled", http.StatusConflict, "Capability disabled"}
	TypeCapabilityNotConfigured                       = ProblemType{"capability_not_configured", http.StatusConflict, "Capability not configured"}
	TypePreconditionFailed                            = ProblemType{"precondition_failed", http.StatusPreconditionFailed, "Precondition failed"}
	TypePayloadTooLarge                               = ProblemType{"payload_too_large", http.StatusRequestEntityTooLarge, "Payload too large"}
	TypeUnsupportedMediaType                          = ProblemType{"unsupported_media_type", http.StatusUnsupportedMediaType, "Unsupported media type"}
	TypeValidationFailed                              = ProblemType{"validation_failed", http.StatusUnprocessableEntity, "Validation failed"}
	TypePreconditionRequired                          = ProblemType{"precondition_required", http.StatusPreconditionRequired, "Precondition required"}
	TypeRateLimited                                   = ProblemType{"rate_limited", http.StatusTooManyRequests, "Rate limited"}
	TypeInternalError                                 = ProblemType{"internal_error", http.StatusInternalServerError, "Internal error"}
	TypeCapabilityUnsupported                         = ProblemType{"capability_unsupported", http.StatusNotImplemented, "Capability unsupported"}
	TypeDependencyUnavailable                         = ProblemType{"dependency_unavailable", http.StatusServiceUnavailable, "Dependency unavailable"}
	TypeClientUpgradeRequired                         = ProblemType{"client_upgrade_required", http.StatusGone, "Client upgrade required"}
	catalog                                           = []ProblemType{TypeMalformedRequest, TypeInvalidCursor, TypeAuthenticationRequired, TypeInvalidToken, TypeSessionExpired, TypePermissionDenied, TypeProfileVerificationRequired, TypeNotFound, TypeMethodNotAllowed, TypeNotAcceptable, TypeRequestTimeout, TypeConflict, TypeIdempotencyConflict, TypeJobNotCancelable, TypeCapabilityDisabled, TypeCapabilityNotConfigured, TypePreconditionFailed, TypePayloadTooLarge, TypeUnsupportedMediaType, TypeValidationFailed, TypePreconditionRequired, TypeRateLimited, TypeInternalError, TypeCapabilityUnsupported, TypeDependencyUnavailable, TypeClientUpgradeRequired}
	defaultTypeByStatus                               = map[int]ProblemType{}
	problemContentType                                = "application/problem+json"
	_                               error             = (*Problem)(nil)
	_                               huma.StatusError  = (*Problem)(nil)
	_                               huma.HeadersError = (*Problem)(nil)
)

func init() {
	for _, t := range catalog {
		if _, taken := defaultTypeByStatus[t.Status]; !taken {
			defaultTypeByStatus[t.Status] = t
		}
	}
}

// Catalog lists every shared problem type.
func Catalog() []ProblemType { return append([]ProblemType(nil), catalog...) }

// TypeForStatus is the catalog type a bare status maps to: the first type
// declared for that status, or internal_error for a status the catalog does
// not name.
func TypeForStatus(status int) ProblemType {
	if t, ok := defaultTypeByStatus[status]; ok {
		return t
	}
	return TypeInternalError
}

// ProblemError is one entry of a validation problem's `errors` array. It
// never carries the rejected value.
type ProblemError struct {
	Location string `json:"location" doc:"Where the error occurred: body.*, query.*, path.* or header.*"`
	Code     string `json:"code" doc:"Stable machine-readable code for the failure"`
	Detail   string `json:"detail" doc:"Safe human-readable explanation; never the rejected value"`
}

// Problem is the RFC 9457 Problem Details envelope every v2 error uses.
// Every member the contract requires is required here; `errors` is present
// only on validation problems.
type Problem struct {
	Type     string         `json:"type" format:"uri" doc:"Stable problem type URI; the final segment is the problem identifier"`
	Title    string         `json:"title" doc:"Short summary fixed for the problem type"`
	Status   int            `json:"status" doc:"HTTP status code, equal to the response status"`
	Detail   string         `json:"detail" doc:"Safe occurrence-specific explanation; not for control flow"`
	Instance string         `json:"instance" format:"uri" doc:"urn:silo:request:<request-id>, matching the X-Request-ID response header"`
	Errors   []ProblemError `json:"errors,omitempty" doc:"Field-level validation details"`

	headers http.Header
}

// NewProblem builds an application problem of type t.
func NewProblem(t ProblemType, detail string) *Problem {
	return &Problem{Type: t.URI(), Title: t.Title, Status: t.Status, Detail: detail}
}

// WithErrors attaches validation details.
func (p *Problem) WithErrors(errs ...ProblemError) *Problem {
	p.Errors = append(p.Errors, errs...)
	return p
}

// WithHeader adds a response header (Retry-After, Location, ...).
func (p *Problem) WithHeader(name, value string) *Problem {
	if p.headers == nil {
		p.headers = http.Header{}
	}
	p.headers.Add(name, value)
	return p
}

// WithRetryAfter adds `Retry-After` in delta-seconds, the form the contract
// mandates for 429 and applicable 503 responses.
func (p *Problem) WithRetryAfter(seconds int) *Problem {
	if seconds < 1 {
		seconds = 1
	}
	return p.WithHeader("Retry-After", strconv.Itoa(seconds))
}

// Error satisfies error.
func (p *Problem) Error() string { return p.Title + ": " + p.Detail }

// GetStatus satisfies huma.StatusError.
func (p *Problem) GetStatus() int { return p.Status }

// GetHeaders satisfies huma.HeadersError.
func (p *Problem) GetHeaders() http.Header {
	if p.headers == nil {
		return http.Header{}
	}
	return p.headers
}

// ContentType satisfies huma.ContentTypeFilter: problems are always
// application/problem+json.
func (p *Problem) ContentType(string) string { return problemContentType }

// RequestIDHeader is the response header carrying the canonical request ID.
// It is written through the header map, not Header.Set, because the contract
// spells it X-Request-ID and Go would canonicalize it to X-Request-Id.
const RequestIDHeader = "X-Request-ID"

// mediaTypeJSON is the one structured representation v2 speaks.
const mediaTypeJSON = "application/json"

// requestInstance renders the problem `instance` for a request ID.
func requestInstance(requestID string) string {
	return "urn:silo:request:" + requestID
}

// requestIDFrom reads the canonical request ID the foundation middleware
// stored. The chi key is reused so every existing log site keeps reading it.
func requestIDFrom(ctx context.Context) string {
	return chimw.GetReqID(ctx)
}

// fromHumaError is the error adapter: it maps a Huma-generated failure
// (status, message and huma.ErrorDetail list) onto the Silo envelope with a
// catalog type, stable codes and no echoed values.
func fromHumaError(requestID string, status int, msg string, errs []error, bodyLimit int64) *Problem {
	t := humaStatusType(status, msg)
	p := NewProblem(t, humaDetail(t, msg, bodyLimit))
	p.Instance = requestInstance(requestID)
	if status != http.StatusUnprocessableEntity && status != http.StatusBadRequest {
		// Only validation-shaped failures carry field details; a 406 or 413
		// has nothing field-level to say and Huma's message would leak.
		return p
	}
	for _, err := range errs {
		if err == nil {
			continue
		}
		var detail *huma.ErrorDetail
		if !errors.As(err, &detail) {
			// An application error wrapped as a detail: keep its text, which
			// application code is responsible for keeping safe.
			p.Errors = append(p.Errors, ProblemError{Location: locationBody, Code: codeInvalid, Detail: err.Error()})
			continue
		}
		if bodyParseFailure(status, detail) {
			// Every way a body fails to parse is one stable code; the parser's
			// own text names the offset and quotes the input.
			p.Errors = append(p.Errors, ProblemError{Location: locationBody, Code: codeMalformedJSON, Detail: "The request body is not valid JSON."})
			continue
		}
		p.Errors = append(p.Errors, ProblemError{
			Location: validationLocation(detail),
			Code:     validationCode(detail.Message),
			Detail:   detail.Message,
		})
	}
	return p
}

// bodyParseFailure reports whether a detail is the decoder refusing the body
// rather than a schema violation: Huma reports it at "body" with the whole
// document as the status-400 failure.
func bodyParseFailure(status int, d *huma.ErrorDetail) bool {
	return status == http.StatusBadRequest && d.Location == locationBody
}

func humaStatusType(status int, msg string) ProblemType {
	switch status {
	case http.StatusBadRequest:
		return TypeMalformedRequest
	case http.StatusUnprocessableEntity:
		return TypeValidationFailed
	case http.StatusNotAcceptable:
		return TypeNotAcceptable
	case http.StatusRequestTimeout:
		return TypeRequestTimeout
	case http.StatusRequestEntityTooLarge:
		return TypePayloadTooLarge
	case http.StatusUnsupportedMediaType:
		return TypeUnsupportedMediaType
	case http.StatusMethodNotAllowed:
		return TypeMethodNotAllowed
	}
	_ = msg
	return TypeForStatus(status)
}

// humaDetail replaces Huma's message with a fixed safe detail per type so a
// framework message (which can embed limits or parser positions) never leaks.
func humaDetail(t ProblemType, msg string, bodyLimit int64) string {
	switch t.ID {
	case TypeValidationFailed.ID:
		return "The request did not pass validation; see errors."
	case TypeMalformedRequest.ID:
		return "The request could not be parsed."
	case TypeNotAcceptable.ID:
		return "No acceptable representation is available for this operation."
	case TypeRequestTimeout.ID:
		return "The request body was not received before the read deadline."
	case TypePayloadTooLarge.ID:
		return fmt.Sprintf("The request body exceeds the %d-byte limit.", bodyLimit)
	case TypeUnsupportedMediaType.ID:
		return "The request media type is not supported; send application/json."
	case TypeInternalError.ID:
		return "An unexpected error occurred."
	}
	if msg == "" {
		return t.Title
	}
	return msg
}

// Error codes carried in Problem.errors[].code. They are contract values
// (docs/architecture/api-contract.md, "Problem Details"); add, never rename.
const (
	codeInvalid          = "invalid"
	codeInvalidType      = "invalid_type"
	codeInvalidEnum      = "invalid_enum"
	codeRequired         = "required"
	codeOutOfRange       = "out_of_range"
	codeUnknownField     = "unknown_field"
	codeUnknownParameter = "unknown_parameter"
	codeMalformedJSON    = "malformed_json"

	locationBody = "body"
)

// validationCode maps Huma's validation messages onto stable codes. A message
// the table does not name is reported as "invalid" rather than as text a
// client would be tempted to parse.
func validationCode(msg string) string {
	switch {
	case strings.HasPrefix(msg, "unknown query parameter"):
		return codeUnknownParameter
	case strings.HasPrefix(msg, "unexpected property"):
		return codeUnknownField
	case strings.HasPrefix(msg, "expected required property"), strings.HasSuffix(msg, "parameter is missing"), msg == "request body is required":
		return codeRequired
	case strings.HasPrefix(msg, "expected value to be one of"):
		return codeInvalidEnum
	case strings.HasPrefix(msg, "expected number >"), strings.HasPrefix(msg, "expected number <"),
		strings.HasPrefix(msg, "expected length"), strings.HasPrefix(msg, "expected array length"):
		return codeOutOfRange
	case strings.HasPrefix(msg, "expected "), strings.HasPrefix(msg, "invalid "):
		return codeInvalidType
	}
	return codeInvalid
}

// validationLocation lifts the property a "required" message names into the
// location, so a missing body.name is reported at body.name like every other
// field-level failure rather than at its parent.
func validationLocation(d *huma.ErrorDetail) string {
	const prefix = "expected required property "
	if strings.HasPrefix(d.Message, prefix) {
		name := strings.TrimSuffix(strings.TrimPrefix(d.Message, prefix), " to be present")
		if name != "" && !strings.ContainsAny(name, " .[") {
			return d.Location + "." + name
		}
	}
	return d.Location
}

// installErrorAdapter replaces Huma's error constructors process-wide. The
// package owns the only Huma API in the binary, so the override is global by
// design; it is installed once from init.
func installErrorAdapter() {
	huma.NewErrorWithContext = func(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
		requestID := ""
		limit := MaxJSONBodyBytes
		if ctx != nil {
			requestID = requestIDFrom(ctx.Context())
			limit = operationBodyLimit(ctx.Operation())
		}
		return fromHumaError(requestID, status, msg, errs, limit)
	}
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		return fromHumaError("", status, msg, errs, MaxJSONBodyBytes)
	}
}

// operationBodyLimit is the body cap the operation declared, so a 413 names
// the limit the caller actually hit rather than the package default.
func operationBodyLimit(op *huma.Operation) int64 {
	if op == nil {
		return MaxJSONBodyBytes
	}
	if v, ok := op.Metadata[metaMaxBodyBytes].(int64); ok && v > 0 {
		return v
	}
	return MaxJSONBodyBytes
}

// problemTransformer completes an application problem on its way out: the
// instance is filled from the request, and the status is forced to the
// envelope's own. It is the only transformer configured; Huma's default
// schema-link transformer is deliberately absent.
func problemTransformer(ctx huma.Context, _ string, v any) (any, error) {
	p, ok := v.(*Problem)
	if !ok {
		return v, nil
	}
	if p.Instance == "" {
		p.Instance = requestInstance(requestIDFrom(ctx.Context()))
	}
	noteProblem(ctx.Context(), p)
	return p, nil
}

// writeProblem renders a problem straight to a ResponseWriter for the paths
// that never reach Huma (router fallbacks, recovered panics, denials).
func writeProblem(w http.ResponseWriter, r *http.Request, p *Problem) {
	if p.Instance == "" {
		p.Instance = requestInstance(requestIDFrom(r.Context()))
	}
	noteProblem(r.Context(), p)
	h := w.Header()
	for name, values := range p.GetHeaders() {
		for _, v := range values {
			h.Add(name, v)
		}
	}
	h.Set("Content-Type", problemContentType)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(p)
}
