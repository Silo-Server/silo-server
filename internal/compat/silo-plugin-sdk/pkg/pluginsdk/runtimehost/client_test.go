package runtimehost_test

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtimehost"
)

type fakeServer struct {
	pluginv1.UnimplementedRuntimeHostServer
	gotEvent             string
	gotPayload           map[string]any
	gotTargetPluginID    string
	gotTargetInstallID   int64
	gotUserID            string
	gotProvider          string
	gotMediaType         string
	gotIDs               []string
	gotConfigKey         string
	gotConfigValue       map[string]any
	listResp             *pluginv1.ListLibrariesResponse
	checkPresenceResp    *pluginv1.CheckMediaPresenceResponse
	listInstalledPlugins *pluginv1.ListInstalledPluginsResponse
	listMediaReq         *pluginv1.ListLibraryMediaRequest
	listMediaResp        *pluginv1.ListLibraryMediaResponse
	statsReq             *pluginv1.GetCatalogStatsRequest
	statsResp            *pluginv1.GetCatalogStatsResponse
	hostInfoResp         *pluginv1.GetHostInfoResponse
	callHTTPReq          *pluginv1.CallPluginHTTPRequest
	callHTTPResp         *pluginv1.CallPluginHTTPResponse
}

func (f *fakeServer) CheckMediaPresence(_ context.Context, req *pluginv1.CheckMediaPresenceRequest) (*pluginv1.CheckMediaPresenceResponse, error) {
	f.gotProvider = req.GetProvider()
	f.gotMediaType = req.GetMediaType()
	f.gotIDs = append([]string(nil), req.GetIds()...)
	if f.checkPresenceResp != nil {
		return f.checkPresenceResp, nil
	}
	return &pluginv1.CheckMediaPresenceResponse{}, nil
}

func (f *fakeServer) PublishEvent(_ context.Context, r *pluginv1.PublishEventRequest) (*pluginv1.PublishEventResponse, error) {
	f.gotEvent = r.GetEventName()
	if r.GetPayload() != nil {
		f.gotPayload = r.GetPayload().AsMap()
	}
	return &pluginv1.PublishEventResponse{}, nil
}

func (f *fakeServer) PublishEventTo(_ context.Context, r *pluginv1.PublishEventToRequest) (*pluginv1.PublishEventToResponse, error) {
	f.gotTargetPluginID = r.GetTargetPluginId()
	f.gotEvent = r.GetEventName()
	if r.GetPayload() != nil {
		f.gotPayload = r.GetPayload().AsMap()
	}
	return &pluginv1.PublishEventToResponse{}, nil
}

func (f *fakeServer) PublishEventToInstallation(_ context.Context, r *pluginv1.PublishEventToInstallationRequest) (*pluginv1.PublishEventToInstallationResponse, error) {
	f.gotTargetInstallID = r.GetTargetInstallationId()
	f.gotEvent = r.GetEventName()
	if r.GetPayload() != nil {
		f.gotPayload = r.GetPayload().AsMap()
	}
	return &pluginv1.PublishEventToInstallationResponse{}, nil
}

func (f *fakeServer) GetHostInfo(context.Context, *pluginv1.GetHostInfoRequest) (*pluginv1.GetHostInfoResponse, error) {
	if f.hostInfoResp != nil {
		return f.hostInfoResp, nil
	}
	return &pluginv1.GetHostInfoResponse{}, nil
}

func (f *fakeServer) ListLibraries(_ context.Context, req *pluginv1.ListLibrariesRequest) (*pluginv1.ListLibrariesResponse, error) {
	f.gotUserID = req.GetUserId()
	if f.listResp != nil {
		return f.listResp, nil
	}
	return &pluginv1.ListLibrariesResponse{}, nil
}

func (f *fakeServer) ListInstalledPlugins(context.Context, *pluginv1.ListInstalledPluginsRequest) (*pluginv1.ListInstalledPluginsResponse, error) {
	if f.listInstalledPlugins != nil {
		return f.listInstalledPlugins, nil
	}
	return &pluginv1.ListInstalledPluginsResponse{}, nil
}

