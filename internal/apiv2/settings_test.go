package apiv2

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

func settingsOwner() map[string]string { return with(bearer(memberToken), "X-Profile-Id", "p-owner") }

func TestGetSettingsContract(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/settings/contract", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	// The same bytes and tag v1 /settings/manifest serves, so a client's
	// vendored copy compares equal across the two surfaces.
	want, err := settingscontract.PublicBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("body is not the canonical public manifest (%d bytes vs %d)", rec.Body.Len(), len(want))
	}
	etag, _ := settingscontract.PublicETag()
	if got := rec.Header().Get("ETag"); got != etag {
		t.Fatalf("ETag = %q, want %q", got, etag)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var doc struct {
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || doc.Revision == 0 {
		t.Fatalf("manifest revision missing: %v %s", err, rec.Body.String()[:80])
	}
	// An account-scoped caller may read it; a present profile is judged.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/contract", "", with(bearer(memberToken), "X-Profile-Id", "p-other")), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/contract", "", nil), TypeAuthenticationRequired)
}

func TestGetSettingsContractCapabilities(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/settings/contract/capabilities", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"api_version":1,"revision":12,"contract_etag":"\"etag-12\"","definition_count":40,"scopes":["account","profile"],"client_families":["tv","web"],"supports_batched_effective":true,"supports_idempotent_writes":true,"supports_atomic_shortcuts":true}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	deps := pilotDeps(nil, nil)
	deps.SettingsContract = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/settings/contract/capabilities", "", bearer(memberToken)), TypeDependencyUnavailable)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/contract/capabilities?fields=x", "", bearer(memberToken)), TypeValidationFailed)
}

