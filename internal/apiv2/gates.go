package apiv2

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

// classGate composes the existing chi-style gates onto a Huma operation from
// the class it declared. It runs first among the API middlewares so nothing
// about an operation (media type, parameters, body) is judged before the
// caller is.
func classGate(deps Dependencies) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		class, _ := op.Metadata[metaClass].(Class)
		permission, _ := op.Metadata[metaPermission].(string)
		demoRestricted, _ := op.Metadata[metaDemoRestricted].(bool)
		profileOptional, _ := op.Metadata[metaProfileOptional].(bool)
		chain, missing := gateChain(deps, class, permission, demoRestricted, profileOptional)
		r, w := humachi.Unwrap(ctx)
		if missing != "" {
			// A gate the class needs is not wired. Fail closed with a typed
			// problem rather than serving the operation ungated.
			writeProblem(w, r, NewProblem(TypeDependencyUnavailable,
				"The server is not wired to authorize this operation ("+missing+" unavailable)."))
			return
		}
		runChain(ctx, next, chain)
	}
}

// gateChain is the ordered gate list for a class. The order is the v1
// router's authenticated group (internal/api/router.go): auth, then the demo
// guard, then the rate limiter, then viewer access, then the class gate. Demo
// mode denies before the limiter so a refused request never spends
// rate-limit budget, exactly as on v1. Viewer access runs for every class v1
// runs it for, so a PIN-locked or unknown profile is judged the same way on
// both surfaces. The second result names the first gate the wiring lacks.
func gateChain(deps Dependencies, class Class, permission string, demoRestricted, profileOptional bool) ([]func(http.Handler) http.Handler, string) {
	if class == ClassPublic {
		return nil, ""
	}
	if deps.Auth == nil {
		return nil, "auth"
	}
	chain := []func(http.Handler) http.Handler{deps.Auth.RequireAuth}
	if demoRestricted {
		chain = append(chain, demoGate(deps.DemoSettings))
	}
	if deps.RateLimit != nil {
		chain = append(chain, deps.RateLimit)
	}
	if class == ClassAuthenticated {
		// The v1 account-scoped groups (diagnostics, compat connect-info) stop
		// here too: they must work before a profile is chosen.
		return chain, ""
	}
	if deps.ViewerAccess == nil {
		return nil, "viewer access"
	}
	switch class {
	case ClassProfileScoped:
		chain = append(chain, deps.ViewerAccess.RequireViewerAccess)
		if !profileOptional {
			chain = append(chain, apimw.RequireProfile)
		}
	case ClassActingAdmin:
		if deps.ActingAdmin == nil {
			return nil, "acting admin"
		}
		chain = append(chain, deps.ViewerAccess.RequireViewerAccess, deps.ActingAdmin)
	case ClassPermissionGated:
		gate := deps.PermissionGates[permission]
		if gate == nil {
			return nil, "permission " + permission
		}
		chain = append(chain, deps.ViewerAccess.RequireViewerAccess, gate)
	}
	return chain, ""
}

// runChain runs chi-style middleware in front of the Huma continuation. A
// gate that denies writes the v1 JSON error shape; denialWriter captures it
// and re-renders it as the matching Problem Details document, so the gate's
// decision is reused verbatim and only the wire form changes.
func runChain(ctx huma.Context, next func(huma.Context), chain []func(http.Handler) http.Handler) {
	r, w := humachi.Unwrap(ctx)
	var h http.Handler = http.HandlerFunc(func(_ http.ResponseWriter, r2 *http.Request) {
		next(huma.WithContext(ctx, r2.Context()))
	})
	for i := len(chain) - 1; i >= 0; i-- {
		h = chain[i](h)
	}
	dw := &denialWriter{header: http.Header{}}
	h.ServeHTTP(dw, r)
	if dw.status != 0 {
		writeProblem(w, r, dw.problem())
	}
}

// demoGate is the v2 demo-mode rule: with demo.enabled set, a non-admin may
// not run an operation declared DemoRestricted. Read-only methods always
// pass, as in v1. A nil settings reader means demo mode cannot be on.
func demoGate(settings apimw.DemoSettingsReader) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if settings == nil || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			enabled, _ := settings.Get(r.Context(), "demo.enabled")
			if enabled != "true" { //nolint:goconst // settings literal, not a shared constant
				next.ServeHTTP(w, r)
				return
			}
			if claims := apimw.GetClaims(r.Context()); claims != nil && claims.Role == "admin" { //nolint:goconst // role literal owned by internal/auth
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", mediaTypeJSON)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"demo_restricted","message":"This action is not available in demo mode."}`))
		})
	}
}

