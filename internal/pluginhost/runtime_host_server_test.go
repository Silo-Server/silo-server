package pluginhost_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type fakeHub struct {
	calls []events.Envelope
}

func (f *fakeHub) Publish(_ context.Context, env events.Envelope) error {
	f.calls = append(f.calls, env)
	return nil
}

type fakeLibLister struct{ libs []pluginhost.LibraryRecord }

func (f *fakeLibLister) ListLibraries(_ context.Context, _ string) ([]pluginhost.LibraryRecord, error) {
	return f.libs, nil
}

func TestRuntimeHostServer_PublishEvent_AutoPrefixesAndPublishes(t *testing.T) {
	hub := &fakeHub{}
	srv := pluginhost.NewRuntimeHostServer(hub, &fakeLibLister{}, "silo.example")

	payload, _ := structpb.NewStruct(map[string]any{"foo": "bar"})
	_, err := srv.PublishEvent(context.Background(), &pluginv1.PublishEventRequest{
		EventName: "approved",
		Payload:   payload,
	})
	if err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	if len(hub.calls) != 1 {
		t.Fatalf("hub calls = %d, want 1", len(hub.calls))
	}
	got := hub.calls[0]
	if got.Channel != events.ChannelPlugins {
		t.Errorf("channel = %q, want %q", got.Channel, events.ChannelPlugins)
	}
	if got.Event != "plugin.silo.example.approved" {
		t.Errorf("event = %q, want %q", got.Event, "plugin.silo.example.approved")
	}
}

func TestRuntimeHostServer_PublishEvent_RejectsEmptyName(t *testing.T) {
	hub := &fakeHub{}
	srv := pluginhost.NewRuntimeHostServer(hub, &fakeLibLister{}, "silo.example")

	_, err := srv.PublishEvent(context.Background(), &pluginv1.PublishEventRequest{EventName: ""})
	if err == nil {
		t.Fatal("expected error for empty event name")
	}
	if len(hub.calls) != 0 {
		t.Errorf("hub.calls = %d, want 0", len(hub.calls))
	}
}

func TestRuntimeHostServer_PublishEventTo_AutoPrefixesAndTargets(t *testing.T) {
	hub := &fakeHub{}
	srv := pluginhost.NewRuntimeHostServer(hub, &fakeLibLister{}, "silo.example")

	payload, _ := structpb.NewStruct(map[string]any{"foo": "bar"})
	_, err := srv.PublishEventTo(context.Background(), &pluginv1.PublishEventToRequest{
		TargetPluginId: "silo.requests",
		EventName:      "approved",
		Payload:        payload,
	})
	if err != nil {
		t.Fatalf("PublishEventTo: %v", err)
	}
	if len(hub.calls) != 1 {
		t.Fatalf("hub calls = %d, want 1", len(hub.calls))
	}
	got := hub.calls[0]
	if got.Event != "plugin.silo.example.approved" {
		t.Errorf("event = %q, want plugin.silo.example.approved", got.Event)
	}
	if got.TargetPluginID != "silo.requests" {
		t.Errorf("target_plugin_id = %q, want silo.requests", got.TargetPluginID)
	}
}

func TestRuntimeHostServer_PublishEventTo_RejectsEmptyTarget(t *testing.T) {
	hub := &fakeHub{}
	srv := pluginhost.NewRuntimeHostServer(hub, &fakeLibLister{}, "silo.example")

	_, err := srv.PublishEventTo(context.Background(), &pluginv1.PublishEventToRequest{EventName: "approved"})
	if err == nil {
		t.Fatal("expected error for empty target_plugin_id")
	}
	if len(hub.calls) != 0 {
		t.Errorf("hub.calls = %d, want 0", len(hub.calls))
	}
}

func TestRuntimeHostServer_ListLibraries_PassesUserID(t *testing.T) {
	libs := &fakeLibLister{libs: []pluginhost.LibraryRecord{
		{ID: "lib-1", Name: "Movies", MediaType: "movie"},
		{ID: "lib-2", Name: "Shows", MediaType: "tv"},
	}}
	srv := pluginhost.NewRuntimeHostServer(&fakeHub{}, libs, "silo.example")

	resp, err := srv.ListLibraries(context.Background(), &pluginv1.ListLibrariesRequest{UserId: "u1"})
	if err != nil {
		t.Fatalf("ListLibraries: %v", err)
	}
	if len(resp.GetLibraries()) != 2 {
		t.Errorf("got %d libraries, want 2", len(resp.GetLibraries()))
	}
	if resp.GetLibraries()[0].GetId() != "lib-1" {
		t.Errorf("first lib id = %q", resp.GetLibraries()[0].GetId())
	}
}

