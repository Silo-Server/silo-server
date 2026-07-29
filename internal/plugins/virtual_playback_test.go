package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"google.golang.org/grpc"
)

type fakeHTTPRoutesGRPCClient struct {
	pluginv1.HttpRoutesClient
	handleFunc func(ctx context.Context, in *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error)
}

func (f *fakeHTTPRoutesGRPCClient) Handle(ctx context.Context, in *pluginv1.HandleHTTPRequest, opts ...grpc.CallOption) (*pluginv1.HandleHTTPResponse, error) {
	if f.handleFunc != nil {
		return f.handleFunc(ctx, in)
	}
	return nil, errors.New("not implemented")
}

type fakeVirtualPluginHost struct {
	clients map[int]pluginClient
}

func (h *fakeVirtualPluginHost) Client(id int) (pluginClient, error) {
	if c, ok := h.clients[id]; ok {
		return c, nil
	}
	return nil, pluginhost.ErrClientNotFound
}

func (h *fakeVirtualPluginHost) Start(context.Context, pluginhost.StartRequest) (pluginClient, error) {
	return nil, errors.New("not implemented")
}

func (h *fakeVirtualPluginHost) Stop(int) error { return nil }

func (h *fakeVirtualPluginHost) Shutdown(context.Context) error { return nil }

func TestListVirtualPlaybackStreamsFailsSoftPerPlugin(t *testing.T) {
	manifest := testPluginManifest(t, "test.virtual", "1.0.0")
	manifest.Capabilities = []*pluginv1.CapabilityDescriptor{
		{Type: "http_routes.v1", Id: virtualPlaybackCapabilityID},
	}
	installPath := writeInstalledPluginManifest(t, manifest)

	inst1 := &Installation{ID: 101, PluginID: "test.virtual", Version: "1.0.0", InstallPath: installPath, Enabled: true}
	inst2 := &Installation{ID: 102, PluginID: "test.virtual", Version: "1.0.0", InstallPath: installPath, Enabled: true}

	store := newFakeServiceInstallationStore(inst1, inst2)
	store.listCapabilities = []*Capability{
		{Type: "http_routes.v1", ID: virtualPlaybackCapabilityID},
	}

	client1 := &fakePluginClient{
		manifest: manifest,
		httpRoutesClient: pluginhost.NewHTTPRoutesClientForTest(&fakeHTTPRoutesGRPCClient{
			handleFunc: func(ctx context.Context, in *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
				// Plugin 1 succeeds but has no candidates; continue to the next provider.
				return &pluginv1.HandleHTTPResponse{StatusCode: 200, Body: []byte(`{"streams":[]}`)}, nil
			},
		}, time.Second),
	}

	payload, _ := json.Marshal(map[string]any{
		"streams": []map[string]any{
			{
				"id":          "s1",
				"label":       "1080p HEVC",
				"uri":         "virtual://movie/tt1234/1080p",
				"resolution":  "1080p",
				"codec_video": "hevc",
				"container":   "mkv",
				"file_size":   1048576,
			},
		},
	})

	client2 := &fakePluginClient{
		manifest: manifest,
		httpRoutesClient: pluginhost.NewHTTPRoutesClientForTest(&fakeHTTPRoutesGRPCClient{
			handleFunc: func(ctx context.Context, in *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
				// Plugin 2 succeeds with candidates
				return &pluginv1.HandleHTTPResponse{StatusCode: 200, Body: payload}, nil
			},
		}, time.Second),
	}

	host := &fakeVirtualPluginHost{
		clients: map[int]pluginClient{
			101: client1,
			102: client2,
		},
	}

	service := &Service{
		installations: store,
		host:          host,
	}

	streams, err := service.ListVirtualPlaybackStreams(context.Background(), "movie/tt1234")
	if err != nil {
		t.Fatalf("ListVirtualPlaybackStreams failed: %v", err)
	}
	if len(streams) != 1 {
		t.Fatalf("expected 1 stream from working plugin, got %d", len(streams))
	}
	if streams[0].URI != "virtual://movie/tt1234/1080p" {
		t.Fatalf("unexpected stream URI: %q", streams[0].URI)
	}
	if streams[0].Container != "mkv" {
		t.Fatalf("unexpected stream container: %q, want mkv", streams[0].Container)
	}
	if streams[0].FileSize != 1048576 {
		t.Fatalf("unexpected stream file size: %d, want 1048576", streams[0].FileSize)
	}
}