// denialWriter buffers what a gate writes on denial. Nothing reaches the
// client until problem() has translated it.
type denialWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
	reason string
}

func (d *denialWriter) Header() http.Header { return d.header }

// RecordDenialReason satisfies apimw.DenialReasonRecorder: the gate names
// which denial this is, out of band, without changing its v1 response body.
func (d *denialWriter) RecordDenialReason(reason string) { d.reason = reason }

func (d *denialWriter) WriteHeader(status int) {
	if d.status == 0 {
		d.status = status
	}
}

func (d *denialWriter) Write(p []byte) (int, error) {
	if d.status == 0 {
		d.status = http.StatusOK
	}
	return d.body.Write(p)
}

// problem maps the v1 error body the gate wrote onto the Problem Details type
// with the same meaning. It switches on the body's machine-readable `error`
// code, and on the reason the gate recorded where one code covers denials v2
// must tell apart (internal/api/middleware, Reason* constants, pinned by
// TestDenialCodesAreStable there). A human message is never parsed.
// Retry-After is carried over; the legacy X-RateLimit-* fields are not part of
// v2 and are dropped.
func (d *denialWriter) problem() *Problem {
	var legacy struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(d.body.Bytes(), &legacy)
	var p *Problem
	switch legacy.Error {
	case "unauthorized":
		p = unauthorizedProblem(d.reason)
	case "profile_unverified":
		// silo-apple and silo-android branch on this to start PIN entry, so it
		// keeps a type of its own rather than collapsing into permission_denied.
		p = NewProblem(TypeProfileVerificationRequired,
			"The declared profile is locked; verify it and retry with X-Profile-Token.")
	case "forbidden", "demo_restricted":
		p = NewProblem(TypePermissionDenied, safeDetail(legacy.Message, "The caller is not permitted to perform this operation."))
	case "not_found":
		p = NewProblem(TypeNotFound, safeDetail(legacy.Message, "The requested resource does not exist."))
	case "rate_limit_exceeded":
		p = NewProblem(TypeRateLimited, "Too many requests; retry after the Retry-After delay.")
		if ra := d.header.Get("Retry-After"); ra != "" {
			p = p.WithHeader("Retry-After", ra)
		} else {
			p = p.WithRetryAfter(1)
		}
	case "bad_request":
		p = badRequestProblem(d.reason)
	case "internal_error":
		p = NewProblem(TypeInternalError, "An unexpected error occurred.")
	default:
		if d.status >= 500 || d.status == 0 {
			p = NewProblem(TypeInternalError, "An unexpected error occurred.")
		} else {
			p = NewProblem(TypeForStatus(d.status), http.StatusText(d.status))
		}
	}
	return p
}

// unauthorizedProblem picks the 401 type from the v1 reason. An unrecognized
// or absent reason is the conservative "authenticate again" answer.
func unauthorizedProblem(reason string) *Problem {
	switch reason {
	case apimw.ReasonSessionInvalid:
		return NewProblem(TypeSessionExpired, "The session is no longer valid; sign in again.")
	case apimw.ReasonInvalidCredential, apimw.ReasonAccountDisabled:
		// account_disabled shares invalid_token today: the credential no
		// longer authenticates anyone and the corrective action is the same.
		return NewProblem(TypeInvalidToken, "The credential is invalid or expired.")
	default:
		return NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
}

// badRequestProblem translates a v1 400 by the reason the gate declared. It
// never invents an error the gate did not raise: an unnamed reason is reported
// as a bare malformed request at the v1 status, with no field-level entry.
func badRequestProblem(reason string) *Problem {
	switch reason {
	case apimw.ReasonProfileHeaderRequired:
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "header.x-profile-id", Code: codeRequired, Detail: "The X-Profile-Id header is required."})
	case apimw.ReasonItemIDRequired:
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "path.id", Code: codeRequired, Detail: "The item id path parameter is required."})
	default:
		return NewProblem(TypeMalformedRequest, "The request could not be parsed.")
	}
}

// safeDetail keeps the gate's own explanation, falling back to a fixed
// sentence so every problem carries the detail the contract requires.
func safeDetail(message, fallback string) string {
	if strings.TrimSpace(message) == "" {
		return fallback
	}
	return message
}

// defaultHeaders applies the structured-response default: no-store unless the
// operation sets its own policy.
func defaultHeaders(ctx huma.Context, next func(huma.Context)) {
	ctx.SetHeader("Cache-Control", "no-store")
	next(ctx)
}

