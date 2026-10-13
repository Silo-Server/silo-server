package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

func decodeVirtualFolders(t *testing.T, rec *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var folders []virtualFolderDTO
	if err := json.NewDecoder(rec.Body).Decode(&folders); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	out := make(map[string]bool, len(folders))
	for _, f := range folders {
		out[f.Name] = f.LibraryOptions.EnableRealtimeMonitor
	}
	return out
}

func TestVirtualFoldersReportRealtimeMonitorConfiguration(t *testing.T) {
	serverOn := true
	codec := NewResourceIDCodec()
	h := &ItemsHandler{
		content: &librariesContentService{libraries: []upstreamUserLibrary{
			{ID: 1, Name: "Movies", Type: "movies", RealtimeMonitoring: true},
			{ID: 2, Name: "Shows", Type: "series", RealtimeMonitoring: false},
		}},
		userData:           &mockUserDataService{},
		codec:              codec,
		mapper:             newMapper(codec, &config.Config{}),
		images:             NewImageCache(time.Hour, time.Now),
		realtimeMonitoring: func() bool { return serverOn },
	}
	request := func() map[string]bool {
		req := httptest.NewRequest(http.MethodGet, "/Library/VirtualFolders", nil)
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
		rec := httptest.NewRecorder()
		h.HandleVirtualFolders(rec, req)
		return decodeVirtualFolders(t, rec)
	}

	got := request()
	if !got["Movies"] || got["Shows"] {
		t.Fatalf("server on: EnableRealtimeMonitor = %v, want Movies true, Shows false", got)
	}
	// The server switch is read live on every request.
	serverOn = false
	got = request()
	if got["Movies"] || got["Shows"] {
		t.Fatalf("server off: EnableRealtimeMonitor = %v, want all false", got)
	}
	h.realtimeMonitoring = nil
	got = request()
	if !got["Movies"] || got["Shows"] {
		t.Fatalf("default server switch: EnableRealtimeMonitor = %v, want Movies true, Shows false", got)
	}
}

func TestAutoscanVirtualFoldersReportRealtimeMonitorConfiguration(t *testing.T) {
	serverOn := true
	handler := NewAutoscanHandler(&fakeAutoscanFolders{folders: []*models.MediaFolder{
		{ID: 1, Name: "Movies", Type: "movie", Enabled: true, RealtimeMonitoring: true, Paths: []string{t.TempDir()}},
		{ID: 2, Name: "Shows", Type: "series", Enabled: true, RealtimeMonitoring: false, Paths: []string{t.TempDir()}},
	}}, nil, NewResourceIDCodec(), nil)
	handler.realtimeMonitoring = func() bool { return serverOn }
	request := func() map[string]bool {
		req := httptest.NewRequest(http.MethodGet, "/Library/VirtualFolders", nil)
		req = req.WithContext(context.WithValue(req.Context(), adminAPIKeyKey, true))
		rec := httptest.NewRecorder()
		handler.HandleVirtualFolders(rec, req)
		return decodeVirtualFolders(t, rec)
	}

	got := request()
	if !got["Movies"] || got["Shows"] {
		t.Fatalf("server on: EnableRealtimeMonitor = %v, want Movies true, Shows false", got)
	}
	serverOn = false
	got = request()
	if got["Movies"] || got["Shows"] {
		t.Fatalf("server off: EnableRealtimeMonitor = %v, want all false", got)
	}
}

func TestDependenciesRealtimeMonitoringEnabledReadsLiveConfig(t *testing.T) {
	var deps Dependencies
	if !deps.RealtimeMonitoringEnabled() {
		t.Fatal("no config: RealtimeMonitoringEnabled = false, want the default true")
	}

	deps.Config = &config.Config{Scanner: config.ScannerConfig{RealtimeMonitoring: true}}
	if !deps.RealtimeMonitoringEnabled() {
		t.Fatal("startup config on: RealtimeMonitoringEnabled = false")
	}

	live := &config.Config{Scanner: config.ScannerConfig{RealtimeMonitoring: false}}
	deps.LiveConfig = func() *config.Config { return live }
	if deps.RealtimeMonitoringEnabled() {
		t.Fatal("live config off: RealtimeMonitoringEnabled = true, want the live value")
	}
	live = &config.Config{Scanner: config.ScannerConfig{RealtimeMonitoring: true}}
	if !deps.RealtimeMonitoringEnabled() {
		t.Fatal("live config back on: RealtimeMonitoringEnabled = false")
	}
}
