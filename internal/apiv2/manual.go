package apiv2

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
)

// RawHandshake is a v2 route the listener serves outside Huma: a protocol
// upgrade or a raw browser handshake whose exchange no request/response
// schema describes. Every such route is listed here so the route/spec
// reconciliation (TestCommittedArtifactMatchesRouter) accounts for it. The
// generator adds each one to contracts/api/v2/openapi.json as an operation
// marked x-silo-raw-handshake, describing only what OpenAPI can: the
// parameters, the statuses and their meaning. Its exchange is covered by
// protocol tests rather than by a fake free-form schema
// (docs/architecture/api-contract.md, "What Huma covers").
type RawHandshake struct {
	Method string
	Path   string
	// OperationID is the contract identifier, as for a Huma operation.
	OperationID string
	Tag         string
	Summary     string
	Description string
	// Protocol names the exchange (for example "websocket" or "redirect");
	// it is what the protocol tests are keyed by and the value of the
	// x-silo-raw-handshake extension.
	Protocol string
	// Reason states why the route cannot be a Huma operation.
	Reason string
	// Parameters are the path and query parameters the handshake reads.
	Parameters []*huma.Param
	// Responses maps a status code to its meaning; the first 3xx (or 2xx)
	// is the handshake's success.
	Responses map[int]string
	// handler serves the route; it receives the listener dependencies.
	handler func(deps Dependencies) http.HandlerFunc
}

// extRawHandshake marks a documented operation the listener serves outside
// Huma; its value is the protocol name.
const extRawHandshake = "x-silo-raw-handshake"

// rawHandshakes is the manual registry. Adding an entry here is a contract
// change reviewed with the operation's section.
var rawHandshakes = []RawHandshake{
	oauthInitHandshake,
	oauthCallbackHandshake,
}

// RawHandshakes lists the manual registry.
func RawHandshakes() []RawHandshake {
	return append([]RawHandshake(nil), rawHandshakes...)
}

// operation renders the handshake's documentation as an OpenAPI operation.
func (h RawHandshake) operation() *huma.Operation {
	op := &huma.Operation{
		Method:      h.Method,
		Path:        h.Path,
		OperationID: h.OperationID,
		Tags:        []string{h.Tag},
		Summary:     h.Summary,
		Description: h.Description,
		Parameters:  h.Parameters,
		Responses:   map[string]*huma.Response{},
		Extensions:  map[string]any{extClass: string(ClassPublic), extRawHandshake: h.Protocol},
	}
	for status, description := range h.Responses {
		op.Responses[strconv.Itoa(status)] = &huma.Response{Description: description}
	}
	return op
}

// serveRawHandshakes registers the manual registry through the Huma
// adapter, the one router consumer the route inventory models: the route
// lands on the listener's chi router behind the listener middleware
// (request id, observation, buffering) but outside Huma's typed pipeline,
// and is declared so the 405 answer and the runtime reconciliation know it.
func serveRawHandshakes(reg *Registry) {
	for _, h := range rawHandshakes {
		handler := h.handler(reg.deps)
		op := h.operation()
		reg.api.Adapter().Handle(op, func(ctx huma.Context) {
			r, w := humachi.Unwrap(ctx)
			// Huma's defaultHeaders middleware does not run here; a
			// handshake answer (a redirect carrying a one-time code) is
			// never cached.
			w.Header().Set("Cache-Control", "no-store")
			handler(w, r)
		})
		reg.mu.Lock()
		reg.ops = append(reg.ops, Declared{Method: h.Method, Path: h.Path, OperationID: h.OperationID, Class: ClassPublic})
		reg.mu.Unlock()
	}
}

// reconcileSpec compares the routes a router serves with the operations a
// committed OpenAPI document describes plus the manual registry. It returns
// the served routes no document or registry entry accounts for, and the
// documented or registered routes the router does not serve. Both must be
// empty: a structured route cannot bypass Huma, and the artifact cannot
// describe a route the binary does not serve.
func reconcileSpec(observed []string, doc []byte, handshakes []RawHandshake) (unaccounted, unserved []string, err error) {
	var parsed struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		return nil, nil, err
	}
	expected := map[string]string{}
	documentedRaw := map[string]bool{}
	for path, item := range parsed.Paths {
		for method, op := range item {
			key := strings.ToUpper(method) + " " + path
			expected[key] = "openapi.json"
			var ext struct {
				Raw string `json:"x-silo-raw-handshake"`
			}
			if err := json.Unmarshal(op, &ext); err == nil && ext.Raw != "" {
				documentedRaw[key] = true
			}
		}
	}
	registered := map[string]bool{}
	for _, h := range handshakes {
		key := h.Method + " " + h.Path
		if prev, dup := expected[key]; dup && !documentedRaw[key] {
			return nil, nil, fmt.Errorf("%s is both a %s entry and a manual-registry entry", key, prev)
		}
		registered[key] = true
		expected[key] = "manual registry"
	}
	for key := range documentedRaw {
		if !registered[key] {
			return nil, nil, fmt.Errorf("%s is documented as a raw handshake but is not in the manual registry", key)
		}
	}
	served := map[string]bool{}
	for _, o := range observed {
		served[o] = true
		if _, ok := expected[o]; !ok {
			unaccounted = append(unaccounted, o)
		}
	}
	for key := range expected {
		if !served[key] {
			unserved = append(unserved, key+" ("+expected[key]+")")
		}
	}
	sort.Strings(unaccounted)
	sort.Strings(unserved)
	return unaccounted, unserved, nil
}
