package autoscan

import (
	"context"
	"errors"
	"fmt"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// capturingClient records the last PollChangesRequest it was handed.
type capturingClient struct {
	last *pluginv1.PollChangesRequest
}

func (c *capturingClient) PollChanges(_ context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error) {
	c.last = req
	return &pluginv1.PollChangesResponse{SourcePaths: []string{"/mnt/media/x"}, NextMarker: "m2"}, nil
}

// capturingResolver yields a fixed PollChangesClient and records the requested
// stable scan-source identity.
type capturingResolver struct {
	client       PollChangesClient
	lastPluginID string
	lastCapID    string
}

func (r *capturingResolver) ScanSourceClient(_ context.Context, pluginID, capabilityID string) (PollChangesClient, error) {
	r.lastPluginID = pluginID
	r.lastCapID = capabilityID
	return r.client, nil
}

// TestPluginProviderPopulatesConnection asserts the resolved connection is
// delivered to the plugin on the PollChangesRequest.
func TestPluginProviderPopulatesConnection(t *testing.T) {
	client := &capturingClient{}
	resolver := &capturingResolver{client: client}
	prov := NewPluginProvider(resolver)

	conn := ResolvedConnection{BaseURL: "https://arr.example", APIKey: "secret-key"}
	sourceConfig := map[string]string{"exclusions": ".downloads"}
	changes, next, err := prov.PollChanges(context.Background(), "silo.autoscan.arr", "cap", "m1", conn, sourceConfig)
	if err != nil {
		t.Fatalf("PollChanges: %v", err)
	}
	if len(changes) != 1 || changes[0].SourcePath != "/mnt/media/x" || changes[0].Scope != ChangeScopeAuto || next != "m2" {
		t.Fatalf("unexpected provider result: changes=%v next=%q", changes, next)
	}
	if client.last == nil {
		t.Fatal("expected a PollChangesRequest to be sent")
	}
	if client.last.GetCapabilityId() != "cap" || client.last.GetMarker() != "m1" {
		t.Fatalf("unexpected request fields: cap=%q marker=%q", client.last.GetCapabilityId(), client.last.GetMarker())
	}
	if resolver.lastPluginID != "silo.autoscan.arr" || resolver.lastCapID != "cap" {
		t.Fatalf("resolver identity = %q/%q", resolver.lastPluginID, resolver.lastCapID)
	}
	rc := client.last.GetConnection()
	if rc == nil {
		t.Fatal("expected connection to be populated on the request")
	}
	if rc.GetBaseUrl() != conn.BaseURL || rc.GetApiKey() != conn.APIKey {
		t.Fatalf("connection not delivered: base_url=%q api_key=%q", rc.GetBaseUrl(), rc.GetApiKey())
	}
	if client.last.GetSourceConfig()["exclusions"] != ".downloads" {
		t.Fatalf("source_config not delivered: %#v", client.last.GetSourceConfig())
	}
}

func TestPluginProviderPrefersStructuredChanges(t *testing.T) {
	client := &capturingStructuredClient{}
	prov := NewPluginProvider(&capturingResolver{client: client})

	changes, next, err := prov.PollChanges(context.Background(), "silo.autoscan.arr", "cap", "m1", ResolvedConnection{}, nil)
	if err != nil {
		t.Fatalf("PollChanges: %v", err)
	}
	if next != "m3" || len(changes) != 2 {
		t.Fatalf("unexpected provider result: changes=%v next=%q", changes, next)
	}
	if changes[0] != (Change{SourcePath: "/ceph/movie/Movie", Scope: ChangeScopeSubtree}) {
		t.Fatalf("first change = %#v", changes[0])
	}
	if changes[1] != (Change{SourcePath: "/ceph/show/S01/E01.mkv", Scope: ChangeScopeFile}) {
		t.Fatalf("second change = %#v", changes[1])
	}
}

type capturingStructuredClient struct {
	last *pluginv1.PollChangesRequest
}

func (c *capturingStructuredClient) PollChanges(_ context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error) {
	c.last = req
	return &pluginv1.PollChangesResponse{
		SourcePaths: []string{"/legacy/ignored.mkv"},
		NextMarker:  "m3",
		Changes: []*pluginv1.ScanSourceChange{
			{
				SourcePath: "/ceph/movie/Movie",
				Scope:      pluginv1.ScanSourceChangeScope_SCAN_SOURCE_CHANGE_SCOPE_SUBTREE,
			},
			{
				SourcePath: "/ceph/show/S01/E01.mkv",
				Scope:      pluginv1.ScanSourceChangeScope_SCAN_SOURCE_CHANGE_SCOPE_FILE,
			},
		},
	}, nil
}

// Errors stored on a source or event are shown to operators verbatim, so the
// gRPC transport framing must not reach them.
func TestPollErrorMessage(t *testing.T) {
	pluginErr := status.Error(codes.Unknown, "scan_source: no connection supplied")
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"plugin error keeps plugin text": {
			err:  pluginErr,
			want: "scan_source: no connection supplied",
		},
		"wrapped plugin error keeps plugin text": {
			err:  fmt.Errorf("poll: %w", pluginErr),
			want: "scan_source: no connection supplied",
		},
		"plugin text relaying an upstream gRPC error is kept": {
			err:  status.Error(codes.Unknown, "upstream: rpc error: code = NotFound desc = series 12"),
			want: "upstream: rpc error: code = NotFound desc = series 12",
		},
		"plugin-chosen code keeps plugin text": {
			err:  status.Error(codes.InvalidArgument, "invalid marker"),
			want: "invalid marker",
		},
		"unavailable plugin hides transport detail": {
			err:  status.Error(codes.Unavailable, `connection error: desc = "transport: Error while dialing: dial unix /tmp/plugin123: connect: connection refused"`),
			want: "Plugin unavailable.",
		},
		"unavailable plugin without detail": {
			err:  status.Error(codes.Unavailable, ""),
			want: "Plugin unavailable.",
		},
		"host deadline hides transport detail": {
			err: fmt.Errorf("%w: %w", context.DeadlineExceeded,
				status.Error(codes.DeadlineExceeded, `latest balancer error: connection error: desc = "transport: Error while dialing: dial unix /tmp/plugin123: connect: connection refused"`)),
			want: "Plugin timed out.",
		},
		"deadline exceeded before the call": {
			err:  fmt.Errorf("resolve plugin: %w", context.DeadlineExceeded),
			want: "Plugin timed out.",
		},
		"host cancel": {
			err:  fmt.Errorf("%w: %w", context.Canceled, status.Error(codes.Canceled, "context canceled")),
			want: "Poll canceled.",
		},
		"plugin-chosen deadline keeps plugin text": {
			err:  status.Error(codes.DeadlineExceeded, "history request to Sonarr timed out after 30s"),
			want: "history request to Sonarr timed out after 30s",
		},
		"plugin-chosen unimplemented keeps plugin text": {
			err:  status.Error(codes.Unimplemented, "structured scope requires Sonarr v4"),
			want: "structured scope requires Sonarr v4",
		},
		"generated stub unimplemented keeps its text": {
			err:  status.Error(codes.Unimplemented, "method PollChanges not implemented"),
			want: "method PollChanges not implemented",
		},
		"unimplemented without detail": {
			err:  status.Error(codes.Unimplemented, ""),
			want: "Plugin does not support polling for changes.",
		},
		"deadline exceeded without detail": {
			err:  status.Error(codes.DeadlineExceeded, ""),
			want: "Plugin timed out.",
		},
		"empty plugin text": {
			err:  status.Error(codes.Internal, ""),
			want: "Plugin error: Internal",
		},
		"plugin text masks credential assignments": {
			err:  status.Error(codes.Unknown, "api_key=FAKE_FIXTURE_SECRET rejected"),
			want: "api_key=" + logredact.Placeholder + " rejected",
		},
		"host error masks URL userinfo": {
			err:  errors.New("dial http://user:FAKE_FIXTURE_SECRET@sonarr.invalid failed"),
			want: "dial http://" + logredact.Placeholder + "@sonarr.invalid failed",
		},
		"host error is unchanged": {
			err:  errors.New("plugin silo.autoscan.arr is not running"),
			want: "plugin silo.autoscan.arr is not running",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := pollErrorMessage(tc.err); got != tc.want {
				t.Fatalf("pollErrorMessage = %q, want %q", got, tc.want)
			}
		})
	}
}