type fakeInstalledPluginLister struct {
	rows []pluginhost.InstalledPluginRecord
}

func (f *fakeInstalledPluginLister) ListInstalledPlugins(context.Context) ([]pluginhost.InstalledPluginRecord, error) {
	return f.rows, nil
}

func TestRuntimeHostServer_ListInstalledPlugins_ReturnsPlugins(t *testing.T) {
	lister := &fakeInstalledPluginLister{rows: []pluginhost.InstalledPluginRecord{
		{
			InstallationID: 42,
			PluginID:       "silo.requests",
			Version:        "0.1.0",
			Enabled:        true,
			Capabilities: []*pluginv1.CapabilityDescriptor{
				{Type: "request_router.v1", Id: "default"},
			},
		},
	}}
	srv := pluginhost.NewRuntimeHostServerWithServices(
		&fakeHub{}, &fakeLibLister{}, nil, lister, nil, nil, "silo.example", 7,
	)

	resp, err := srv.ListInstalledPlugins(context.Background(), &pluginv1.ListInstalledPluginsRequest{})
	if err != nil {
		t.Fatalf("ListInstalledPlugins: %v", err)
	}
	if len(resp.GetPlugins()) != 1 {
		t.Fatalf("plugins = %d, want 1", len(resp.GetPlugins()))
	}
	got := resp.GetPlugins()[0]
	if got.GetInstallationId() != 42 || got.GetPluginId() != "silo.requests" || !got.GetEnabled() {
		t.Errorf("plugin = %+v", got)
	}
	if got.GetCapabilities()[0].GetType() != "request_router.v1" {
		t.Errorf("capability = %+v", got.GetCapabilities()[0])
	}
}

type fakeConfigSetter struct {
	installationID int
	key            string
	value          map[string]any
}

type fakeVirtualCatalog struct{ installationID int }

func (f *fakeVirtualCatalog) UpsertVirtualMedia(_ context.Context, installationID int, req *pluginv1.UpsertVirtualMediaRequest) (*pluginv1.UpsertVirtualMediaResponse, error) {
	f.installationID = installationID
	return &pluginv1.UpsertVirtualMediaResponse{MediaId: "movie-tmdb-42", LibraryId: req.GetLibraryId()}, nil
}

func TestRuntimeHostServerUpsertVirtualMediaUsesBoundInstallation(t *testing.T) {
	registrar := &fakeVirtualCatalog{}
	srv := pluginhost.NewRuntimeHostServerWithServices(&fakeHub{}, &fakeLibLister{}, nil, nil, nil, registrar, "virtual.plugin", 77)
	resp, err := srv.UpsertVirtualMedia(context.Background(), &pluginv1.UpsertVirtualMediaRequest{LibraryId: "1", MediaType: "movie", Title: "Example", VirtualUri: "aiostreams://movie/tt1"})
	if err != nil {
		t.Fatal(err)
	}
	if registrar.installationID != 77 {
		t.Fatalf("installation=%d", registrar.installationID)
	}
	if resp.GetMediaId() != "movie-tmdb-42" {
		t.Fatalf("media=%q", resp.GetMediaId())
	}
}

func (f *fakeConfigSetter) SetGlobalConfigEntry(_ context.Context, installationID int, key string, value map[string]any) error {
	f.installationID = installationID
	f.key = key
	f.value = value
	return nil
}

func TestRuntimeHostServer_SetGlobalConfigEntry_PassesInstallationKeyAndValue(t *testing.T) {
	setter := &fakeConfigSetter{}
	srv := pluginhost.NewRuntimeHostServerWithServices(
		&fakeHub{}, &fakeLibLister{}, nil, nil, setter, nil, "silo.example", 42,
	)
	value, _ := structpb.NewStruct(map[string]any{"baseUrl": "https://example.test"})

	_, err := srv.SetGlobalConfigEntry(context.Background(), &pluginv1.SetGlobalConfigEntryRequest{
		Key:   "connection",
		Value: value,
	})
	if err != nil {
		t.Fatalf("SetGlobalConfigEntry: %v", err)
	}
	if setter.installationID != 42 {
		t.Errorf("installationID = %d, want 42", setter.installationID)
	}
	if setter.key != "connection" {
		t.Errorf("key = %q, want connection", setter.key)
	}
	if got := setter.value["baseUrl"]; got != "https://example.test" {
		t.Errorf("baseUrl = %#v, want https://example.test", got)
	}
}

