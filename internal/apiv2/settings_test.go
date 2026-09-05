package apiv2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
	if len(p.Errors) != 1 || p.Errors[0].Location != locationBodyValue || p.Errors[0].Code != codeInvalid {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Unknown members are refused; the member is required.
	p = requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/device/subtitle-appearance", `{"value":"{}","device":"x"}`, device), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Code != codeUnknownField {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/device/subtitle-appearance", `{}`, device), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationBodyValue || p.Errors[0].Code != codeRequired {
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
	_, _, plugins, _ := settingsFakes()
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

func TestUpdatePluginSettings(t *testing.T) {
	deps := pilotDeps(nil, nil)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPut, "/api/v2/settings/plugins/3", `{"values":{"region":"eu","lang":"fr"}}`, bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	// The write answers with the same document getPluginSettings serves.
	var body struct {
		Installation map[string]json.RawMessage `json:"installation"`
		Values       map[string]string          `json:"values"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body.Installation["id"]) != `"3"` || body.Values["region"] != "eu" || body.Values["lang"] != "fr" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// An empty set clears; values is still emitted as {}.
	rec = do(t, h, http.MethodPut, "/api/v2/settings/plugins/3", `{"values":{}}`, bearer(memberToken))
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"values":{}`)) {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/plugins/9", `{"values":{}}`, bearer(memberToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/plugins/abc", `{"values":{}}`, bearer(memberToken)), TypeNotFound)
	// The member is required and its values are strings; unknown members are refused.
	p := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/plugins/3", `{}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationBodyValues || p.Errors[0].Code != codeRequired {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/plugins/3", `{"values":{"region":1}}`, bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/plugins/3", `{"values":{},"extra":1}`, bearer(memberToken)), TypeValidationFailed)
	// The plugin's own schema refusal is rendered at body.values.
	plugins := deps.PluginSettings.(*fakePluginSettingsSeam)
	plugins.err = &handlers.APIError{Status: 400, Code: "bad_request", Message: "region must be one of us, eu", Field: "values"}
	p = requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/plugins/3", `{"values":{"region":"xx"}}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationBodyValues || p.Errors[0].Code != codeInvalid {
		t.Fatalf("errors = %+v", p.Errors)
	}
	plugins.err = nil
	requireProblem(t, do(t, newTestHandler(t, parityDeps(true)), http.MethodPut, "/api/v2/settings/plugins/3", `{"values":{}}`, bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/plugins/3", `{"values":{}}`, nil), TypeAuthenticationRequired)
}

func TestSettingValueRoundTrip(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	path := "/api/v2/settings/values/ui.theme?scope=profile"

	// Nothing stored: a read is a 404, and a delete too.
	requireProblem(t, do(t, h, http.MethodGet, path, "", settingsOwner()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodDelete, path, "", settingsOwner()), TypeNotFound)

	rec := do(t, h, http.MethodPut, path, `{"value":"cinema-light"}`, settingsOwner())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"key":"ui.theme","scope":"profile","profile_id":"p-owner","value":"cinema-light","revision":1,"updated_at":"2026-01-02T03:04:05.678Z"}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, path, "", settingsOwner())
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("read: %d %s", rec.Code, rec.Body.String())
	}
	// The value's shape is the contract's, not this document's: any JSON
	// is carried to the seam, which decides.
	requireProblem(t, do(t, h, http.MethodPut, path, `{"value":7}`, settingsOwner()), TypeValidationFailed)

	// Another profile sees nothing at its own row.
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(bearer(memberToken), "X-Profile-Id", "p-primary")), TypeNotFound)

	rec = do(t, h, http.MethodDelete, path, "", settingsOwner())
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("delete: %d %q", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, path, "", settingsOwner()), TypeNotFound)
}

