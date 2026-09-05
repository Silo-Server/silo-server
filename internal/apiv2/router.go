package apiv2

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

// Prefix is the path every v2 operation lives under. Operations register with
// their full path because the API listener hands the subtree over with a
// wildcard Handle, not a Mount, so the inner router sees the whole URL.
const Prefix = "/api/v2"

// DelegationPattern is the API listener's registration that hands the v2
// subtree to this package.
const DelegationPattern = Prefix + "/*"

// APIMajor is the contract major this package serves.
const APIMajor = 2

// MaxJSONBodyBytes is the default structured request-body cap, enforced
// before the body is decoded. An operation may lower it; raising it needs an
// explicit limit, rationale and boundary tests in that operation's contract
// ledger (docs/architecture/api-contract.md, "HTTP representation").
const MaxJSONBodyBytes int64 = 1 << 20

// BodyReadTimeout is the structured-body read deadline. It mirrors the
// server's 30 s ReadTimeout baseline rather than Huma's 5 s default.
// Ratified on #135, 2026-09-02.
const BodyReadTimeout = 30 * time.Second

func init() {
	// Response slices and maps are never null on the wire; builders
	// initialize them (see Collection) and the schema says so.
	huma.DefaultArrayNullable = false
	installErrorAdapter()
}

// Dependencies is the runtime wiring the v2 listener composes onto
// operations. Every field is optional: a missing gate never removes a route,
// it makes the operations behind that gate fail closed with a typed problem.
type Dependencies struct {
	// Auth is the bearer/API-key gate shared with the v1 router.
	Auth *apimw.AuthMiddleware
	// ViewerAccess resolves the declared profile into a viewer scope.
	ViewerAccess *apimw.ViewerAccessMiddleware
	// ActingAdmin is the admin-through-primary-profile gate.
	ActingAdmin func(http.Handler) http.Handler
	// PermissionGates maps a permission name (policy.Permission* constants)
	// to the gate that enforces it.
	PermissionGates map[string]func(http.Handler) http.Handler
	// DemoSettings reads the demo.enabled setting; nil means demo mode is
	// never on.
	DemoSettings apimw.DemoSettingsReader
	// RateLimit is the generic authenticated-route limiter.
	RateLimit func(http.Handler) http.Handler
	// CursorSecret keys pagination cursors. It must be shared by every replica
	// (the JWT secret is); empty means a per-process random key.
	CursorSecret []byte

	// bodyReadTimeout overrides BodyReadTimeout; tests use it to exercise the
	// 408 boundary without waiting for the production deadline.
	bodyReadTimeout time.Duration
	// testRegister registers probe operations; tests only.
	testRegister func(*Registry)
}

// sealedHandler is what NewHandler hands out: the finished router behind an
// unexported field and a ServeHTTP method, nothing else, so no assertion or
// type switch recovers a registration surface from it (see
// docs/architecture/api-contract.md, "Legacy native route inventory"). Do not
// embed http.Handler here: embedding exports the field and promotes its
// methods.
type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

// NewHandler builds the v2 listener. It returns a sealed http.Handler, never
// the chi surface; the route inventory checks this shape.
func NewHandler(deps Dependencies) http.Handler {
	return sealedHandler{h: newChiRouter(deps)}
}

// newChiRouter is the v2 listener's registration surface. The route inventory
// walks this function: the router is handed to the Huma adapter exactly once,
// and every operation registered through the Registry is described by
// contracts/api/v2/openapi.json rather than by an inventory row.
func newChiRouter(deps Dependencies) chi.Router {
	r := chi.NewRouter()
	r.Use(requestID)
	r.Use(observe)
	r.Use(dropDelegationPattern)
	r.Use(bufferResponse)
	r.NotFound(notFound)

	api := humachi.New(r, humaConfig())
	api.UseMiddleware(observeOperation, defaultHeaders, classGate(deps), observeIdentity, normalizeAccept, mediaTypeGuard, queryGuard)

	reg := &Registry{api: api, deps: deps}
	registerAll(reg)
	if deps.testRegister != nil {
		deps.testRegister(reg)
	}
	// After registration: the 405 answer names the methods the registry
	// declared for the matched path, which is only known once every operation
	// is in.
	r.MethodNotAllowed(reg.methodNotAllowed)
	return r
}

