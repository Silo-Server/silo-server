package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakeRepositoryCreate struct {
	inputs []plugins.CreateRepositoryInput
	err    error
}

func (f *fakeRepositoryCreate) Create(_ context.Context, in plugins.CreateRepositoryInput) (*plugins.Repository, error) {
	f.inputs = append(f.inputs, in)
	if f.err != nil {
		return nil, f.err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return &plugins.Repository{ID: 7, URL: in.URL, DisplayName: in.DisplayName, Enabled: enabled, SourceKind: "external", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 123456789, time.UTC), UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 123456789, time.UTC)}, nil
}
func TestAdminPluginRepositoryCreate(t *testing.T) {
	f := new(fakeRepositoryCreate)
	deps := pilotDeps(nil, nil)
	deps.AdminPluginRepositoryCreation = f
	h := NewHandler(deps)
	path := Prefix + "/admin/plugins/repositories"
	body := `{"url":"https://example.invalid/index.json","display_name":"Synthetic"}`
	requireProblem(t, do(t, h, "POST", path, body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "POST", path, body, bearer(memberToken)), TypePermissionDenied)
	for _, invalid := range []string{`{"url":" ","display_name":"x"}`, `{"url":"https://example.invalid","display_name":" "}`, `{"url":"` + plugins.DefaultRepositoryURL + `","display_name":"x"}`, `{"url":"` + plugins.ApprovedCommunityRepositoryURL + `","display_name":"x"}`} {
		requireProblem(t, do(t, h, "POST", path, invalid, bearer(adminToken)), TypeValidationFailed)
	}
	if len(f.inputs) != 0 {
		t.Fatal("invalid request wrote store")
	}
	rec := do(t, h, "POST", path, body, bearer(adminToken))
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"id":"7"`) || !strings.Contains(rec.Body.String(), `"enabled":true`) || !strings.Contains(rec.Body.String(), "2026-09-01T00:00:00.123Z") || len(f.inputs) != 1 || f.inputs[0].Enabled != nil {
		t.Fatal(rec.Code, rec.Body.String(), f.inputs)
	}
	rec = do(t, h, "POST", path, `{"url":"https://example.invalid","display_name":"Disabled","enabled":false}`, bearer(adminToken))
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.err = errors.New("private credential-bearing store failure")
	rec = do(t, h, "POST", path, body, bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "credential-bearing") || len(f.inputs) != 3 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminPluginRepositoryCreation = nil
	requireProblem(t, do(t, NewHandler(deps), "POST", path, body, bearer(adminToken)), TypeDependencyUnavailable)
}
