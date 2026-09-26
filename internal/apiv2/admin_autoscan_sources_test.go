package apiv2

import (
	"context"
	"encoding/json"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeAdminAutoscanSources struct{ calls int }

func (f *fakeAdminAutoscanSources) ReadAdminAutoscanSources(context.Context) ([]handlers.AdminAutoscanSourceView, error) {
	f.calls++
	at := time.Date(2026, 9, 1, 0, 0, 0, 123456789, time.UTC)
	return []handlers.AdminAutoscanSourceView{{ID: "b", Label: "same", DeliveryMode: "webhook", WebhookConfigured: true, WebhookURL: "https://server.example.test/silo/api/v2/autoscan/webhooks/existing-token", WebhookLastReceivedAt: &at, SourceConfig: map[string]string{}}, {ID: "a", Label: "same", DeliveryMode: "poll", SourceConfig: map[string]string{}}}, nil
}
func TestAdminAutoscanSourcesRead(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeAdminAutoscanSources)
	deps.AdminAutoscanSources = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/sources"
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized read")
	}
	rec := do(t, h, "GET", path+"?limit=1", "", bearer(adminToken))
	var body Collection[AdminAutoscanSource]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Items) != 1 || body.Items[0].ID != "a" || body.Page == nil || !body.Page.HasMore {
		t.Fatal(rec.Code, rec.Body.String())
	}
	cursor := url.QueryEscape(body.Page.NextCursor)
	rec = do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	for _, want := range []string{`"id":"b"`, `https://server.example.test/silo/api/v2/autoscan/webhooks/existing-token`, `"webhook_last_received_at":"2026-09-01T00:00:00.123Z"`, `"path_rewrites":[]`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatal(want, rec.Code, rec.Body.String())
		}
	}
	requireProblem(t, do(t, h, "GET", path+"?limit=2&cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", actingRequestAdmin), TypeInvalidCursor)
	deps.AdminAutoscanSources = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}
