package apiv2

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
)

// RawOperation describes a finite raw HTTP handshake in the native OpenAPI
// artifact. The owning transport enforces its declared path/query/header
// constraints and range, HEAD, conditional, and media authorization semantics.
// Structured JSON operations must use Register instead.
type RawOperation struct {
	Operation
	Protocol string
	Reason   string
	// WildcardParam names a terminal path parameter that captures nested paths.
	// OpenAPI retains {name}; Chi receives its corresponding /* route.
	WildcardParam string
}

// RawImpliedStatuses lists only failures of the shared authorization gates.
// Raw handlers declare their own protocol statuses; JSON negotiation and Huma
// input validation do not run on byte streams.
func RawImpliedStatuses(class Class, serviceBacked bool) []int {
	if class == ClassPublic {
		if serviceBacked {
			return []int{http.StatusServiceUnavailable}
		}
		return nil
	}
	statuses := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable}
	if resolvesProfile(class) {
		statuses = append(statuses, http.StatusNotFound, http.StatusUnprocessableEntity)
	}
	slices.Sort(statuses)
	return statuses
}

// RawSuccessStatus identifies a completed raw HTTP exchange. A redirect or a
// WebSocket upgrade does not need a fabricated 2xx response in its contract.
func RawSuccessStatus(method, protocol string, status int) bool {
	if status >= 200 && status < 300 {
		return true
	}
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	case http.StatusSwitchingProtocols:
		return method == http.MethodGet && protocol == "websocket"
	}
	return false
}