func TestRuntimeHostServer_SetGlobalConfigEntry_RejectsEmptyKey(t *testing.T) {
	setter := &fakeConfigSetter{}
	srv := pluginhost.NewRuntimeHostServerWithServices(
		&fakeHub{}, &fakeLibLister{}, nil, nil, setter, nil, "silo.example", 42,
	)

	_, err := srv.SetGlobalConfigEntry(context.Background(), &pluginv1.SetGlobalConfigEntryRequest{})
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	if setter.installationID != 0 {
		t.Errorf("setter was called for empty key")
	}
}

func TestRuntimeHostServer_PublishEvent_RateLimited(t *testing.T) {
	hub := &fakeHub{}
	srv := pluginhost.NewRuntimeHostServerWithRate(hub, &fakeLibLister{}, "silo.example", 2)

	payload, _ := structpb.NewStruct(map[string]any{"i": float64(1)})
	for i := 0; i < 5; i++ {
		_, _ = srv.PublishEvent(context.Background(), &pluginv1.PublishEventRequest{
			EventName: "ping",
			Payload:   payload,
		})
	}
	// rate.NewLimiter(2, 2) → burst=2 + ~0 tokens replenished within
	// microsecond test runtime → 2-3 calls succeed, rest are rate-limited.
	if got := len(hub.calls); got > 3 {
		t.Errorf("hub.calls = %d, expected at most 3 with rate=2", got)
	}
	if len(hub.calls) == 0 {
		t.Errorf("expected at least the burst to succeed")
	}
}

type fakeCatalog struct {
	matches []pluginhost.LibraryPresenceRecord
	gotIDs  []string
	gotType string
	gotProv string
}

func (f *fakeCatalog) LookupByExternalIDs(_ context.Context, provider, mediaType string, ids []string) ([]pluginhost.LibraryPresenceRecord, error) {
	f.gotProv = provider
	f.gotType = mediaType
	f.gotIDs = append(f.gotIDs, ids...)
	return f.matches, nil
}

func TestRuntimeHostServer_CheckMediaPresence_Returns(t *testing.T) {
	cat := &fakeCatalog{matches: []pluginhost.LibraryPresenceRecord{
		{ExternalID: "603", MediaID: "m-1", LibraryID: "lib-1", Title: "The Matrix"},
	}}
	srv := pluginhost.NewRuntimeHostServerWithCatalog(&fakeHub{}, &fakeLibLister{}, cat, "silo.example")

	resp, err := srv.CheckMediaPresence(context.Background(), &pluginv1.CheckMediaPresenceRequest{
		Provider:  "tmdb",
		MediaType: "movie",
		Ids:       []string{"603", "550"},
	})
	if err != nil {
		t.Fatalf("CheckMediaPresence: %v", err)
	}
	if len(resp.GetPresent()) != 1 {
		t.Fatalf("got %d, want 1", len(resp.GetPresent()))
	}
	if resp.GetPresent()[0].GetExternalId() != "603" {
		t.Errorf("got %+v", resp.GetPresent()[0])
	}
	if cat.gotProv != "tmdb" || cat.gotType != "movie" {
		t.Errorf("delegate: prov=%q type=%q", cat.gotProv, cat.gotType)
	}
}

func TestRuntimeHostServer_CheckMediaPresence_RejectsTooMany(t *testing.T) {
	srv := pluginhost.NewRuntimeHostServerWithCatalog(&fakeHub{}, &fakeLibLister{}, &fakeCatalog{}, "p")
	ids := make([]string, 101)
	for i := range ids {
		ids[i] = "x"
	}
	_, err := srv.CheckMediaPresence(context.Background(), &pluginv1.CheckMediaPresenceRequest{
		Provider:  "tmdb",
		MediaType: "movie",
		Ids:       ids,
	})
	if err == nil {
		t.Error("expected error for >100 ids")
	}
}

func TestRuntimeHostServer_CheckMediaPresence_NoCatalogReturnsEmpty(t *testing.T) {
	srv := pluginhost.NewRuntimeHostServer(&fakeHub{}, &fakeLibLister{}, "p")
	resp, err := srv.CheckMediaPresence(context.Background(), &pluginv1.CheckMediaPresenceRequest{
		Provider: "tmdb", MediaType: "movie", Ids: []string{"1"},
	})
	if err != nil || len(resp.GetPresent()) != 0 {
		t.Errorf("expected empty no-catalog response, got %+v err=%v", resp, err)
	}
}
