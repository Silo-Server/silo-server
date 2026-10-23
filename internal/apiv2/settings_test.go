package apiv2

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

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
