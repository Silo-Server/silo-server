package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Silo-Server/silo-server/internal/autoscan"
	"net/url"
	"strings"
	"testing"
)

type fakeAdminAutoscanAvailableSources struct {
	calls int
	rows  []autoscan.AvailableScanSource
	err   error
}

func (f *fakeAdminAutoscanAvailableSources) ReadAdminAutoscanAvailableSources(context.Context) ([]autoscan.AvailableScanSource, error) {
	f.calls++
	return f.rows, f.err
}
func TestAdminAutoscanAvailableSourcesRead(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminAutoscanAvailableSources{rows: []autoscan.AvailableScanSource{
		{PluginID: "same", CapabilityID: "b", DisplayName: "Second", Descriptor: autoscan.BuiltinArrWebhookSource().Descriptor},
		{PluginID: "same", CapabilityID: "a", DisplayName: "First", Descriptor: autoscan.DefaultScanSourceDescriptor()},
	}}
	deps.AdminAutoscanAvailableSources = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/scan-source-plugins"
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized read")
	}
	rec := do(t, h, "GET", path+"?limit=1", "", bearer(adminToken))
	var body Collection[AdminAutoscanAvailableSource]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Items) != 1 || body.Items[0].CapabilityID != "a" || body.Page == nil || !body.Page.HasMore {
		t.Fatal(rec.Code, rec.Body.String())
	}
	cursor := url.QueryEscape(body.Page.NextCursor)
	rec = do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	for _, want := range []string{`"capability_id":"b"`, `"connection":"none"`, `"control":"SELECT"`, `"value":"sonarr"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatal(want, rec.Code, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "source_config") {
		t.Fatal("credential reference exposed")
	}

	requireProblem(t, do(t, h, "GET", path+"?limit=2&cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", actingRequestAdmin), TypeInvalidCursor)
	f.rows = append(f.rows, f.rows[0])
	requireProblem(t, do(t, h, "GET", path, "", bearer(adminToken)), TypeInternalError)
	f.rows = nil
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.err = errors.New("private synthetic provider metadata")
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "private synthetic") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminAutoscanAvailableSources = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

func TestAdminAutoscanAvailableSourceFormDefaults(t *testing.T) {
	f := &fakeAdminAutoscanAvailableSources{rows: []autoscan.AvailableScanSource{{PluginID: "p", CapabilityID: "c", Descriptor: autoscan.ScanSourceDescriptor{DeliveryModes: []string{"poll"}, Connection: autoscan.ConnectionOptional, ConfigForm: &autoscan.AdminForm{Fields: []autoscan.AdminFormField{{Key: "value", Control: "FUTURE_CONTROL", DefaultValue: map[string]any{"nested": []any{false, 2, "x"}}, Secret: true, FillFrom: "library_paths_movie", Validation: &autoscan.AdminFormValidation{HasMin: true, Min: 0}}}}}}}}
	deps := pilotDeps(nil, nil)
	deps.AdminAutoscanAvailableSources = f
	rec := do(t, NewHandler(deps), "GET", Prefix+"/admin/autoscan/scan-source-plugins", "", bearer(adminToken))
	for _, want := range []string{`"default_value":{"nested":[false,2,"x"]}`, `"control":"FUTURE_CONTROL"`, `"fill_from":"library_paths_movie"`, `"secret":true`, `"has_min":true`, `"connection_kinds":[]`} {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Fatal(want, rec.Code, rec.Body.String())
		}
	}
}