// humaConfig is the framework configuration the contract ratifies. Nothing
// here is a Huma default accepted implicitly; each choice is locked by a test
// in router_test.go.
func humaConfig() huma.Config {
	return huma.Config{
		OpenAPI: &huma.OpenAPI{
			OpenAPI: "3.1.0",
			Info:    &huma.Info{Title: "Silo API", Version: fmt.Sprintf("%d", APIMajor)},
			Components: &huma.Components{
				Schemas: huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer),
				SecuritySchemes: map[string]*huma.SecurityScheme{
					securitySchemeBearer: {
						Type:        "http",
						Scheme:      "bearer",
						Description: "A session token or an API key in the Authorization header. The server decides which it holds.",
					},
				},
			},
		},
		// Built-in spec, docs and schema routes are disabled: Silo serves the
		// committed artifact itself (getOpenAPIDocument) and nothing else.
		OpenAPIPath: "",
		DocsPath:    "",
		SchemasPath: "",
		// The "json" alias is what Huma's marshaller resolves the
		// application/problem+json suffix to; normalizeAccept keeps a bare
		// `Accept: json` from selecting it.
		Formats:       map[string]huma.Format{mediaTypeJSON: huma.DefaultJSONFormat, "json": huma.DefaultJSONFormat},
		DefaultFormat: mediaTypeJSON,
		// An unacceptable Accept is 406, never a silent fallback.
		NoFormatFallback: true,
		// An undeclared query parameter is a validation failure.
		RejectUnknownQueryParameters: true,
		// The schema-link transformer is absent: no $schema member, no links.
		Transformers: []huma.Transformer{problemTransformer},
	}
}

// requestID exposes the canonical request ID. Under the API listener,
// apimw.RequestID has already stored a server-generated one in the context;
// it is reused verbatim so the request log, activity log and the problem
// `instance` all name the same request. Standalone (tests), one is minted
// with the same generator. A client-supplied X-Request-Id is never adopted
// anywhere in either chain.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chimw.GetReqID(r.Context())
		if id == "" {
			id = apimw.NewRequestID()
			r = r.WithContext(context.WithValue(r.Context(), chimw.RequestIDKey, id))
		}
		// Set through the map to keep the contract's spelling (X-Request-ID);
		// Header.Set would canonicalize it.
		w.Header()[RequestIDHeader] = []string{id}
		next.ServeHTTP(w, r)
	})
}

// dropDelegationPattern removes the API listener's wildcard pattern from the
// chi route context so the request logger and activity log see the operation
// path, not "/api/v2/*" joined with it.
func dropDelegationPattern(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			if n := len(rctx.RoutePatterns); n > 0 && rctx.RoutePatterns[n-1] == DelegationPattern {
				rctx.RoutePatterns = rctx.RoutePatterns[:n-1]
			}
		}
		next.ServeHTTP(w, r)
	})
}

// bufferResponse holds the response until the operation has finished, then
// writes it in one go. That is what makes three contract rules hold:
//
//   - a panic anywhere, including Huma's own marshal failure after the status
//     would already have been sent, becomes the internal_error problem with no
//     detail leakage;
//   - a success body is never written once the client has gone away (net/http
//     cancels the request context on disconnect);
//   - a problem — including the 408 for a body-read timeout, which net/http
//     also reports as a canceled context — is still delivered.
//
// Structured responses are bounded JSON documents, so buffering them is
// cheap; raw media and streams are not served through Huma.
func bufferResponse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bw := &bufferedWriter{w: w, ctx: r.Context()}
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
					panic(rec)
				}
				slog.ErrorContext(r.Context(), "apiv2 handler panic",
					"component", "apiv2",
					"request_id", requestIDFrom(r.Context()),
					"panic", fmt.Sprint(rec),
					"stack", string(debug.Stack()))
				bw.discard()
				writeProblem(bw, r, NewProblem(TypeInternalError, "An unexpected error occurred."))
			}
			bw.flush()
		}()
		next.ServeHTTP(bw, r)
	})
}

type bufferedWriter struct {
	w       http.ResponseWriter
	ctx     context.Context
	status  int
	body    bytes.Buffer
	flushed bool
}

func (b *bufferedWriter) Header() http.Header { return b.w.Header() }

func (b *bufferedWriter) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

// Unwrap lets huma.SetReadDeadline and http.ResponseController reach the
// connection.
func (b *bufferedWriter) Unwrap() http.ResponseWriter { return b.w }

// discard drops whatever the failed handler produced, headers included,
// except the request ID.
func (b *bufferedWriter) discard() {
	b.status = 0
	b.body.Reset()
	h := b.w.Header()
	id := h[RequestIDHeader] //nolint:staticcheck // the contract spells the header X-Request-ID; the map key is set the same way
	for k := range h {
		delete(h, k)
	}
	if len(id) > 0 {
		h[RequestIDHeader] = id
	}
}

func (b *bufferedWriter) flush() {
	if b.flushed {
		return
	}
	b.flushed = true
	if b.status == 0 {
		b.status = http.StatusOK
	}
	if b.ctx.Err() != nil && b.status < 400 {
		// The client is gone; a success body has nobody to read it.
		return
	}
	b.w.WriteHeader(b.status)
	_, _ = b.w.Write(b.body.Bytes())
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, NewProblem(TypeNotFound, "No operation is registered at this path."))
}

// methodNotAllowed answers a matched path with an unsupported method. chi does
// not hand the allowed set to a custom handler, so it is recomputed from the
// registry's declared rows: RFC 9110 requires Allow on a 405.
func (reg *Registry) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	p := NewProblem(TypeMethodNotAllowed, "The method is not supported at this path.")
	if allow := strings.Join(reg.AllowedMethods(r.URL.Path), ", "); allow != "" {
		p = p.WithHeader("Allow", allow)
	}
	writeProblem(w, r, p)
}
