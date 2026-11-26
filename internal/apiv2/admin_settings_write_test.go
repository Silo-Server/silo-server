package apiv2

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakeAdminSettingsWrite struct {
	fakeAdminSettingsInspection
	stored map[string]string
	writes int
}

func (f *fakeAdminSettingsWrite) InspectAdminSettingsSnapshot(context.Context) (handlers.AdminSettingsSnapshot, error) {
	return f.SnapshotAdminSettings(f.stored), nil
}

func TestAdminSettingsValidatorIgnoresRecoveryState(t *testing.T) {
	f := &fakeAdminSettingsWrite{stored: map[string]string{
		"server.log_level":                          "info",
		config.StorageTransitionTargetKey:           "running:10",
		config.ArtworkStorageReconcileCheckpointKey: "checkpoint:1",
	}}
	deps := pilotDeps(nil, nil)
	deps.CursorSecret = []byte("synthetic-settings-test-secret")
	deps.AdminSettingsInspection = f
	deps.AdminSettingsWrite = f
	h := NewHandler(deps)
	path := Prefix + "/admin/settings"
	read := do(t, h, "GET", path+"/effective", "", bearer(adminToken))
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" {
		t.Fatalf("initial settings read: status=%d tag=%q body=%s", read.Code, tag, read.Body.String())
	}

	f.stored[config.StorageTransitionTargetKey] = "running:20"
	f.stored[config.ArtworkStorageReconcileCheckpointKey] = "checkpoint:2"
	read = do(t, h, "GET", path+"/effective", "", bearer(adminToken))
	if read.Code != 200 || read.Header().Get("ETag") != tag {
		t.Fatalf("recovery state changed settings validator: status=%d old=%q new=%q", read.Code, tag, read.Header().Get("ETag"))
	}

	headers := with(bearer(adminToken), "If-Match", tag)
	saved := do(t, h, "PUT", path, `{"values":{"server.log_level":"debug"}}`, headers)
	if saved.Code != 200 || f.stored["server.log_level"] != "debug" || f.writes != 1 {
		t.Fatalf("save after recovery progress: status=%d writes=%d body=%s", saved.Code, f.writes, saved.Body.String())
	}
	read = do(t, h, "GET", path+"/effective", "", bearer(adminToken))
	newTag := read.Header().Get("ETag")
	if newTag == "" || newTag == tag {
		t.Fatalf("admin edit did not advance validator: old=%q new=%q", tag, newTag)
	}

	f.stored["server.log_level"] = "error"
	stale := do(t, h, "PUT", path+"/server.log_level", `{"value":"warn"}`, with(bearer(adminToken), "If-Match", newTag))
	requireProblem(t, stale, TypePreconditionFailed)
	if f.stored["server.log_level"] != "error" || f.writes != 1 {
		t.Fatal("stale admin edit overwrote a newer setting")
	}
}
func (f *fakeAdminSettingsWrite) UpdateAdminSettings(ctx context.Context, values map[string]string, guard func(handlers.AdminSettingsSnapshot) error) (handlers.AdminSettingsUpdateResult, error) {
	snapshot, _ := f.InspectAdminSettingsSnapshot(ctx)
	if err := guard(snapshot); err != nil {
		return handlers.AdminSettingsUpdateResult{}, err
	}
	maps.Copy(f.stored, values)
	f.writes++
	return handlers.AdminSettingsUpdateResult{Values: values}, nil
}
func (f *fakeAdminSettingsWrite) UpdateAdminSetting(ctx context.Context, key, value string, guard func(handlers.AdminSettingsSnapshot) error) (handlers.AdminSettingUpdateResult, error) {
	_, err := f.UpdateAdminSettings(ctx, map[string]string{key: value}, guard)
	return handlers.AdminSettingUpdateResult{}, err
}
func TestAdminSettingsWriteSnapshotGuard(t *testing.T) {
	f := &fakeAdminSettingsWrite{stored: map[string]string{"server.log_level": "info", "email.smtp_password": "private-value"}}
	deps := pilotDeps(nil, nil)
	deps.CursorSecret = []byte("synthetic-settings-test-secret")
	deps.AdminSettingsInspection = f
	deps.AdminSettingsWrite = f
	h := NewHandler(deps)
	path := Prefix + "/admin/settings"
	read := do(t, h, "GET", path+"/effective", "", bearer(adminToken))
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" || strings.Contains(read.Body.String(), "private-value") || strings.Contains(read.Body.String(), "smtp_password") {
		t.Fatal(read.Code, read.Body.String())
	}
	headers := bearer(adminToken)
	headers["If-Match"] = tag
	requireProblem(t, do(t, h, "PUT", path, `{"values":{"server.log_level":"debug"}}`, bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "PUT", path, `{"values":{}}`, bearer(adminToken)), TypePreconditionRequired)
	for _, body := range []string{`{}`, `{"values":null}`, `{"values":{"server.log_level":null}}`} {
		requireProblem(t, do(t, h, "PUT", path, body, headers), TypeValidationFailed)
	}
	for _, body := range []string{`{}`, `{"value":null}`} {
		requireProblem(t, do(t, h, "PUT", path+"/server.log_level", body, headers), TypeValidationFailed)
	}
	if f.writes != 0 {
		t.Fatal("invalid or unauthorized request wrote settings")
	}
	// Hidden secret edits must invalidate even when the displayed map is equal.
	f.stored["email.smtp_password"] = "replacement-private-value"
	requireProblem(t, do(t, h, "PUT", path, `{"values":{"server.log_level":"debug"}}`, headers), TypePreconditionFailed)
	if f.writes != 0 {
		t.Fatal("stale request wrote settings")
	}
	read = do(t, h, "GET", path, "", bearer(adminToken))
	if read.Header().Get("ETag") == tag {
		t.Fatal("secret change did not invalidate")
	}
	headers["If-Match"] = read.Header().Get("ETag")
	saved := do(t, h, "PUT", path+"/server.log_level", `{"value":"debug"}`, headers)
	if saved.Code != 200 || f.stored["server.log_level"] != "debug" || f.writes != 1 {
		t.Fatal(saved.Code, saved.Body.String())
	}
}