// normalizeAccept implements the contract's negotiation rule in front of
// Huma's exact-match negotiation: a missing Accept, `*/*`, `application/*` or
// an explicit application/json select JSON; anything else is the 406 problem.
// NoFormatFallback stays set so Huma agrees if a request reaches it anyway.
func normalizeAccept(ctx huma.Context, next func(huma.Context)) {
	r, w := humachi.Unwrap(ctx)
	accept := r.Header.Get("Accept")
	if accept != "" && !acceptsJSON(accept) {
		writeProblem(w, r, NewProblem(TypeNotAcceptable, "No acceptable representation is available for this operation."))
		return
	}
	r.Header.Set("Accept", mediaTypeJSON)
	next(ctx)
}

// acceptsJSON reports whether an Accept header admits application/json under
// RFC 9110 §12.5.1: the media range that matches JSON most specifically
// decides, so "application/json;q=0, */*" refuses JSON even though the
// wildcard would accept it, and "*/*;q=0, application/json" accepts it.
func acceptsJSON(accept string) bool {
	bestSpecificity, bestQ := 0, 0.0
	for _, part := range strings.Split(accept, ",") {
		fields := strings.Split(part, ";")
		mt := strings.ToLower(strings.TrimSpace(fields[0]))
		q := 1.0
		for _, param := range fields[1:] {
			param = strings.TrimSpace(param)
			if strings.HasPrefix(param, "q=") {
				if v, err := strconv.ParseFloat(param[2:], 64); err == nil {
					q = v
				}
			}
		}
		var specificity int
		switch mt {
		case mediaTypeJSON:
			specificity = 3
		case "application/*":
			specificity = 2
		case "*/*":
			specificity = 1
		default:
			continue
		}
		if specificity > bestSpecificity {
			bestSpecificity, bestQ = specificity, q
		}
	}
	return bestSpecificity > 0 && bestQ > 0
}

// mediaTypeGuard is the structured-body media-type rule: exactly
// application/json, with charset=utf-8 as the only tolerated parameter.
// Missing, vendor (+json), other charsets and any other parameter are the
// unsupported_media_type problem, and so is any non-identity
// Content-Encoding: compressed request bodies are rejected by default
// (docs/architecture/api-contract.md, "HTTP representation and lifecycle
// conventions"), before the body is read. Huma alone would treat a missing
// Content-Type as JSON, accept +json suffixes and ignore Content-Encoding
// entirely. The accepted media type is rewritten to its canonical lowercase
// form so this guard, not Huma's case-sensitive format table, is the single
// authority for the 415.
func mediaTypeGuard(ctx huma.Context, next func(huma.Context)) {
	r, w := humachi.Unwrap(ctx)
	if !contentEncodingOK(r.Header.Get("Content-Encoding")) {
		writeProblem(w, r, NewProblem(TypeUnsupportedMediaType,
			"Compressed request bodies are not supported; send an unencoded application/json body."))
		return
	}
	op := ctx.Operation()
	if op.RequestBody == nil {
		next(ctx)
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" && r.ContentLength == 0 && !op.RequestBody.Required {
		next(ctx)
		return
	}
	if !structuredMediaTypeOK(ct) {
		writeProblem(w, r, NewProblem(TypeUnsupportedMediaType, "The request media type is not supported; send application/json."))
		return
	}
	// charset=utf-8 is JSON's only encoding, so the canonical form carries
	// everything the accepted header said.
	r.Header.Set("Content-Type", mediaTypeJSON)
	next(ctx)
}

// contentEncodingOK reports whether a request body is unencoded. An absent
// header and an explicit `identity` (in any case, however many times) are the
// only accepted values.
func contentEncodingOK(enc string) bool {
	if strings.TrimSpace(enc) == "" {
		return true
	}
	for _, part := range strings.Split(enc, ",") {
		if strings.ToLower(strings.TrimSpace(part)) != "identity" {
			return false
		}
	}
	return true
}

func structuredMediaTypeOK(ct string) bool {
	parts := strings.Split(ct, ";")
	if strings.ToLower(strings.TrimSpace(parts[0])) != mediaTypeJSON {
		return false
	}
	for _, param := range parts[1:] {
		kv := strings.SplitN(strings.TrimSpace(param), "=", 2)
		if len(kv) != 2 || strings.ToLower(strings.TrimSpace(kv[0])) != "charset" || strings.ToLower(strings.Trim(kv[1], `"`)) != "utf-8" {
			return false
		}
	}
	return true
}
