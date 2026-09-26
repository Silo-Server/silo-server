package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type fakeSourceWebhookLifecycle struct {
	calls []string
	err   error
}

func (f *fakeSourceWebhookLifecycle) view(action, id string) (handlers.AdminAutoscanSourceView, error) {
	f.calls = append(f.calls, action+":"+id)
	return handlers.AdminAutoscanSourceView{ID: id, WebhookConfigured: true, WebhookURL: "/api/v2/autoscan/webhooks/synthetic-token"}, f.err
}
func (f *fakeSourceWebhookLifecycle) CreateAdminAutoscanSourceWebhook(_ context.Context, id string) (handlers.AdminAutoscanSourceView, error) {
	return f.view("create", id)
}
func (f *fakeSourceWebhookLifecycle) RotateAdminAutoscanSourceWebhook(_ context.Context, id string) (handlers.AdminAutoscanSourceView, error) {
	return f.view("rotate", id)
}
func (f *fakeSourceWebhookLifecycle) DeleteAdminAutoscanSourceWebhook(_ context.Context, id string) error {
	_, err := f.view("delete", id)
	return err
}
func TestAdminSourceWebhookLifecycle(t *testing.T) {
	for _, tc := range []struct {
		action, method, suffix string
		status                 int
	}{{"create", "POST", "", 200}, {"rotate", "POST", "/rotate", 200}, {"delete", "DELETE", "", 204}} {
		t.Run(tc.action, func(t *testing.T) {
			f := new(fakeSourceWebhookLifecycle)
			deps := pilotDeps(nil, nil)
			deps.AdminSourceWebhookLifecycle = f
			h := NewHandler(deps)
			path := Prefix + "/admin/autoscan/sources/source-a/webhook" + tc.suffix
			requireProblem(t, do(t, h, tc.method, path, "", nil), TypeAuthenticationRequired)
			requireProblem(t, do(t, h, tc.method, path, "", bearer(memberToken)), TypePermissionDenied)
			if len(f.calls) != 0 {
				t.Fatal("unauthorized mutation", f.calls)
			}
			rec := do(t, h, tc.method, path, "", bearer(adminToken))
			if rec.Code != tc.status || len(f.calls) != 1 || f.calls[0] != tc.action+":source-a" {
				t.Fatal(rec.Code, rec.Body.String(), f.calls)
			}
			if tc.method == "DELETE" {
				if rec.Body.Len() != 0 {
					t.Fatal(rec.Body.String())
				}
			} else if !strings.Contains(rec.Body.String(), "/api/v2/autoscan/webhooks/synthetic-token") {
				t.Fatal(rec.Body.String())
			}
			for _, failure := range []struct {
				err    error
				status int
			}{{autoscan.ErrNotFound, 404}, {handlers.ErrAdminSourceWebhookMode, 422}, {handlers.ErrAdminSourceWebhookUnavailable, 503}, {errors.New("private-token-store"), 500}} {
				f.err = failure.err
				before := len(f.calls)
				rec = do(t, h, tc.method, path, "", bearer(adminToken))
				if rec.Code != failure.status || strings.Contains(rec.Body.String(), "private-token") || len(f.calls) != before+1 {
					t.Fatal(rec.Code, rec.Body.String(), f.calls)
				}
			}
			deps.AdminSourceWebhookLifecycle = nil
			requireProblem(t, do(t, NewHandler(deps), tc.method, path, "", bearer(adminToken)), TypeDependencyUnavailable)
		})
	}
}