func (f *fakeServer) SetGlobalConfigEntry(_ context.Context, req *pluginv1.SetGlobalConfigEntryRequest) (*pluginv1.SetGlobalConfigEntryResponse, error) {
	f.gotConfigKey = req.GetKey()
	if req.GetValue() != nil {
		f.gotConfigValue = req.GetValue().AsMap()
	}
	return &pluginv1.SetGlobalConfigEntryResponse{}, nil
}

func (f *fakeServer) ListLibraryMedia(_ context.Context, r *pluginv1.ListLibraryMediaRequest) (*pluginv1.ListLibraryMediaResponse, error) {
	f.listMediaReq = r
	if f.listMediaResp != nil {
		return f.listMediaResp, nil
	}
	return &pluginv1.ListLibraryMediaResponse{}, nil
}

func (f *fakeServer) GetCatalogStats(_ context.Context, r *pluginv1.GetCatalogStatsRequest) (*pluginv1.GetCatalogStatsResponse, error) {
	f.statsReq = r
	if f.statsResp != nil {
		return f.statsResp, nil
	}
	return &pluginv1.GetCatalogStatsResponse{}, nil
}

func (f *fakeServer) CallPluginHTTP(_ context.Context, r *pluginv1.CallPluginHTTPRequest) (*pluginv1.CallPluginHTTPResponse, error) {
	f.callHTTPReq = r
	if f.callHTTPResp != nil {
		return f.callHTTPResp, nil
	}
	return &pluginv1.CallPluginHTTPResponse{StatusCode: 204}, nil
}

func dial(t *testing.T, srv *fakeServer) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	g := grpc.NewServer()
	pluginv1.RegisterRuntimeHostServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestPublishEvent_PassesNameAndPayload(t *testing.T) {
	srv := &fakeServer{}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	if err := c.PublishEvent(context.Background(), "approved", map[string]any{
		"requestId": "01HXYZ",
		"tmdbId":    603,
	}); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	if srv.gotEvent != "approved" {
		t.Errorf("event_name = %q, want %q", srv.gotEvent, "approved")
	}
	if got := srv.gotPayload["requestId"]; got != "01HXYZ" {
		t.Errorf("requestId = %#v, want 01HXYZ", got)
	}
	if got := srv.gotPayload["tmdbId"]; got != float64(603) {
		t.Errorf("tmdbId = %#v, want 603", got)
	}
}

func TestPublishEventTo_PassesTargetNameAndPayload(t *testing.T) {
	srv := &fakeServer{}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	if err := c.PublishEventTo(context.Background(), "silo.requests", "approved", map[string]any{
		"requestId": "01HXYZ",
	}); err != nil {
		t.Fatalf("PublishEventTo: %v", err)
	}
	if srv.gotTargetPluginID != "silo.requests" {
		t.Errorf("target_plugin_id = %q, want silo.requests", srv.gotTargetPluginID)
	}
	if srv.gotEvent != "approved" {
		t.Errorf("event_name = %q, want approved", srv.gotEvent)
	}
	if got := srv.gotPayload["requestId"]; got != "01HXYZ" {
		t.Errorf("requestId = %#v, want 01HXYZ", got)
	}
}

func TestPublishEventToInstallation_PassesTargetNameAndPayload(t *testing.T) {
	srv := &fakeServer{}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	if err := c.PublishEventToInstallation(context.Background(), 42, "approved", map[string]any{
		"requestId": "01HXYZ",
	}); err != nil {
		t.Fatalf("PublishEventToInstallation: %v", err)
	}
	if srv.gotTargetInstallID != 42 {
		t.Errorf("target_installation_id = %d, want 42", srv.gotTargetInstallID)
	}
	if srv.gotEvent != "approved" {
		t.Errorf("event_name = %q, want approved", srv.gotEvent)
	}
	if got := srv.gotPayload["requestId"]; got != "01HXYZ" {
		t.Errorf("requestId = %#v, want 01HXYZ", got)
	}
}