// RegisterRaw registers a GET/HEAD/POST raw handshake and its actual wire
// documentation. No Huma JSON validation, negotiation, encoding, or buffering
// runs here. Missing authorization dependencies still fail closed.
func RegisterRaw(reg *Registry, raw RawOperation, handler http.Handler) {
	op := raw.Operation
	fail := func(message string) { panic(fmt.Sprintf("apiv2: %s: %s", op.OperationID, message)) }
	if err := checkOperation(op); err != nil {
		fail(err.Error())
	}
	if handler == nil {
		fail("raw handler is required")
	}
	if op.Method != http.MethodGet && op.Method != http.MethodHead && op.Method != http.MethodPost {
		fail("raw registration currently supports GET, HEAD and POST handshakes")
	}
	if strings.TrimSpace(raw.Protocol) == "" || strings.TrimSpace(raw.Reason) == "" {
		fail("raw protocol and exclusion reason are required")
	}
	if op.MaxBodyBytes != 0 || op.BodyReadTimeout != 0 || op.RequestBody != nil || op.Guarded || op.CreateOnly || op.Conditional || op.Deprecation != nil || len(op.Errors) != 0 {
		fail("raw handlers must explicitly document and enforce protocol controls; structured body/concurrency/deprecation options are unsupported")
	}
	if len(op.Responses) == 0 {
		fail("raw response statuses and media types are required")
	}
	if raw.Protocol == "websocket" && (op.Method != http.MethodGet || op.Responses["101"] == nil) {
		fail("websocket requires GET with an explicit 101 response")
	}
	success := false
	for status, response := range op.Responses {
		code, err := strconv.Atoi(status)
		if err != nil || code < 100 || code > 599 || response == nil || response.Description == "" {
			fail("raw responses require explicit statuses and descriptions")
		}
		if code == http.StatusSwitchingProtocols {
			if op.Method != http.MethodGet || raw.Protocol != "websocket" || len(response.Content) != 0 {
				fail("101 requires a bodyless GET websocket handshake")
			}
			for _, name := range []string{"Connection", "Upgrade", "Sec-WebSocket-Accept"} {
				if !rawResponseHeader(response, name) {
					fail("websocket response must document header " + name)
				}
			}
		}
		if RawSuccessStatus(op.Method, raw.Protocol, code) {
			success = true
			if code >= 300 && !rawResponseHeader(response, "Location") {
				fail("redirect response must document Location")
			}
			for media := range response.Content {
				media = strings.ToLower(strings.TrimSpace(strings.SplitN(media, ";", 2)[0]))
				if media == mediaTypeJSON || strings.HasSuffix(media, "+json") {
					fail("JSON success responses must use Huma Register")
				}
			}
			if op.Method != http.MethodHead && code >= 200 && code < 300 && code != http.StatusNoContent && len(response.Content) == 0 {
				fail("raw response body requires its media type")
			}
		}
	}
	if !success {
		fail("raw success response is required")
	}
	// Refuse missing path documentation instead of inventing parameter schemas.
	for _, part := range strings.Split(op.Path, "/") {
		if !strings.HasPrefix(part, "{") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
		if !slices.ContainsFunc(op.Parameters, func(p *huma.Param) bool {
			return p != nil && p.In == "path" && p.Name == name && p.Required && p.Schema != nil
		}) {
			fail("raw path parameter must be explicitly documented: " + name)
		}
	}
	for _, declared := range reg.Declared() {
		if declared.Method == op.Method && declared.Path == rawRouterPath(op.Path, raw.WildcardParam) {
			fail("duplicate raw route")
		}
	}
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[metaClass] = op.Class
	op.Metadata[metaPermission] = op.Permission
	op.Metadata[metaDemoRestricted] = op.DemoRestricted
	op.Metadata[metaProfileOptional] = op.ProfileOptional
	op.Metadata[metaRateLimitBucket] = op.RateLimitBucket
	op.Metadata[metaRetrySafety] = string(op.RetrySafety)
	if op.Extensions == nil {
		op.Extensions = map[string]any{}
	}
	op.Extensions[extClass] = string(op.Class)
	if op.Permission != "" {
		op.Extensions[extPermission] = op.Permission
	}
	if op.ServiceBacked {
		op.Extensions[extServiceBacked] = true
	}
	if op.DemoRestricted {
		op.Extensions[extDemoRestricted] = true
	}
	op.Extensions["x-silo-raw-protocol"] = raw.Protocol
	op.Extensions["x-silo-raw-reason"] = raw.Reason
	if raw.WildcardParam != "" {
		if !strings.HasSuffix(op.Path, "/{"+raw.WildcardParam+"}") {
			fail("wildcard parameter must be the final path component")
		}
		op.Extensions["x-silo-wildcard-param"] = raw.WildcardParam
	}
	if op.RetrySafety != "" {
		op.Extensions[extRetrySafety] = string(op.RetrySafety)
	}
	if op.Class != ClassPublic {
		op.Security = []map[string][]string{{securitySchemeBearer: {}}}
	}
	if resolvesProfile(op.Class) {
		class := op.Class
		if op.ProfileOptional {
			class = ClassAuthenticated
		}
		op.Parameters = append(op.Parameters, profileHeaderParam(class), profileTokenHeaderParam())
	}
	for _, status := range RawImpliedStatuses(op.Class, op.ServiceBacked) {
		key := strconv.Itoa(status)
		if _, exists := op.Responses[key]; !exists {
			op.Responses[key] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{
				problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")},
			}}
		}
	}
	reg.api.OpenAPI().AddOperation(&op.Operation)
	routeOp := op.Operation
	routeOp.Path = rawRouterPath(op.Path, raw.WildcardParam)
	reg.mu.Lock()
	reg.ops = append(reg.ops, Declared{Method: op.Method, Path: routeOp.Path, OperationID: op.OperationID, Class: op.Class, RetrySafety: op.RetrySafety})
	reg.mu.Unlock()
	serve := func(ctx huma.Context) {
		r, w := humachi.Unwrap(ctx)
		handler.ServeHTTP(w, r.WithContext(ctx.Context()))
	}
	for _, middleware := range []func(huma.Context, func(huma.Context)){observeIdentity, classGate(reg.deps), observeOperation} {
		next := serve
		serve = func(ctx huma.Context) { middleware(ctx, next) }
	}
	reg.api.Adapter().Handle(&routeOp, serve)
}

func rawRouterPath(documentPath, wildcard string) string {
	if wildcard == "" {
		return documentPath
	}
	return strings.TrimSuffix(documentPath, "{"+wildcard+"}") + "*"
}

func rawResponseHeader(response *huma.Response, name string) bool {
	for key, header := range response.Headers {
		if strings.EqualFold(key, name) && header != nil && header.Schema != nil {
			return true
		}
	}
	return false
}