func TestSettingValueScopes(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	// account scope carries no profile.
	rec := do(t, h, http.MethodPut, "/api/v2/settings/values/ui.theme?scope=account", `{"value":"cobalt-studio"}`, settingsOwner())
	if rec.Code != 200 || bytes.Contains(rec.Body.Bytes(), []byte(`"profile_id"`)) {
		t.Fatalf("account: %d %s", rec.Code, rec.Body.String())
	}
	// profile_device stores against the declared device header, or a named device.
	rec = do(t, h, http.MethodPut, "/api/v2/settings/values/ui.theme?scope=profile_device", `{"value":"x"}`, with(settingsOwner(), "X-Silo-Device-Id", "iphone-1"))
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"device_id":"iphone-1"`)) {
		t.Fatalf("device: %d %s", rec.Code, rec.Body.String())
	}
	// profile_library renders the library as a string ID.
	rec = do(t, h, http.MethodPut, "/api/v2/settings/values/ui.theme?scope=profile_library&library_id=3", `{"value":"x"}`, settingsOwner())
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"library_id":"3"`)) {
		t.Fatalf("library: %d %s", rec.Code, rec.Body.String())
	}
	// profile_client takes the family from the header.
	rec = do(t, h, http.MethodPut, "/api/v2/settings/values/ui.theme?scope=profile_client", `{"value":"x"}`, with(settingsOwner(), "X-Silo-Client-Family", "tv"))
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"client_family":"tv"`)) {
		t.Fatalf("client: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSettingValueValidation(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	cases := []struct {
		name, method, path, body string
		headers                  map[string]string
		location, code           string
	}{
		{"scope required", http.MethodGet, "/api/v2/settings/values/ui.theme", "", settingsOwner(), locationQueryScope, codeRequired},
		{"scope enum", http.MethodGet, "/api/v2/settings/values/ui.theme?scope=global", "", settingsOwner(), locationQueryScope, codeInvalidEnum},
		{"unknown key is 422, not 404", http.MethodGet, "/api/v2/settings/values/no.such?scope=profile", "", settingsOwner(), locationPathKey, codeInvalid},
		{"device header missing", http.MethodPut, "/api/v2/settings/values/ui.theme?scope=profile_device", `{"value":"x"}`, settingsOwner(), "header." + deviceIDHeader, codeInvalid},
		{"client family missing", http.MethodPut, "/api/v2/settings/values/ui.theme?scope=profile_client", `{"value":"x"}`, settingsOwner(), "header." + clientFamilyHeader, codeInvalid},
		{"client family enum", http.MethodPut, "/api/v2/settings/values/ui.theme?scope=profile_client", `{"value":"x"}`, with(settingsOwner(), "X-Silo-Client-Family", "toaster"), "header.X-Silo-Client-Family", codeInvalidEnum},
		{"library id", http.MethodGet, "/api/v2/settings/values/ui.theme?scope=profile_library&library_id=9", "", settingsOwner(), locationQueryLibraryID, codeInvalid},
		{"value required", http.MethodPut, "/api/v2/settings/values/ui.theme?scope=profile", `{}`, settingsOwner(), locationBodyValue, codeRequired},
		{"unknown member", http.MethodPut, "/api/v2/settings/values/ui.theme?scope=profile", `{"value":"x","mutation_id":"1"}`, settingsOwner(), "body.mutation_id", codeUnknownField},
		{"nav.shortcuts whole-document write refused", http.MethodPut, "/api/v2/settings/values/nav.shortcuts?scope=profile", `{"value":{"items":[]}}`, settingsOwner(), locationPathKey, codeInvalid},
		{"nav.shortcuts delete refused", http.MethodDelete, "/api/v2/settings/values/nav.shortcuts?scope=profile", "", settingsOwner(), locationPathKey, codeInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := requireProblem(t, do(t, h, c.method, c.path, c.body, c.headers), TypeValidationFailed)
			if len(p.Errors) != 1 || p.Errors[0].Location != c.location || p.Errors[0].Code != c.code {
				t.Fatalf("errors = %+v", p.Errors)
			}
		})
	}
	// A named profile: unknown is 404; naming another profile without the
	// household verification is 403.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/values/ui.theme?scope=profile&profile_id=p-nope", "", settingsOwner()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/values/ui.theme?scope=profile&profile_id=p-other", "", with(bearer(apiKeyToken), "X-Profile-Id", "p-locked")), TypePermissionDenied)
	// Class denials.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/values/ui.theme?scope=profile", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/values/ui.theme?scope=profile", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, newTestHandler(t, parityDeps(true)), http.MethodPut, "/api/v2/settings/values/ui.theme?scope=profile", `{"value":"x"}`, settingsOwner()), TypePermissionDenied)
	requireProblem(t, do(t, newTestHandler(t, parityDeps(true)), http.MethodDelete, "/api/v2/settings/values/ui.theme?scope=profile", "", settingsOwner()), TypePermissionDenied)
	deps := pilotDeps(nil, nil)
	deps.SettingValues = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/settings/values/ui.theme?scope=profile", "", settingsOwner()), TypeDependencyUnavailable)
}

func TestListSettingValues(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	if rec := do(t, h, http.MethodPut, "/api/v2/settings/values/ui.theme?scope=profile", `{"value":"cinema-light"}`, settingsOwner()); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	// Keys repeat the parameter; a comma is not a separator. Unset keys
	// stay in the answer with is_set false.
	rec := do(t, h, http.MethodGet, "/api/v2/settings/values?scope=profile&keys=ui.theme&keys=playback.preferred_quality", "", settingsOwner())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"items":[{"key":"ui.theme","scope":"profile","profile_id":"p-owner","is_set":true,"value":"cinema-light","revision":1,"updated_at":"2026-01-02T03:04:05.678Z"},{"key":"playback.preferred_quality","scope":"profile","profile_id":"p-owner","is_set":false}],"revision":8}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/values?scope=profile&keys=ui.theme,playback.preferred_quality", "", settingsOwner()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationQueryKeys {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// A key mentioned twice is answered twice, in order: the seam does not
	// reparse the array as CSV or deduplicate it.
	rec = do(t, h, http.MethodGet, "/api/v2/settings/values?scope=profile&keys=ui.theme&keys=ui.theme", "", settingsOwner())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if got := strings.Count(rec.Body.String(), `"key":"ui.theme"`); got != 2 {
		t.Fatalf("a repeated key is answered once per mention, got %d in %s", got, rec.Body.String())
	}
	p = requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/values?scope=profile", "", settingsOwner()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationQueryKeys || p.Errors[0].Code != codeRequired {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/values?scope=profile&keys=ui.theme", "", bearer(memberToken)), TypeValidationFailed)
}

func TestListEffectiveSettings(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	if rec := do(t, h, http.MethodPut, "/api/v2/settings/values/playback.preferred_quality?scope=profile", `{"value":"2160p"}`, settingsOwner()); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	rec := do(t, h, http.MethodGet, "/api/v2/settings/values/effective?keys=playback.preferred_quality&keys=ui.theme", "", settingsOwner())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	// A constrained value reports the authored one and what is permitted;
	// a default carries no scope members.
	want := `{"items":[{"key":"playback.preferred_quality","value":"1080p","source":"profile","stored_value":"2160p","constrained":true,"constraint_kind":"ceiling","permitted_values":["auto","1080p"],"definition_revision":3,"updated_at":"2026-01-02T03:04:05.678Z","source_context":{"profile_id":"p-owner"},"scope":"profile","profile_id":"p-owner"},{"key":"ui.theme","value":"midnight-cinema","source":"default","definition_revision":3}],"revision":8}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// No keys resolves every server-stored setting.
	rec = do(t, h, http.MethodGet, "/api/v2/settings/values/effective", "", settingsOwner())
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"key":"nav.shortcuts"`)) {
		t.Fatalf("all: %d %s", rec.Code, rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/values/effective?keys=no.such", "", settingsOwner()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationQueryKeys {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/values/effective?library_ids=x", "", settingsOwner()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationQueryLibraryIDs {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Each list passes its own maxItems, but the seam bounds their sum; that
	// refusal lands on a declared parameter, not the seam's single-id name.
	var many strings.Builder
	many.WriteString("/api/v2/settings/values/effective?keys=ui.theme")
	for i := 1; i <= 101; i++ {
		fmt.Fprintf(&many, "&library_ids=%d", i)
	}
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&many, "&series_ids=tv:%d", i)
	}
	p = requireProblem(t, do(t, h, http.MethodGet, many.String(), "", settingsOwner()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationQueryLibraryIDs {
		t.Fatalf("combined bound errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/values/effective", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/settings/values/effective", "", bearer(expiredToken)), TypeSessionExpired)
}

func TestResolveEffectiveSettings(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	if rec := do(t, h, http.MethodPut, "/api/v2/settings/values/ui.theme?scope=profile", `{"value":"cinema-light"}`, settingsOwner()); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	body := `{"keys":["ui.theme","playback.preferred_quality"],"contexts":[{"context_id":"a","library_id":"3"},{"context_id":"b","series_id":"tv:1"}]}`
	rec := do(t, h, http.MethodPost, "/api/v2/settings/values/effective", body, settingsOwner())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	// source_context is the winning row's identity, as on the single resolve:
	// a profile-scoped value answers a library or series context with the
	// profile alone, and a contract default carries none. It is never the
	// context the batch asked for; context_id is what ties an answer back.
	want := `{"items":[{"context_id":"a","settings":[{"key":"ui.theme","value":"cinema-light","source":"profile","definition_revision":3,"updated_at":"2026-01-02T03:04:05.678Z","source_context":{"profile_id":"p-owner"},"scope":"profile","profile_id":"p-owner"},{"key":"playback.preferred_quality","value":"auto","source":"default","definition_revision":3}]},{"context_id":"b","settings":[{"key":"ui.theme","value":"cinema-light","source":"profile","definition_revision":3,"updated_at":"2026-01-02T03:04:05.678Z","source_context":{"profile_id":"p-owner"},"scope":"profile","profile_id":"p-owner"},{"key":"playback.preferred_quality","value":"auto","source":"default","definition_revision":3}]}],"revision":8}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// The declared contexts bound fits every context shape: 100 contexts
	// naming both a library and a series is the seam's whole id budget and
	// resolves; one more context is refused at the schema.
	full := effectiveBatchBody(100)
	if rec := do(t, h, http.MethodPost, "/api/v2/settings/values/effective", full, settingsOwner()); rec.Code != 200 {
		t.Fatalf("100 two-id contexts: %d %s", rec.Code, rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/settings/values/effective", effectiveBatchBody(101), settingsOwner()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationBodyContexts {
		t.Fatalf("101 contexts errors = %+v", p.Errors)
	}
	cases := []struct{ name, body, location string }{
		{"keys required", `{"contexts":[{"context_id":"a","library_id":"3"}]}`, locationBodyKeys},
		{"keys empty", `{"keys":[],"contexts":[{"context_id":"a","library_id":"3"}]}`, locationBodyKeys},
		{"unknown key", `{"keys":["no.such"],"contexts":[{"context_id":"a","library_id":"3"}]}`, locationBodyKeys},
		{"contexts empty", `{"keys":["ui.theme"],"contexts":[]}`, locationBodyContexts},
		{"context id required", `{"keys":["ui.theme"],"contexts":[{"library_id":"3"}]}`, "body.contexts[0].context_id"},
		{"context needs content", `{"keys":["ui.theme"],"contexts":[{"context_id":"a"}]}`, locationBodyContexts},
		{"duplicate context", `{"keys":["ui.theme"],"contexts":[{"context_id":"a","library_id":"3"},{"context_id":"a","library_id":"3"}]}`, locationBodyContexts},
		{"unknown member", `{"keys":["ui.theme"],"contexts":[{"context_id":"a","library_id":"3","item_id":"x"}]}`, "body.contexts[0].item_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/settings/values/effective", c.body, settingsOwner()), TypeValidationFailed)
			if len(p.Errors) != 1 || p.Errors[0].Location != c.location {
				t.Fatalf("errors = %+v", p.Errors)
			}
		})
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/settings/values/effective", body, bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/settings/values/effective", body, nil), TypeAuthenticationRequired)
}

