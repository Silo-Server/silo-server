package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/notifications"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// Discover operations, not schema names: several capability endpoints historically
// used anonymous Body schemas, and the two subtitle probes have status paths.
func capabilityOperations(t *testing.T) (map[string]any, map[string]map[string]any) {
	t.Helper()
	doc := generatedDocument(t)
	operations := map[string]map[string]any{}
	for path, raw := range doc["paths"].(map[string]any) {
		item := raw.(map[string]any)
		operation, ok := item["get"].(map[string]any)
		if !ok {
			continue
		}
		if strings.Contains(path, "/capabilities") || strings.HasSuffix(path, "/capability") || strings.HasSuffix(path, "/command-capabilities") || path == Prefix+"/requests/status" || path == Prefix+"/subtitles/providers/status" || path == Prefix+"/subtitles/ai/status" {
			operations[path] = operation
		}
	}
	return doc, operations
}

func TestCapabilityOperationsCommonContract(t *testing.T) {
	doc, operations := capabilityOperations(t)
	if len(operations) < 47 {
		t.Fatalf("capability operation census lost routes: %d", len(operations))
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	publicSchemas := map[string]bool{}
	for _, op := range operations {
		if op["x-silo-class"] == string(ClassPublic) {
			response := op["responses"].(map[string]any)["200"].(map[string]any)
			body := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			publicSchemas[body["$ref"].(string)] = true
		}
	}
	for path, op := range operations {
		t.Run(op["operationId"].(string), func(t *testing.T) {
			responses := op["responses"].(map[string]any)
			response := responses["200"].(map[string]any)
			body := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			schema := schemas[strings.TrimPrefix(body["$ref"].(string), "#/components/schemas/")].(map[string]any)
			props := schema["properties"].(map[string]any)
			required := schema["required"].([]any)
			for _, field := range []string{"revision", "state"} {
				prop, ok := props[field].(map[string]any)
				if !ok || prop["type"] != "string" || !slices.Contains(required, any(field)) {
					t.Fatalf("%s lacks required string %s", path, field)
				}
			}
			if op["x-silo-class"] != string(ClassPublic) && props["allowed"] == nil {
				t.Fatal("scoped capability lacks allowed")
			}
			wantRequired := !publicSchemas[body["$ref"].(string)]
			if slices.Contains(required, any("allowed")) != wantRequired {
				t.Fatalf("allowed required=%v, want %v for schema %s", !wantRequired, wantRequired, body["$ref"])
			}
			if responses["304"] == nil {
				t.Fatal("missing conditional response")
			}
			for _, field := range []string{"ETag", "Cache-Control"} {
				if response["headers"].(map[string]any)[field] == nil {
					t.Fatalf("missing %s", field)
				}
			}
		})
	}
}

func TestCapabilityOperationsRuntimeAndRevalidation(t *testing.T) {
	_, operations := capabilityOperations(t)
	h := NewHandler(fixtureDeps())
	for path, op := range operations {
		t.Run(op["operationId"].(string), func(t *testing.T) {
			headers := profileOwner()
			switch op["x-silo-class"] {
			case string(ClassPublic):
				headers = map[string]string{}
			case string(ClassActingAdmin):
				headers = bearer(adminToken)
			}
			rec := do(t, h, http.MethodGet, path, "", headers)
			if rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			revision, ok := body["revision"].(string)
			if !ok || revision == "" {
				t.Fatal("missing opaque revision", body)
			}
			state, ok := body["state"].(string)
			if !ok || !slices.Contains([]string{StateAvailable, StateDisabled, StateNotConfigured, StateUnsupported}, state) {
				t.Fatal("invalid state", body)
			}
			if op["x-silo-class"] != string(ClassPublic) {
				allowed, ok := body["allowed"].(bool)
				if !ok || (state != StateAvailable && allowed) {
					t.Fatal("invalid effective allowed", body)
				}
			}
			tag := rec.Header().Get("ETag")
			if tag == "" || rec.Header().Get("Cache-Control") != cachePrivateNoCache {
				t.Fatal("missing private revalidation", rec.Header())
			}
			cached := do(t, h, http.MethodGet, path, "", with(headers, "If-None-Match", "W/"+tag))
			if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 || cached.Header().Get("ETag") != tag {
				t.Fatalf("revalidation: %d %s", cached.Code, cached.Body.String())
			}
			if op["x-silo-class"] != string(ClassPublic) {
				requireProblem(t, do(t, h, http.MethodGet, path, "", map[string]string{"If-None-Match": tag}), TypeAuthenticationRequired)
			}
		})
	}
}

// Existing domain assertions remain exact while accepting the new common head.
func capabilityBodyMatches(t *testing.T, raw []byte, expected string) bool {
	t.Helper()
	var got, want map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if revision, ok := got["revision"].(string); !ok || revision == "" {
		t.Fatal("missing opaque capability revision", got)
	}
	delete(got, "revision")
	delete(want, "revision")
	for _, field := range []string{"state", "allowed"} {
		if _, ok := want[field]; !ok {
			delete(got, field)
		}
	}
	return reflect.DeepEqual(got, want)
}

type capabilityDownloads struct {
	fakeDownloadRegistry
	enabled, permitted bool
}

func (f *capabilityDownloads) Capability(context.Context, int) (downloads.Capability, error) {
	return downloads.Capability{Enabled: f.enabled, DownloadAllowed: f.permitted, QualityPresets: []string{"original"}}, nil
}

func TestCapabilityStatePermissionAndRevisionAreIndependent(t *testing.T) {
	deps := pilotDeps(nil, nil)
	service := &capabilityDownloads{enabled: true, permitted: true}
	deps.Downloads = service
	h := NewHandler(deps)
	path := Prefix + "/capabilities/downloads"
	first := do(t, h, "GET", path, "", profileOwner())
	var before DownloadCapability
	if err := json.Unmarshal(first.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if first.Code != 200 || before.State != StateAvailable || before.Allowed == nil || !*before.Allowed {
		t.Fatal(first.Code, first.Body.String())
	}
	// Losing permission leaves server support available. Both the revision and
	// validator must change, even though configuration is unchanged.
	service.permitted = false
	denied := do(t, h, "GET", path, "", with(profileOwner(), "If-None-Match", first.Header().Get("ETag")))
	var after DownloadCapability
	if err := json.Unmarshal(denied.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if denied.Code != 200 || after.State != StateAvailable || after.Allowed == nil || *after.Allowed || before.Revision == after.Revision || first.Header().Get("ETag") == denied.Header().Get("ETag") {
		t.Fatal(denied.Code, denied.Body.String())
	}
	service.permitted = true
	service.enabled = false
	disabled := do(t, h, "GET", path, "", profileOwner())
	if err := json.Unmarshal(disabled.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if disabled.Code != 200 || after.State != StateDisabled || after.Allowed == nil || *after.Allowed || !after.DownloadAllowed {
		t.Fatal(disabled.Code, disabled.Body.String())
	}
	// A strong If-Match is evaluated before the weak cache validator.
	requireProblem(t, do(t, h, "GET", path, "", with(with(profileOwner(), "If-Match", first.Header().Get("ETag")), "If-None-Match", disabled.Header().Get("ETag"))), TypePreconditionFailed)
	requireProblem(t, do(t, h, "GET", path, "", with(profileOwner(), "If-None-Match", "broken")), TypeMalformedRequest)
}

func TestEmailVerificationCapabilityRejectsChildAuthority(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.NotificationEmailVerification = &fakeEmailVerification{err: notifications.ErrEmailChildProfile}
	rec := do(t, NewHandler(deps), "GET", Prefix+"/notifications/email-preferences/address/capabilities", "", profileOwner())
	var body NotificationEmailVerificationCapability
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || body.State != StateAvailable || !body.QueueAvailable || body.Allowed == nil || *body.Allowed {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

type deniedRequestCapability struct{ fakeLifecycle }

func (*deniedRequestCapability) RequestCapabilityAllowed(context.Context, mediarequests.Viewer) (bool, error) {
	return false, nil
}
func TestRequestCapabilityReportsBlockedAccountWithoutDisablingDomain(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.RequestLifecycle = &deniedRequestCapability{}
	rec := do(t, NewHandler(deps), "GET", Prefix+"/requests/status", "", profileOwner())
	var body FeatureStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || body.State != StateAvailable || !body.RequestsEnabled || body.Allowed == nil || *body.Allowed {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestCapabilityMissingCacheHeaderRejectedAtRegistration(t *testing.T) {
	type malformedCapabilityOutput struct {
		Status int
		ETag   string `header:"ETag"`
		Body   Capability
	}
	defer func() {
		failure := recover()
		if failure == nil || !strings.Contains(fmt.Sprint(failure), "Cache-Control") {
			t.Fatalf("missing header must fail registration, got %v", failure)
		}
	}()
	operation := Operation{Operation: humaOp(http.MethodGet, Prefix+"/probe/capabilities", "getMalformedCapability", "probe", "Malformed capability"), Class: ClassPublic}
	prepareCapabilityOperation(&operation, func(context.Context, *CapabilityInput) (*malformedCapabilityOutput, error) {
		t.Fatal("handler must not run")
		return nil, nil
	})
}

type capabilityScopeProbe struct{ Capability }
type capabilityScopeProbeOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         capabilityScopeProbe
}

func TestCapabilityAllowedSchemaRespectsEveryUsage(t *testing.T) {
	for _, first := range []Class{ClassPublic, ClassAuthenticated} {
		t.Run(string(first), func(t *testing.T) {
			NewHandler(Dependencies{testRegister: func(reg *Registry) {
				register := func(class Class, id string) {
					Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/probe/"+id, id, "probe", "Capability schema scope probe"), Class: class}, func(context.Context, *CapabilityInput) (*capabilityScopeProbeOutput, error) {
						return &capabilityScopeProbeOutput{Body: capabilityScopeProbe{Capability: Capability{State: StateAvailable}}}, nil
					})
				}
				register(first, "getFirstScopeCapability")
				schema := reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[capabilityScopeProbe](), false, "")
				if slices.Contains(schema.Required, "allowed") != (first != ClassPublic) {
					t.Fatalf("first usage: %v", schema.Required)
				}
				second := ClassPublic
				if first == ClassPublic {
					second = ClassAuthenticated
				}
				register(second, "getSecondScopeCapability")
				if slices.Contains(schema.Required, "allowed") {
					t.Fatalf("mixed usage requires allowed: %v", schema.Required)
				}
			}})
		})
	}
}