func (f *fakeAdminSettingsWrite) SnapshotAdminSettings(stored map[string]string) handlers.AdminSettingsSnapshot {
	return (&handlers.AdminHandler{}).SnapshotAdminSettings(stored)
}

// refusingSettingsWrite refuses every write with err, as the settings
// service does for a refused auth.local_password_login change.
type refusingSettingsWrite struct {
	fakeAdminSettingsWrite
	err error
}

func (f *refusingSettingsWrite) UpdateAdminSettings(context.Context, map[string]string, func(handlers.AdminSettingsSnapshot) error) (handlers.AdminSettingsUpdateResult, error) {
	return handlers.AdminSettingsUpdateResult{}, f.err
}

func (f *refusingSettingsWrite) UpdateAdminSetting(context.Context, string, string, func(handlers.AdminSettingsSnapshot) error) (handlers.AdminSettingUpdateResult, error) {
	return handlers.AdminSettingUpdateResult{}, f.err
}

// The two refusals of turning password sign-in off reach clients as their
// own problem types, so the Sign-in page can tell "add a break-glass admin"
// from "turn a provider on" instead of showing a generic conflict.
func TestLocalLoginRefusalProblemTypes(t *testing.T) {
	for _, tc := range []struct {
		code string
		want ProblemType
	}{
		{"break_glass_required", TypeBreakGlassRequired},
		{"sign_in_provider_required", TypeSignInProviderRequired},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := &refusingSettingsWrite{err: &handlers.APIError{Status: 409, Code: tc.code, Message: "refused"}}
			deps := pilotDeps(nil, nil)
			deps.CursorSecret = []byte("synthetic-settings-test-secret")
			deps.AdminSettingsInspection = f
			deps.AdminSettingsWrite = f
			h := NewHandler(deps)
			headers := with(bearer(adminToken), "If-Match", "*")
			requireProblem(t, do(t, h, "PUT", Prefix+"/admin/settings", `{"values":{"auth.local_password_login":"false"}}`, headers), tc.want)
			requireProblem(t, do(t, h, "PUT", Prefix+"/admin/settings/auth.local_password_login", `{"value":"false"}`, headers), tc.want)
		})
	}
}

// Taking away the last sign-in provider while password sign-in is off is
// refused with the same problem type the settings write uses, so the web's
// plugin and sign-in pages can explain it.
func TestPluginWriteSignInProviderRefusal(t *testing.T) {
	p, ok := errors.AsType[*Problem](adminPluginMutationProblem(fmt.Errorf("disabling: %w", auth.ErrSignInProviderRequired)))
	if !ok || p.Type != TypeSignInProviderRequired.URI() || p.Status != http.StatusConflict {
		t.Fatalf("problem = %+v", p)
	}
}

// An uninstall that found the plugin's sign-in identities busy changed
// nothing; clients get the server-busy answer with a retry delay, and repeat.
func TestPluginUninstallBusyIsRetryable(t *testing.T) {
	p, ok := errors.AsType[*Problem](adminPluginMutationProblem(plugins.ErrInstallationBusy))
	if !ok || p.Type != TypeDependencyUnavailable.URI() || p.Status != http.StatusServiceUnavailable || p.headers.Get("Retry-After") == "" {
		t.Fatalf("problem = %+v", p)
	}
}