func TestGetHostInfo_MapsResponse(t *testing.T) {
	srv := &fakeServer{
		hostInfoResp: &pluginv1.GetHostInfoResponse{
			PublicBaseUrl:      "https://silo.example",
			InternalBaseUrl:    "http://silo:3000",
			PluginProxyBaseUrl: "https://silo.example/api/v1/plugins",
		},
	}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	got, err := c.GetHostInfo(context.Background())
	if err != nil {
		t.Fatalf("GetHostInfo: %v", err)
	}
	if got.PublicBaseURL != "https://silo.example" || got.PluginProxyBaseURL == "" {
		t.Fatalf("bad host info: %+v", got)
	}
}

func TestListLibraries_PassesUserID(t *testing.T) {
	srv := &fakeServer{
		listResp: &pluginv1.ListLibrariesResponse{
			Libraries: []*pluginv1.Library{
				{Id: "lib-1", Name: "Movies", MediaType: "movie"},
			},
		},
	}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	libs, err := c.ListLibraries(context.Background(), "user-42")
	if err != nil {
		t.Fatalf("ListLibraries: %v", err)
	}
	if len(libs) != 1 || libs[0].Id != "lib-1" {
		t.Errorf("got %+v", libs)
	}
	if srv.gotUserID != "user-42" {
		t.Errorf("user_id = %q, want user-42", srv.gotUserID)
	}
}

func TestCheckMediaPresence_ReturnsMap(t *testing.T) {
	srv := &fakeServer{}
	srv.checkPresenceResp = &pluginv1.CheckMediaPresenceResponse{
		Present: []*pluginv1.MediaPresence{
			{ExternalId: "603", MediaId: "m-1", LibraryId: "lib-1", Title: "The Matrix"},
			{ExternalId: "550", MediaId: "m-2", LibraryId: "lib-1", Title: "Fight Club"},
		},
	}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	got, err := c.CheckMediaPresence(context.Background(), "tmdb", "movie", []string{"603", "550", "999"})
	if err != nil {
		t.Fatalf("CheckMediaPresence: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got["603"].MediaID != "m-1" || got["603"].LibraryID != "lib-1" {
		t.Errorf("603: %+v", got["603"])
	}
	if _, ok := got["999"]; ok {
		t.Errorf("999 should not be present in map")
	}
	if srv.gotProvider != "tmdb" {
		t.Errorf("provider = %q, want tmdb", srv.gotProvider)
	}
	if srv.gotMediaType != "movie" {
		t.Errorf("media_type = %q, want movie", srv.gotMediaType)
	}
	if want := []string{"603", "550", "999"}; !reflect.DeepEqual(srv.gotIDs, want) {
		t.Errorf("ids = %v, want %v", srv.gotIDs, want)
	}
}

func TestListInstalledPlugins_ReturnsPlugins(t *testing.T) {
	srv := &fakeServer{
		listInstalledPlugins: &pluginv1.ListInstalledPluginsResponse{
			Plugins: []*pluginv1.InstalledPlugin{
				{
					InstallationId: 42,
					PluginId:       "silo.requests",
					Version:        "0.1.0",
					Enabled:        true,
					Capabilities: []*pluginv1.CapabilityDescriptor{
						{Type: "request_router.v1", Id: "default"},
					},
				},
			},
		},
	}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	plugins, err := c.ListInstalledPlugins(context.Background())
	if err != nil {
		t.Fatalf("ListInstalledPlugins: %v", err)
	}
	if len(plugins) != 1 || plugins[0].GetPluginId() != "silo.requests" {
		t.Fatalf("plugins = %+v, want silo.requests", plugins)
	}
	if got := plugins[0].GetCapabilities()[0].GetType(); got != "request_router.v1" {
		t.Errorf("capability type = %q, want request_router.v1", got)
	}
}

func TestListInstalledPluginsByCapability_FiltersAndReadsMetadata(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{
		"mediaTypes": []any{"movie", "tv"},
		"basePath":   "/requests",
	})
	if err != nil {
		t.Fatalf("NewStruct: %v", err)
	}
	srv := &fakeServer{
		listInstalledPlugins: &pluginv1.ListInstalledPluginsResponse{
			Plugins: []*pluginv1.InstalledPlugin{
				{
					InstallationId: 42,
					PluginId:       "silo.requests",
					Enabled:        true,
					Capabilities: []*pluginv1.CapabilityDescriptor{
						{Type: capability.RequestRouter, Id: "default", Metadata: metadata},
					},
				},
				{
					InstallationId: 99,
					PluginId:       "silo.other",
					Enabled:        true,
					Capabilities: []*pluginv1.CapabilityDescriptor{
						{Type: capability.HTTPRoutes, Id: "default"},
					},
				},
			},
		},
	}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	plugins, err := c.ListInstalledPluginsByCapability(context.Background(), capability.RequestRouter)
	if err != nil {
		t.Fatalf("ListInstalledPluginsByCapability: %v", err)
	}
	if len(plugins) != 1 || plugins[0].GetInstallationId() != 42 {
		t.Fatalf("plugins = %+v, want installation 42", plugins)
	}
	cap := runtimehost.Capability(plugins[0], capability.RequestRouter)
	if cap == nil || !runtimehost.HasCapability(plugins[0], capability.RequestRouter) {
		t.Fatalf("capability helpers did not find request router")
	}
	if got := runtimehost.CapabilityMetadataString(cap, "basePath"); got != "/requests" {
		t.Fatalf("basePath = %q, want /requests", got)
	}
	if got := runtimehost.CapabilityMetadataStrings(cap, "mediaTypes"); !reflect.DeepEqual(got, []string{"movie", "tv"}) {
		t.Fatalf("mediaTypes = %v, want [movie tv]", got)
	}
}

