package apiv2

import (
	"context"
	"testing"
)

type fakeAdminSectionSettings struct {
	enabled bool
	calls   int
}

func (f *fakeAdminSectionSettings) AllowProfileCustomSections(context.Context) bool { return f.enabled }
func (f *fakeAdminSectionSettings) UpdateAdminSectionSettings(_ context.Context, enabled bool, guard func(bool) error) error {
	f.calls++
	if err := guard(f.enabled); err != nil {
		return err
	}
	f.enabled = enabled
	return nil
}
func TestAdminSectionSettingsGuard(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeAdminSectionSettings)
	deps.SectionFlags = f
	deps.AdminSectionSettingsWrite = f
	h := NewHandler(deps)
	path := Prefix + "/admin/settings/sections"
	requireProblem(t, do(t, h, "PUT", path, `{"allow_profile_custom_sections":true}`, bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized write")
	}
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	falseTag := rec.Header().Get("ETag")
	if rec.Code != 200 || falseTag == "" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	headers := bearer(adminToken)
	headers["If-None-Match"] = falseTag
	rec = do(t, h, "GET", path, "", headers)
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, "PUT", path, `{"allow_profile_custom_sections":true}`, bearer(adminToken)), TypePreconditionRequired)
	headers = bearer(adminToken)
	headers["If-Match"] = falseTag
	rec = do(t, h, "PUT", path, `{"allow_profile_custom_sections":true}`, headers)
	trueTag := rec.Header().Get("ETag")
	if rec.Code != 200 || !f.enabled || trueTag == falseTag {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = do(t, h, "PUT", path, `{"allow_profile_custom_sections":false}`, headers)
	if rec.Code != 412 || !f.enabled {
		t.Fatal("stale write was applied", rec.Code)
	}
	headers["If-Match"] = "*"
	headers["If-None-Match"] = trueTag
	rec = do(t, h, "PUT", path, `{"allow_profile_custom_sections":false}`, headers)
	if rec.Code != 412 || !f.enabled {
		t.Fatal("wildcard ignored exclusion", rec.Code)
	}
	delete(headers, "If-None-Match")
	requireProblem(t, do(t, h, "PUT", path, `{"allow_profile_custom_sections":null}`, headers), TypeValidationFailed)
	requireProblem(t, do(t, h, "PUT", path, `{}`, headers), TypeValidationFailed)
	rec = do(t, h, "PUT", path, `{"allow_profile_custom_sections":false}`, headers)
	if rec.Code != 200 || f.enabled {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminSectionSettingsWrite = nil
	requireProblem(t, do(t, NewHandler(deps), "PUT", path, `{"allow_profile_custom_sections":true}`, headers), TypeDependencyUnavailable)
}