func TestGetOverlayConfig(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/settings/overlay-config", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	// defaults is absent, not "", when the administrator set none.
	want := `{"enabled":true,"quick_actions_enabled":false,"quick_actions_default":"both"}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/overlay-config", "", bearer(expiredToken)), TypeSessionExpired)
}

func TestSubtitleAppearanceDeviceOverrideRoundTrip(t *testing.T) {
	deps := pilotDeps(nil, nil)
	h := newTestHandler(t, deps)
	device := with(with(settingsOwner(), "X-Silo-Device-Id", "iphone-1"), "X-Silo-Device-Name", "Living room")

	// Before any override: the profile-wide value applies and the device
	// members are absent.
	rec := do(t, h, http.MethodGet, "/api/v2/settings/subtitle-appearance/effective", "", device)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"key":"subtitle_appearance","profile_id":"p-owner","global_value":"{\"fontSize\":\"large\"}","effective_value":"{\"fontSize\":\"large\"}","has_device_override":false}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}

	// The write answers with the canonical resolved representation.
	rec = do(t, h, http.MethodPut, "/api/v2/settings/device/subtitle-appearance", `{"value":"{\"fontSize\":\"xxlarge\"}"}`, device)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{
		"has_device_override": `true`, "device_value": `"{\"fontSize\":\"xxlarge\"}"`, "effective_value": `"{\"fontSize\":\"xxlarge\"}"`,
		"device_id": `"iphone-1"`, "device_name": `"Living room"`, "updated_at": `"2026-01-02T03:04:05.678Z"`,
	} {
		if string(body[field]) != want {
			t.Errorf("%s = %s, want %s", field, body[field], want)
		}
	}
	if _, ok := body["device_platform"]; ok {
		t.Errorf("device_platform emitted without a value: %s", rec.Body.String())
	}
	// Another device on the same profile sees no override.
	rec = do(t, h, http.MethodGet, "/api/v2/settings/subtitle-appearance/effective", "", with(settingsOwner(), "X-Silo-Device-Id", "tv-1"))
	if rec.Code != 200 || bytes.Contains(rec.Body.Bytes(), []byte(`"has_device_override":true`)) {
		t.Fatalf("override leaked to another device: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodDelete, "/api/v2/settings/device/subtitle-appearance", "", device)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("delete: %d %q", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/settings/subtitle-appearance/effective", "", device)
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("after delete: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSubtitleAppearanceDeviceOverrideValidation(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	device := with(settingsOwner(), "X-Silo-Device-Id", "iphone-1")

	// The device header is a declared required parameter.
	p := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/device/subtitle-appearance", `{"value":"{}"}`, settingsOwner()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "header.X-Silo-Device-Id" || p.Errors[0].Code != codeRequired {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/settings/device/subtitle-appearance", "", settingsOwner()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "header.X-Silo-Device-Id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// A value that is not JSON is the seam's decision, rendered at body.value.
	p = requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/device/subtitle-appearance", `{"value":"not json"}`, device), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.value" || p.Errors[0].Code != codeInvalid {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Unknown members are refused; the member is required.
	p = requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/device/subtitle-appearance", `{"value":"{}","device":"x"}`, device), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Code != codeUnknownField {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/device/subtitle-appearance", `{}`, device), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.value" || p.Errors[0].Code != codeRequired {
		t.Fatalf("errors = %+v", p.Errors)
	}
}

func TestSubtitleAppearanceDenied(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	// Profile scoped: the header is required, and a locked profile asks for
	// the PIN.
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/subtitle-appearance/effective", "", with(bearer(memberToken), "X-Silo-Device-Id", "iphone-1")), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "header.x-profile-id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	locked := with(with(bearer(memberToken), "X-Profile-Id", "p-locked"), "X-Silo-Device-Id", "iphone-1")
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/device/subtitle-appearance", `{"value":"{}"}`, locked), TypeProfileVerificationRequired)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/settings/device/subtitle-appearance", "", locked), TypeProfileVerificationRequired)
	// Demo mode refuses the mutations to non-admins, never the read.
	demo := pilotDeps(nil, nil)
	demo.DemoSettings = fakeSettings{demo: true}
	dh := newTestHandler(t, demo)
	requireProblem(t, do(t, dh, http.MethodPut, "/api/v2/settings/device/subtitle-appearance", `{"value":"{}"}`, with(settingsOwner(), "X-Silo-Device-Id", "iphone-1")), TypePermissionDenied)
	if rec := do(t, dh, http.MethodGet, "/api/v2/settings/subtitle-appearance/effective", "", with(settingsOwner(), "X-Silo-Device-Id", "iphone-1")); rec.Code != 200 {
		t.Fatalf("demo mode blocked a read: %d %s", rec.Code, rec.Body.String())
	}
	// A store failure is an internal error with no detail.
	deps := pilotDeps(nil, nil)
	deps.Settings = &fakeSettingsSeam{err: errStore}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/settings/subtitle-appearance/effective", "", settingsOwner()), TypeInternalError)
}

func TestListPluginSettings(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/settings/plugins", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"items":[{"id":"3","plugin_id":"org.example.subtitles","version":"1.2.0","user_config_schema":[{"key":"region","title":"Region","description":"","json_schema":"{\"type\":\"string\"}","required":false}],"routes":[{"id":"dashboard","method":"GET","path":"/dashboard","access":"user","navigable":true,"navigation_label":"Dashboard","navigation_kind":"user","static_asset":false}],"assets":[],"category":"Tools"}]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// No installations: an empty items array, never null.
	deps := pilotDeps(nil, nil)
	deps.PluginSettings = &fakePluginSettingsSeam{}
	rec = do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/settings/plugins", "", bearer(memberToken))
	if rec.Code != 200 || rec.Body.String() != `{"items":[]}`+"\n" {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body.String())
	}
	// Not wired: fail closed, the route stays.
	deps.PluginSettings = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/settings/plugins", "", bearer(memberToken)), TypeDependencyUnavailable)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/plugins", "", nil), TypeAuthenticationRequired)
}

func TestGetPluginSettings(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/settings/plugins/3", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body struct {
		Installation struct {
			ID string `json:"id"`
		} `json:"installation"`
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Installation.ID != "3" || body.Values["region"] != "us" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// Values the account never set: an empty object, never null.
	deps := pilotDeps(nil, nil)
	_, _, plugins := settingsFakes()
	plugins.values = nil
	deps.PluginSettings = plugins
	rec = do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/settings/plugins/3", "", bearer(memberToken))
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"values":{}`)) {
		t.Fatalf("empty values: %d %s", rec.Code, rec.Body.String())
	}
	// Unknown, and not-a-number, are the same 404: identifiers are opaque.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/plugins/9", "", bearer(memberToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/plugins/abc", "", bearer(memberToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/plugins/3", "", bearer(expiredToken)), TypeSessionExpired)
}

// TestPluginSettingsSchemaIsExtensionBag locks the values bag: the plugin
// defines the keys, so the document declares a typed map marked as an
// extension bag rather than a closed object.
func TestPluginSettingsSchemaIsExtensionBag(t *testing.T) {
	doc := generatedDocument(t)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	values := schemas["PluginSettings"].(map[string]any)["properties"].(map[string]any)["values"].(map[string]any)
	if values[extExtensionBag] != "plugin-setting-values" {
		t.Fatalf("values is not marked as an extension bag: %v", values)
	}
	ap, _ := values["additionalProperties"].(map[string]any)
	if ap["type"] != "string" {
		t.Fatalf("values are not typed as strings: %v", values)
	}
	var _ handlers.PluginUserSettingsDetailView
}