func TestSetGlobalConfigEntry_PassesKeyAndValue(t *testing.T) {
	srv := &fakeServer{}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	if err := c.SetGlobalConfigEntry(context.Background(), "connection", map[string]any{
		"baseUrl": "https://example.test",
	}); err != nil {
		t.Fatalf("SetGlobalConfigEntry: %v", err)
	}
	if srv.gotConfigKey != "connection" {
		t.Errorf("key = %q, want connection", srv.gotConfigKey)
	}
	if got := srv.gotConfigValue["baseUrl"]; got != "https://example.test" {
		t.Errorf("baseUrl = %#v, want https://example.test", got)
	}
}

func TestClientValidation(t *testing.T) {
	srv := &fakeServer{}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	if err := c.PublishEvent(context.Background(), "", nil); err == nil {
		t.Fatal("PublishEvent empty name returned nil error")
	}
	if err := c.PublishEventTo(context.Background(), "", "approved", nil); err == nil {
		t.Fatal("PublishEventTo empty target returned nil error")
	}
	if err := c.PublishEventTo(context.Background(), "silo.requests", "", nil); err == nil {
		t.Fatal("PublishEventTo empty name returned nil error")
	}
	if err := c.PublishEventToInstallation(context.Background(), 0, "approved", nil); err == nil {
		t.Fatal("PublishEventToInstallation empty target returned nil error")
	}
	if err := c.PublishEventToInstallation(context.Background(), 42, "", nil); err == nil {
		t.Fatal("PublishEventToInstallation empty name returned nil error")
	}
	if err := c.SetGlobalConfigEntry(context.Background(), "", nil); err == nil {
		t.Fatal("SetGlobalConfigEntry empty key returned nil error")
	}
}

func TestCallPluginJSON_MarshalsRequestAndDecodesResponse(t *testing.T) {
	srv := &fakeServer{
		callHTTPResp: &pluginv1.CallPluginHTTPResponse{
			StatusCode: 200,
			Body:       []byte(`{"accepted":true,"id":"r-1"}`),
		},
	}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	var got struct {
		Accepted bool   `json:"accepted"`
		ID       string `json:"id"`
	}
	err := c.CallPluginJSON(context.Background(), runtimehost.CallPluginJSONRequest{
		InstallationID: 42,
		Path:           "/api/request",
		Headers:        map[string]string{"X-Test": "yes"},
		Query:          map[string]any{"library": "movies"},
		Request:        map[string]any{"title": "The Matrix"},
		Response:       &got,
	})
	if err != nil {
		t.Fatalf("CallPluginJSON: %v", err)
	}
	if srv.callHTTPReq.GetMethod() != "POST" || srv.callHTTPReq.GetPath() != "/api/request" {
		t.Fatalf("bad http request: %+v", srv.callHTTPReq)
	}
	if srv.callHTTPReq.GetHeaders()["Content-Type"] != "application/json" || srv.callHTTPReq.GetHeaders()["Accept"] != "application/json" {
		t.Fatalf("headers not set: %+v", srv.callHTTPReq.GetHeaders())
	}
	if got.Accepted != true || got.ID != "r-1" {
		t.Fatalf("bad response decode: %+v", got)
	}
	if srv.callHTTPReq.GetQuery().AsMap()["library"] != "movies" {
		t.Fatalf("query not passed: %+v", srv.callHTTPReq.GetQuery())
	}
}

