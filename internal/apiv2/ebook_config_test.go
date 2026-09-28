package apiv2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeEbookConfig struct {
	current *handlers.EbookReaderConfig
	calls   int
}

func (f *fakeEbookConfig) ReaderConfig(context.Context, int, string, string, catalogpkg.AccessFilter) (*handlers.EbookReaderConfig, error) {
	return f.current, nil
}
func (f *fakeEbookConfig) SaveReaderConfig(_ context.Context, config handlers.EbookReaderConfig, _ catalogpkg.AccessFilter, guard handlers.EbookConfigGuard) (*handlers.EbookReaderConfig, error) {
	f.calls++
	if err := guard(f.current); err != nil {
		return nil, err
	}
	config.UpdatedAt = time.Date(2026, 1, 2, 3, 4, 5, f.calls*1000000, time.UTC)
	f.current = &config
	return f.current, nil
}
func TestEbookConfigTransport(t *testing.T) {
	service := &fakeEbookConfig{}
	deps := pilotDeps(nil, nil)
	deps.EbookConfig = service
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	path := Prefix + "/ebooks/book/reader-config"
	rec := do(t, h, "GET", path, "", viewer)
	if rec.Code != 200 || rec.Header().Get("ETag") == "" || !strings.Contains(rec.Body.String(), `"config":{}`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	initial := rec.Header().Get("ETag")
	body := `{"config":{"theme":"dark","renderer":{"font":1},"plugin-setting":[true,12]}}`
	for _, tc := range []struct {
		name, body, match string
		want              int
	}{
		{"missing guard", body, "", 428},
		{"wrong guard", body, `"stale"`, 412},
		{"null configuration", `{"config":null}`, initial, 422},
		{"array configuration", `{"config":[]}`, initial, 422},
		{"create", body, initial, 200},
		{"stale retry", body, initial, 412},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := viewer
			if tc.match != "" {
				headers = with(viewer, "If-Match", tc.match)
			}
			rec := do(t, h, "PUT", path, tc.body, headers)
			if rec.Code != tc.want {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
	if service.current.UserID != 1 || service.current.ProfileID != "p-owner" || service.current.ContentID != "book" {
		t.Fatalf("scope: %+v", service.current)
	}
	var decoded map[string]any
	if err := json.Unmarshal(service.current.Config, &decoded); err != nil || decoded["renderer"] == nil {
		t.Fatalf("config extension: %s %v", service.current.Config, err)
	}
	rec = do(t, h, "GET", path, "", viewer)
	latest := rec.Header().Get("ETag")
	rec = do(t, h, "PUT", path, body, with(with(viewer, "If-Match", latest), "If-None-Match", "*"))
	if rec.Code != 412 {
		t.Fatalf("second precondition: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "PUT", path, `{"config":{"large":"`+strings.Repeat("x", 256<<10)+`"}}`, with(viewer, "If-Match", latest))
	if rec.Code != 413 {
		t.Fatalf("body cap: %d", rec.Code)
	}
}
