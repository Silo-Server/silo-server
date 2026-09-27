package apiv2

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
)

func rawFixture(method string, class Class) RawOperation {
	id := "getRawFixture"
	if method == http.MethodHead {
		id = "headRawFixture"
	}
	return RawOperation{Operation: Operation{Operation: huma.Operation{
		Method: method, Path: Prefix + "/raw-fixture/{id}", OperationID: id, Tags: []string{"test"},
		Parameters: []*huma.Param{{Name: "id", In: "path", Required: true, Schema: &huma.Schema{Type: "string"}}},
		Responses: map[string]*huma.Response{
			"200": {Description: "File bytes", Content: map[string]*huma.MediaType{"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}},
			"206": {Description: "Requested byte range", Content: map[string]*huma.MediaType{"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}},
			"416": {Description: "Range not satisfiable"},
		},
	}, Class: class}, Protocol: "byte-range", Reason: "File bytes retain HTTP range and HEAD semantics."}
}

func TestRawRangeHeadAndDocument(t *testing.T) {
	var declared []Declared
	handler := NewHandler(Dependencies{testRegister: func(reg *Registry) {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			RegisterRaw(reg, rawFixture(method, ClassPublic), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if chi.URLParam(r, "id") != "asset" {
					t.Error("path context was lost")
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				http.ServeContent(w, r, "fixture", time.Time{}, strings.NewReader("0123456789"))
			}))
		}
		declared = reg.Declared()
		raw := reg.api.OpenAPI().Paths[Prefix+"/raw-fixture/{id}"].Get
		if raw.Responses["206"] == nil || raw.Responses["200"].Content["application/octet-stream"] == nil || raw.Extensions["x-silo-raw-protocol"] != "byte-range" {
			t.Fatal("raw handshake was not documented")
		}
		if raw.Responses["422"] != nil || raw.Responses["406"] != nil {
			t.Fatal("raw route advertises JSON validation")
		}
	}})
	rangeResponse := do(t, handler, http.MethodGet, Prefix+"/raw-fixture/asset?opaque=accepted", "", map[string]string{"Range": "bytes=2-4", "Accept": "application/octet-stream"})
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "234" || rangeResponse.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Fatalf("range: %d %v %q", rangeResponse.Code, rangeResponse.Header(), rangeResponse.Body.String())
	}
	head := do(t, handler, http.MethodHead, Prefix+"/raw-fixture/asset", "", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "10" {
		t.Fatalf("HEAD: %d %v %q", head.Code, head.Header(), head.Body.String())
	}
	deniedMethod := do(t, handler, http.MethodPost, Prefix+"/raw-fixture/asset", "", nil)
	if deniedMethod.Code != http.StatusMethodNotAllowed || deniedMethod.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("method: %d %v", deniedMethod.Code, deniedMethod.Header())
	}
	found := 0
	for _, d := range declared {
		if d.Path == Prefix+"/raw-fixture/{id}" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("declared raw routes=%d", found)
	}
}

func TestRawStreamingFlushesBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	handler := NewHandler(Dependencies{testRegister: func(reg *Registry) {
		RegisterRaw(reg, rawFixture(http.MethodGet, ClassPublic), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, "first")
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("flush: %v", err)
				return
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, "last")
		}))
	}})
	server := httptest.NewServer(handler)
	defer server.Close()
	// Ensure failures release the handler before closing the server.
	defer close(release)
	client := server.Client()
	client.Timeout = 3 * time.Second
	response, err := client.Get(server.URL + Prefix + "/raw-fixture/asset")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, 5)
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	if string(first) != "first" {
		t.Fatalf("first bytes=%q", first)
	}
}

func TestRawAuthorizationFailsClosed(t *testing.T) {
	for _, class := range []Class{ClassAuthenticated, ClassProfileScoped, ClassActingAdmin, ClassPermissionGated} {
		t.Run(string(class), func(t *testing.T) {
			raw := rawFixture(http.MethodGet, class)
			if class == ClassPermissionGated {
				raw.Permission = "acting_admin"
			}
			handler := NewHandler(Dependencies{testRegister: func(reg *Registry) {
				RegisterRaw(reg, raw, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unauthorized raw handler ran") }))
			}})
			response := do(t, handler, http.MethodGet, Prefix+"/raw-fixture/asset", "", nil)
			requireProblem(t, response, TypeDependencyUnavailable)
		})
	}
}

func TestRawRejectsUndocumentedAndJSONPorts(t *testing.T) {
	for name, change := range map[string]func(*RawOperation){
		"reason":    func(r *RawOperation) { r.Reason = "" },
		"responses": func(r *RawOperation) { r.Responses = nil },
		"path":      func(r *RawOperation) { r.Parameters = nil },
		"JSON": func(r *RawOperation) {
			r.Responses["200"].Content = map[string]*huma.MediaType{mediaTypeJSON: {Schema: &huma.Schema{Type: "object"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid raw registration accepted")
				}
			}()
			NewHandler(Dependencies{testRegister: func(reg *Registry) {
				r := rawFixture(http.MethodGet, ClassPublic)
				change(&r)
				RegisterRaw(reg, r, http.NotFoundHandler())
			}})
		})
	}
}

func TestRawRetainsAuthorizedIdentity(t *testing.T) {
	deps := parityDeps(false)
	calls := 0
	deps.testRegister = func(reg *Registry) {
		RegisterRaw(reg, rawFixture(http.MethodGet, ClassAuthenticated), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if apimw.GetUserID(r.Context()) != 1 {
				t.Error("authorized identity lost when unwrapping Huma context")
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, "authorized")
		}))
	}
	handler := NewHandler(deps)
	denied := do(t, handler, http.MethodGet, Prefix+"/raw-fixture/asset", "", bearer("invalid"))
	requireProblem(t, denied, TypeInvalidToken)
	if calls != 0 {
		t.Fatal("invalid bearer reached raw transport")
	}
	admitted := do(t, handler, http.MethodGet, Prefix+"/raw-fixture/asset", "", bearer(memberToken))
	if admitted.Code != http.StatusOK || admitted.Body.String() != "authorized" || calls != 1 {
		t.Fatalf("admitted %d %q calls=%d", admitted.Code, admitted.Body.String(), calls)
	}
}

func TestRawProfileAuthorizationPrecedesBytes(t *testing.T) {
	deps := parityDeps(false)
	calls := 0
	deps.testRegister = func(reg *Registry) {
		RegisterRaw(reg, rawFixture(http.MethodGet, ClassProfileScoped), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if apimw.GetUserID(r.Context()) != 1 || apimw.GetProfileID(r.Context()) != "p-owner" {
				t.Error("authorized profile lost")
			}
			_, _ = io.WriteString(w, "authorized")
		}))
	}
	handler := NewHandler(deps)
	for _, test := range []struct {
		profile string
		want    ProblemType
	}{
		{"", TypeValidationFailed}, {"p-other", TypeNotFound}, {"p-locked", TypeProfileVerificationRequired},
	} {
		response := do(t, handler, http.MethodGet, Prefix+"/raw-fixture/asset", "", with(bearer(memberToken), "X-Profile-Id", test.profile))
		requireProblem(t, response, test.want)
	}
	if calls != 0 {
		t.Fatal("unauthorized profile reached bytes")
	}
	admitted := do(t, handler, http.MethodGet, Prefix+"/raw-fixture/asset", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if admitted.Code != http.StatusOK || calls != 1 {
		t.Fatalf("admitted %d calls=%d", admitted.Code, calls)
	}
}
