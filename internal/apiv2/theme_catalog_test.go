package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeThemeCatalog struct {
	catalog, file               []byte
	stale                       bool
	loads, refreshes, downloads int
	url                         string
	err                         error
}

func (f *fakeThemeCatalog) LoadThemeCatalog(context.Context) (*handlers.ThemeCatalogResult, error) {
	f.loads++
	return &handlers.ThemeCatalogResult{Body: f.catalog, Stale: f.stale}, f.err
}
func (f *fakeThemeCatalog) RefreshThemeCatalog(context.Context) (*handlers.ThemeCatalogResult, error) {
	f.refreshes++
	return &handlers.ThemeCatalogResult{Body: f.catalog}, f.err
}
func (f *fakeThemeCatalog) DownloadThemeFile(_ context.Context, target string) ([]byte, error) {
	f.downloads++
	f.url = target
	return f.file, f.err
}
func fixtureThemeCatalog() *fakeThemeCatalog {
	return &fakeThemeCatalog{catalog: []byte(`{"version":1,"themes":[{"id":"fixture","name":"Fixture","downloadUrl":"https://themes.example/theme.json"}]}`), file: []byte(`{"version":1,"name":"Fixture","baseTheme":"midnight-cinema","vars":{"--primary":"red"},"customCss":"body { color: red; }"}`)}
}
func themeCatalogHandler(f *fakeThemeCatalog) http.Handler {
	deps := pilotDeps(nil, nil)
	if f != nil {
		deps.ThemeCatalog = f
	}
	return NewHandler(deps)
}
func TestThemeCatalogTypedDocumentsAndAuthority(t *testing.T) {
	f := fixtureThemeCatalog()
	f.stale = true
	h := themeCatalogHandler(f)
	read := do(t, h, "GET", Prefix+"/theme/catalog", "", bearer(memberToken))
	var catalog ThemeCatalogResponse
	if err := json.Unmarshal(read.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if read.Code != 200 || !catalog.Stale || len(catalog.Document.Themes) != 1 || catalog.Document.Themes[0].Tags == nil {
		t.Fatal(read.Code, read.Body.String())
	}
	// Catalog/download match the old account-only routes, even with a selected
	// PIN-locked profile. Only refresh requires acting-admin authority.
	pinned := do(t, h, "GET", Prefix+"/theme/catalog", "", with(bearer(memberToken), "X-Profile-Id", "p-locked"))
	if pinned.Code != 200 {
		t.Fatal(pinned.Code, pinned.Body.String())
	}
	requireProblem(t, do(t, h, "POST", Prefix+"/theme/catalog/refresh", "", requestOwner), TypePermissionDenied)
	requireProblem(t, do(t, h, "GET", Prefix+"/theme/catalog", "", nil), TypeAuthenticationRequired)
	refreshed := do(t, h, "POST", Prefix+"/theme/catalog/refresh", "", actingRequestAdmin)
	if refreshed.Code != 200 || f.refreshes != 1 {
		t.Fatal(refreshed.Code, refreshed.Body.String(), f.refreshes)
	}
	target := "https://themes.example/theme.json"
	downloaded := do(t, h, "GET", Prefix+"/theme/download?url="+url.QueryEscape(target), "", bearer(memberToken))
	var file ThemeDownloadResponse
	if err := json.Unmarshal(downloaded.Body.Bytes(), &file); err != nil {
		t.Fatal(err)
	}
	if downloaded.Code != 200 || f.url != target || file.Document.BaseTheme != "midnight-cinema" || file.Document.Vars["--primary"] != "red" || file.Document.CustomCSS != "body { color: red; }" {
		t.Fatal(downloaded.Code, downloaded.Body.String())
	}
}
func TestThemeCatalogErrorsAndPortableValidation(t *testing.T) {
	f := fixtureThemeCatalog()
	h := themeCatalogHandler(f)
	requireProblem(t, do(t, h, "GET", Prefix+"/theme/download", "", bearer(memberToken)), TypeValidationFailed)
	if f.downloads != 0 {
		t.Fatal("missing URL reached service")
	}
	path := Prefix + "/theme/download?url=" + url.QueryEscape("https://themes.example/theme.json")
	for _, tc := range []struct {
		status int
		want   ProblemType
	}{{400, TypeValidationFailed}, {403, TypePermissionDenied}, {502, TypeDependencyUnavailable}, {503, TypeDependencyUnavailable}} {
		f.err = &handlers.APIError{Status: tc.status, Code: "synthetic", Message: "Upstream failure"}
		requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), tc.want)
	}
	f.err = nil
	for _, body := range []string{`null`, `[]`, `{"version":1,"name":"","baseTheme":"dark"}`, `{"version":1,"name":"Fixture","baseTheme":"midnight-cinema","vars":[]}`, `{"version":1,"themes":"invalid"}`} {
		f.file = []byte(body)
		requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypeDependencyUnavailable)
	}
	f.file = append([]byte(`{"version":1,"name":"Fixture","baseTheme":"dark"}`), []byte(strings.Repeat(" ", handlers.ThemeFileLimit-len(`{"version":1,"name":"Fixture","baseTheme":"dark"}`)))...)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypeDependencyUnavailable)
	f.catalog = []byte(`{"version":1,"themes":[null]}`)
	requireProblem(t, do(t, h, "GET", Prefix+"/theme/catalog", "", bearer(memberToken)), TypeDependencyUnavailable)
	f.catalog = []byte(`{"version":1,"themes":"invalid"}`)
	requireProblem(t, do(t, h, "GET", Prefix+"/theme/catalog", "", bearer(memberToken)), TypeDependencyUnavailable)
	f.catalog = append([]byte(`{"version":1,"themes":[]}`), []byte(strings.Repeat(" ", handlers.ThemeCatalogLimit-len(`{"version":1,"themes":[]}`)))...)
	requireProblem(t, do(t, h, "GET", Prefix+"/theme/catalog", "", bearer(memberToken)), TypeDependencyUnavailable)
	absent := themeCatalogHandler(nil)
	requireProblem(t, do(t, absent, "GET", Prefix+"/theme/catalog", "", bearer(memberToken)), TypeDependencyUnavailable)
	capability := do(t, absent, "GET", Prefix+"/theme/catalog/capabilities", "", bearer(memberToken))
	if capability.Code != 200 || !strings.Contains(capability.Body.String(), `"available":false`) {
		t.Fatal(capability.Code, capability.Body.String())
	}
}