// effectiveBatchBody is a resolveEffectiveSettings body of n contexts that
// each name both a library and a series.
func effectiveBatchBody(n int) string {
	var b strings.Builder
	b.WriteString(`{"keys":["ui.theme"],"contexts":[`)
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"context_id":"c%d","library_id":"%d","series_id":"tv:%d"}`, i, i, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

func TestUpdateNavigationShortcut(t *testing.T) {
	deps := pilotDeps(nil, nil)
	h := newTestHandler(t, deps)
	item := `{"type":"library","library_id":3,"label":"Movies"}`
	rec := do(t, h, http.MethodPut, "/api/v2/settings/values/nav.shortcuts/item", `{"item":`+item+`,"present":true}`, settingsOwner())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	// The answer is the stored nav.shortcuts document, as a setting value.
	want := `{"key":"nav.shortcuts","scope":"profile","profile_id":"p-owner","value":{"items":[{"type":"library","library_id":3,"label":"Movies"}]},"revision":1,"updated_at":"2026-01-02T03:04:05.678Z"}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, "/api/v2/settings/values/nav.shortcuts/item", `{"item":`+item+`,"present":false}`, settingsOwner())
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"value":{"items":[]}`)) {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	// present is required (v1 answered its absence the same way); the item
	// is judged by the contract's schema and rendered at body.item.
	p := requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/values/nav.shortcuts/item", `{"item":`+item+`}`, settingsOwner()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.present" || p.Errors[0].Code != codeRequired {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/values/nav.shortcuts/item", `{"item":{"type":"library"},"present":true}`, settingsOwner()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationBodyItem || p.Errors[0].Code != codeInvalid {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/values/nav.shortcuts/item", `{"item":"movies","present":true}`, settingsOwner()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/values/nav.shortcuts/item", `{"item":`+item+`,"present":true}`, bearer(memberToken)), TypeValidationFailed)
	// Exhausted compare-and-set retries: the seam's 409 setting_update_conflict
	// is the conflict problem type, and the operation documents it as a
	// retryable conflict.
	seam := deps.SettingValues.(*fakeSettingValuesSeam)
	contention := settingContentionError()
	seam.err = contention
	p = requireProblem(t, do(t, h, http.MethodPut, "/api/v2/settings/values/nav.shortcuts/item", `{"item":`+item+`,"present":true}`, settingsOwner()), TypeConflict)
	if p.Detail != contention.Message {
		t.Fatalf("detail = %q", p.Detail)
	}
	seam.err = nil
	requireProblem(t, do(t, newTestHandler(t, parityDeps(true)), http.MethodPut, "/api/v2/settings/values/nav.shortcuts/item", `{"item":`+item+`,"present":true}`, settingsOwner()), TypePermissionDenied)
}