func TestCallPluginJSON_ReturnsStatusError(t *testing.T) {
	srv := &fakeServer{
		callHTTPResp: &pluginv1.CallPluginHTTPResponse{
			StatusCode: 404,
			Body:       []byte(`{"error":"missing"}`),
		},
	}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	err := c.CallPluginJSON(context.Background(), runtimehost.CallPluginJSONRequest{
		InstallationID: 42,
		Path:           "/api/request",
	})
	var statusErr *runtimehost.HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %v, want HTTPStatusError", err)
	}
	if statusErr.StatusCode != 404 || string(statusErr.Body) != `{"error":"missing"}` {
		t.Fatalf("bad status error: %+v", statusErr)
	}
}

func TestListLibraryMedia_MapsRequestAndResponse(t *testing.T) {
	srv := &fakeServer{
		listMediaResp: &pluginv1.ListLibraryMediaResponse{
			Items: []*pluginv1.CatalogMediaItem{{
				MediaId: "m-1", LibraryId: "lib-1", MediaType: "movie", Title: "The Matrix",
				Year: 1999, PosterUrl: "poster", Genres: []string{"Action"}, RuntimeMinutes: 136,
				Rating: 8.7, ExternalProvider: "tmdb", ExternalId: "603",
			}},
			NextPageToken: "next",
			TotalCount:    42,
		},
	}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	got, err := c.ListLibraryMedia(context.Background(), runtimehost.ListLibraryMediaRequest{
		LibraryIDs: []string{"lib-1"},
		MediaTypes: []string{"movie"},
		Query:      "matrix",
		Sort:       "title",
		PageSize:   25,
	})
	if err != nil {
		t.Fatalf("ListLibraryMedia: %v", err)
	}
	if srv.listMediaReq.GetQuery() != "matrix" || srv.listMediaReq.GetPageSize() != 25 {
		t.Fatalf("request not mapped: %+v", srv.listMediaReq)
	}
	if got.TotalCount != 42 || got.NextPageToken != "next" || len(got.Items) != 1 {
		t.Fatalf("bad response: %+v", got)
	}
	if got.Items[0].Title != "The Matrix" || got.Items[0].Genres[0] != "Action" {
		t.Fatalf("bad item: %+v", got.Items[0])
	}
}

func TestGetCatalogStats_MapsResponse(t *testing.T) {
	srv := &fakeServer{
		statsResp: &pluginv1.GetCatalogStatsResponse{
			TotalItems: 10,
			MediaTypeCounts: []*pluginv1.CatalogTypeCount{
				{MediaType: "movie", Count: 7},
			},
			LibraryCounts: []*pluginv1.CatalogLibraryCount{
				{LibraryId: "lib-1", LibraryName: "Movies", MediaType: "movie", Count: 7},
			},
		},
	}
	conn := dial(t, srv)
	c := runtimehost.NewClient(conn)

	got, err := c.GetCatalogStats(context.Background(), []string{"lib-1"})
	if err != nil {
		t.Fatalf("GetCatalogStats: %v", err)
	}
	if srv.statsReq.GetLibraryIds()[0] != "lib-1" {
		t.Fatalf("library ids not passed: %+v", srv.statsReq)
	}
	if got.TotalItems != 10 || got.MediaTypeCounts[0].Count != 7 || got.LibraryCounts[0].LibraryName != "Movies" {
		t.Fatalf("bad stats: %+v", got)
	}
}
