package plugins

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const testVirtualCapabilityID = "test-stream-provider"

type fakeVirtualStreamGRPCClient struct {
	pluginv1.VirtualStreamProviderClient
	resolveFunc  func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error)
	profilesFunc func(context.Context, *pluginv1.ListVirtualStreamProfilesRequest) (*pluginv1.ListVirtualStreamProfilesResponse, error)
}

func (f *fakeVirtualStreamGRPCClient) ResolveVirtualStream(
	ctx context.Context,
	request *pluginv1.ResolveVirtualStreamRequest,
	_ ...grpc.CallOption,
) (*pluginv1.ResolveVirtualStreamResponse, error) {
	if f.resolveFunc == nil {
		return nil, errors.New("not implemented")
	}
	return f.resolveFunc(ctx, request)
}

func (f *fakeVirtualStreamGRPCClient) ListVirtualStreamProfiles(
	ctx context.Context,
	request *pluginv1.ListVirtualStreamProfilesRequest,
	_ ...grpc.CallOption,
) (*pluginv1.ListVirtualStreamProfilesResponse, error) {
	if f.profilesFunc == nil {
		return nil, errors.New("not implemented")
	}
	return f.profilesFunc(ctx, request)
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

func TestListVirtualPlaybackStreamsRoutesOwnerThenExplicitFallback(t *testing.T) {
	service, calls := newVirtualPlaybackTestService(t,
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(), nil
		},
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(virtualCandidate("fallback", "https://1.1.1.1/fallback")), nil
		},
	)
	streams, err := service.ListVirtualPlaybackStreamsWithRouting(
		context.Background(),
		"virtual://movie/tt1234",
		7,
		"profile-1",
		VirtualPlaybackRouting{OwnerInstallationID: 101, AllowFallback: true},
	)
	if err != nil {
		t.Fatalf("ListVirtualPlaybackStreamsWithRouting failed: %v", err)
	}
	if len(streams) != 1 || streams[0].ID != "fallback" || streams[0].OwnerInstallationID != 102 {
		t.Fatalf("unexpected fallback streams: %+v", streams)
	}
	if calls[101].Load() != 1 || calls[102].Load() != 1 {
		t.Fatalf("provider calls = owner:%d fallback:%d, want 1 each", calls[101].Load(), calls[102].Load())
	}
}

func TestVirtualPlaybackProfileMissTriesFallbackProvider(t *testing.T) {
	owner := virtualCandidate("owner-hd", "https://1.1.1.1/hd")
	owner.Resolution.Label = "1080p"
	fallback := virtualCandidate("fallback-4k", "https://8.8.8.8/4k")
	fallback.Resolution.Label = "2160p"
	service, calls := newVirtualPlaybackTestService(t,
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(owner), nil
		},
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(fallback), nil
		},
	)
	resolved, err := service.ResolveVirtualPlaybackWithRouting(
		context.Background(), "virtual://movie/tt1234?profile=2160p", 7, "p1",
		VirtualPlaybackRouting{OwnerInstallationID: 101, AllowFallback: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "https://8.8.8.8/4k" || calls[101].Load() != 1 || calls[102].Load() != 1 {
		t.Fatalf("resolved=%q calls owner=%d fallback=%d", resolved, calls[101].Load(), calls[102].Load())
	}
}

func TestVirtualPlaybackDoesNotFallbackWithoutOptIn(t *testing.T) {
	service, calls := newVirtualPlaybackTestService(t,
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(), nil
		},
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(virtualCandidate("other", "https://1.1.1.1/other")), nil
		},
	)
	_, err := service.ListVirtualPlaybackStreamsWithRouting(
		context.Background(),
		"virtual://movie/tt1234",
		0,
		"",
		VirtualPlaybackRouting{OwnerInstallationID: 101},
	)
	if err == nil {
		t.Fatal("owner-only resolution succeeded with no owner candidates")
	}
	if calls[101].Load() != 1 || calls[102].Load() != 0 {
		t.Fatalf("provider calls = owner:%d other:%d, want 1 and 0", calls[101].Load(), calls[102].Load())
	}
}

func TestVirtualPlaybackCachesTypedResponseAcrossListAndResolve(t *testing.T) {
	service, calls := newVirtualPlaybackTestService(t,
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(virtualCandidate("chosen", "https://1.1.1.1/chosen?token=secret")), nil
		},
	)
	routing := VirtualPlaybackRouting{OwnerInstallationID: 101}
	streams, err := service.ListVirtualPlaybackStreamsWithRouting(context.Background(), "virtual://movie/tt1234", 7, "p1", routing)
	if err != nil || len(streams) != 1 {
		t.Fatalf("list streams = %+v, %v", streams, err)
	}
	resolved, err := service.ResolveVirtualPlaybackWithRouting(context.Background(), streams[0].URI, 7, "p1", routing)
	if err != nil {
		t.Fatalf("resolve selected stream: %v", err)
	}
	if resolved != "https://1.1.1.1/chosen?token=secret" {
		t.Fatalf("resolved URL = %q", resolved)
	}
	if calls[101].Load() != 1 {
		t.Fatalf("provider called %d times, want cached single call", calls[101].Load())
	}
}

func TestVirtualPlaybackForceRefreshBypassesAndReplacesHostCache(t *testing.T) {
	var sequence atomic.Int32
	var forced atomic.Bool
	service, calls := newVirtualPlaybackTestService(t,
		func(_ context.Context, request *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			number := sequence.Add(1)
			if virtualStreamForceRefresh(request) {
				forced.Store(true)
			}
			return virtualResponse(virtualCandidate(
				fmt.Sprintf("candidate-%d", number),
				fmt.Sprintf("https://1.1.1.1/stream-%d", number),
			)), nil
		},
	)
	routing := VirtualPlaybackRouting{OwnerInstallationID: 101}
	first, err := service.ResolveVirtualPlaybackWithRouting(context.Background(), "virtual://movie/tt1234", 7, "p1", routing)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := service.ResolveVirtualPlaybackWithRouting(context.Background(), "virtual://movie/tt1234", 7, "p1", routing)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := service.RefreshVirtualPlaybackForInstallation(
		context.Background(), "virtual://movie/tt1234", 7, "p1", 101, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	afterRefresh, err := service.ResolveVirtualPlaybackWithRouting(context.Background(), "virtual://movie/tt1234", 7, "p1", routing)
	if err != nil {
		t.Fatal(err)
	}
	if first != "https://1.1.1.1/stream-1" || cached != first ||
		refreshed != "https://1.1.1.1/stream-2" || afterRefresh != refreshed {
		t.Fatalf("cache sequence = first %q cached %q refreshed %q after %q", first, cached, refreshed, afterRefresh)
	}
	if calls[101].Load() != 2 || !forced.Load() {
		t.Fatalf("provider calls=%d force_refresh=%v", calls[101].Load(), forced.Load())
	}
}

func TestVirtualPlaybackCacheTTLUsesBoundedProviderMetadata(t *testing.T) {
	tests := []struct {
		name    string
		seconds any
		want    time.Duration
	}{
		{name: "default", seconds: nil, want: virtualStreamsCacheTTL},
		{name: "minimum clamp", seconds: float64(1), want: minVirtualStreamsCacheTTL},
		{name: "provider value", seconds: float64(3600), want: time.Hour},
		{name: "maximum clamp", seconds: float64(9999999), want: maxVirtualStreamsCacheTTL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := virtualResponse(virtualCandidate("ttl", "https://1.1.1.1/ttl")).GetResult()
			if test.seconds != nil {
				result.Metadata, _ = structpb.NewStruct(map[string]any{"cache_ttl_seconds": test.seconds})
			}
			if got := virtualStreamCacheTTL(result); got != test.want {
				t.Fatalf("virtualStreamCacheTTL = %v, want %v", got, test.want)
			}
			service := &Service{}
			now := time.Now()
			service.storeVirtualStreamResult("ttl", result, now)
			if got := service.virtualStreamsCache["ttl"].expiresAt.Sub(now); got != test.want {
				t.Fatalf("stored TTL = %v, want %v", got, test.want)
			}
		})
	}
}

func TestVirtualPlaybackRejectsInvalidProviderCacheTTL(t *testing.T) {
	for _, ttl := range []any{"3600", 60.5} {
		result := virtualResponse(virtualCandidate("ttl", "https://1.1.1.1/ttl"))
		result.Result.Metadata, _ = structpb.NewStruct(map[string]any{"cache_ttl_seconds": ttl})
		if _, err := validateVirtualStreamResponse(result); err == nil {
			t.Fatalf("invalid cache_ttl_seconds %#v was accepted", ttl)
		}
	}
}

func TestConfiguredVirtualProfilesCachesConfigurationOnlyRPC(t *testing.T) {
	var calls atomic.Int32
	client := pluginhost.NewVirtualStreamProviderClientForTest(&fakeVirtualStreamGRPCClient{
		profilesFunc: func(_ context.Context, request *pluginv1.ListVirtualStreamProfilesRequest) (*pluginv1.ListVirtualStreamProfilesResponse, error) {
			calls.Add(1)
			if request.GetCapabilityId() != testVirtualCapabilityID || request.GetMediaType() != "movie" {
				t.Fatalf("request = %#v", request)
			}
			return &pluginv1.ListVirtualStreamProfilesResponse{Profiles: []*pluginv1.VirtualStreamProfile{{Label: "1080p"}}}, nil
		},
	}, time.Second)
	service := &Service{}

	for range 2 {
		response, err := service.configuredVirtualProfiles(context.Background(), 101, testVirtualCapabilityID, "movie", client)
		if err != nil {
			t.Fatalf("configuredVirtualProfiles: %v", err)
		}
		if len(response.GetProfiles()) != 1 || response.GetProfiles()[0].GetLabel() != "1080p" {
			t.Fatalf("response = %#v", response)
		}
		response.Profiles[0].Label = "mutated"
	}
	if calls.Load() != 1 {
		t.Fatalf("profile RPC calls = %d, want 1", calls.Load())
	}
	service.invalidateInstallationCache()
	if _, err := service.configuredVirtualProfiles(context.Background(), 101, testVirtualCapabilityID, "movie", client); err != nil {
		t.Fatalf("configuredVirtualProfiles after invalidation: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("profile RPC calls after invalidation = %d, want 2", calls.Load())
	}
}

func TestVirtualPlaybackRejectsOversizedCandidateSet(t *testing.T) {
	candidates := make([]*pluginv1.VirtualStreamCandidate, maxVirtualPlaybackStreams+1)
	for i := range candidates {
		candidates[i] = virtualCandidate(fmt.Sprintf("candidate-%d", i), fmt.Sprintf("https://1.1.1.1/%d", i))
	}
	service, _ := newVirtualPlaybackTestService(t,
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(candidates...), nil
		},
	)
	_, err := service.ListVirtualPlaybackStreamsWithRouting(
		context.Background(), "virtual://movie/tt1234", 0, "", VirtualPlaybackRouting{OwnerInstallationID: 101},
	)
	if err == nil {
		t.Fatal("oversized candidate set was accepted")
	}
}

func TestVirtualPlaybackRejectsUnsafeCandidateURL(t *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd",
		"https://user:secret@1.1.1.1/stream",
		"https://1.1.1.1/stream\ninjected",
		"http://127.0.0.1/admin",
	} {
		t.Run(raw, func(t *testing.T) {
			service, _ := newVirtualPlaybackTestService(t,
				func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
					return virtualResponse(virtualCandidate("unsafe", raw)), nil
				},
			)
			_, err := service.ListVirtualPlaybackStreamsWithRouting(
				context.Background(), "virtual://movie/tt1234", 0, "", VirtualPlaybackRouting{OwnerInstallationID: 101},
			)
			if err == nil {
				t.Fatal("unsafe candidate URL was accepted")
			}
		})
	}
}

func TestVirtualPlaybackDefersDNSUntilCandidateResolution(t *testing.T) {
	service, _ := newVirtualPlaybackTestService(t,
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(virtualCandidate("unresolved", "https://provider.invalid/stream")), nil
		},
	)
	streams, err := service.ListVirtualPlaybackStreamsWithRouting(
		context.Background(), "virtual://movie/tt1234", 0, "", VirtualPlaybackRouting{OwnerInstallationID: 101},
	)
	if err != nil || len(streams) != 1 {
		t.Fatalf("candidate list = %+v, %v; DNS should be deferred", streams, err)
	}
	if _, err := service.ResolveVirtualPlaybackWithRouting(
		context.Background(), streams[0].URI, 0, "", VirtualPlaybackRouting{OwnerInstallationID: 101},
	); err == nil {
		t.Fatal("selected candidate with an unresolvable host was accepted")
	}
}

func TestVirtualPlaybackUsesBoundedCandidateDisplayMetadata(t *testing.T) {
	candidate := virtualCandidate("display", "https://1.1.1.1/display")
	candidate.Metadata, _ = structpb.NewStruct(map[string]any{
		"display_name": "Release Name · 4K",
		"source_type":  "Alt source",
	})
	service, _ := newVirtualPlaybackTestService(t,
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(candidate), nil
		},
	)
	streams, err := service.ListVirtualPlaybackStreamsWithRouting(
		context.Background(), "virtual://movie/tt1234", 0, "", VirtualPlaybackRouting{OwnerInstallationID: 101},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 1 || streams[0].Label != "Release Name · 4K" || streams[0].SourceType != "Alt source" {
		t.Fatalf("display metadata not applied: %+v", streams)
	}
}

func TestVirtualPlaybackRejectsInvalidCandidateDisplayMetadata(t *testing.T) {
	for _, metadata := range []map[string]any{
		{"display_name": 42},
		{"source_type": "bad\nsource"},
		{"display_name": "spoof\u202ename"},
		{"display_name": strings.Repeat("x", maxVirtualLabelLen+1)},
	} {
		candidate := virtualCandidate("display", "https://1.1.1.1/display")
		candidate.Metadata, _ = structpb.NewStruct(metadata)
		service, _ := newVirtualPlaybackTestService(t,
			func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
				return virtualResponse(candidate), nil
			},
		)
		if _, err := service.ListVirtualPlaybackStreamsWithRouting(
			context.Background(), "virtual://movie/tt1234", 0, "", VirtualPlaybackRouting{OwnerInstallationID: 101},
		); err == nil {
			t.Fatalf("invalid display metadata was accepted: %#v", metadata)
		}
	}
}

func TestVirtualPlaybackFallsBackWhenSelectedCandidateDisappearsOrExpires(t *testing.T) {
	expired := virtualCandidate("expired", "https://1.1.1.1/expired")
	expired.ExpiresAt = timestamppb.New(time.Now().Add(-time.Minute))
	fallback := virtualCandidate("fallback", "https://1.1.1.1/fallback")
	fallback.Rank = 2
	service, calls := newVirtualPlaybackTestService(t,
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(expired, fallback), nil
		},
	)
	routing := VirtualPlaybackRouting{OwnerInstallationID: 101}
	for _, selected := range []string{"expired", "no-longer-returned"} {
		resolved, err := service.ResolveVirtualPlaybackWithRouting(
			context.Background(), "virtual://movie/tt1234?result="+selected, 7, "p1", routing,
		)
		if err != nil {
			t.Fatalf("resolve stale selection %q: %v", selected, err)
		}
		if resolved != "https://1.1.1.1/fallback" {
			t.Fatalf("resolved stale selection %q to %q", selected, resolved)
		}
	}
	if calls[101].Load() != 1 {
		t.Fatalf("provider called %d times, want one cached resolution", calls[101].Load())
	}
}

func TestVirtualPlaybackStaleSelectionIsRejected(t *testing.T) {
	best := virtualCandidate("new-4k", "https://1.1.1.1/4k")
	best.Resolution.Label = "2160p"
	profileMatch := virtualCandidate("new-1080p", "https://1.1.1.1/1080p")
	profileMatch.Resolution.Label = "1080p"
	profileMatch.Rank = 2
	service, _ := newVirtualPlaybackTestService(t,
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(best, profileMatch), nil
		},
	)
	resolved, err := service.ResolveVirtualPlaybackWithRouting(
		context.Background(),
		"virtual://movie/tt1234?profile=1080p&result=rotated-away",
		7,
		"p1",
		VirtualPlaybackRouting{OwnerInstallationID: 101},
	)
	if err == nil {
		t.Fatalf("stale result unexpectedly resolved to %q", resolved)
	}
}

func TestVirtualPlaybackMissingOwnerFallsBackToReplacementProvider(t *testing.T) {
	service, calls := newVirtualPlaybackTestService(t,
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(virtualCandidate("old", "https://1.1.1.1/old")), nil
		},
		func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
			return virtualResponse(virtualCandidate("replacement", "https://1.1.1.1/replacement")), nil
		},
	)
	installationStore, ok := service.installations.(*fakeServiceInstallationStore)
	if !ok {
		t.Fatal("test service did not use the fake installation store")
	}
	delete(installationStore.byID, 101)
	resolved, err := service.ResolveVirtualPlaybackWithRouting(
		context.Background(),
		"virtual://movie/tt1234?result=old",
		7,
		"p1",
		VirtualPlaybackRouting{OwnerInstallationID: 101, AllowFallback: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "https://1.1.1.1/replacement" {
		t.Fatalf("replacement provider resolved %q", resolved)
	}
	if calls[101].Load() != 0 || calls[102].Load() != 1 {
		t.Fatalf("provider calls = old:%d replacement:%d", calls[101].Load(), calls[102].Load())
	}
}

func TestVirtualPlaybackCacheIsBoundedAndLifecycleInvalidated(t *testing.T) {
	service := &Service{}
	now := time.Now()
	for i := 0; i < maxVirtualPlaybackCacheEntries+25; i++ {
		service.storeVirtualStreamResult(
			fmt.Sprintf("key-%d", i),
			virtualResponse(virtualCandidate(fmt.Sprintf("candidate-%d", i), "https://1.1.1.1/stream")).GetResult(),
			now.Add(time.Duration(i)*time.Millisecond),
		)
	}
	if got := len(service.virtualStreamsCache); got != maxVirtualPlaybackCacheEntries {
		t.Fatalf("cache entries = %d, want %d", got, maxVirtualPlaybackCacheEntries)
	}
	if got := virtualStreamsCacheSize(service.virtualStreamsCache); got > maxVirtualPlaybackCacheBytes {
		t.Fatalf("cache size = %d, limit %d", got, maxVirtualPlaybackCacheBytes)
	}
	service.invalidateInstallationCache()
	if service.virtualStreamsCache != nil {
		t.Fatalf("lifecycle invalidation retained %d cache entries", len(service.virtualStreamsCache))
	}
}

func TestVirtualPlaybackCacheIsBoundedByAggregateBytes(t *testing.T) {
	service := &Service{}
	largeCandidates := make([]*pluginv1.VirtualStreamCandidate, 4)
	for index := range largeCandidates {
		candidate := virtualCandidate(fmt.Sprintf("large-%d", index), fmt.Sprintf("https://1.1.1.1/large-%d", index))
		candidate.Metadata, _ = structpb.NewStruct(map[string]any{
			"payload": strings.Repeat("x", 60<<10),
		})
		largeCandidates[index] = candidate
	}
	result := virtualResponse(largeCandidates...).GetResult()
	now := time.Now()
	for index := 0; index < 100; index++ {
		service.storeVirtualStreamResult(fmt.Sprintf("large-key-%d", index), result, now.Add(time.Duration(index)*time.Millisecond))
	}
	if got := virtualStreamsCacheSize(service.virtualStreamsCache); got > maxVirtualPlaybackCacheBytes {
		t.Fatalf("cache size = %d, limit %d", got, maxVirtualPlaybackCacheBytes)
	}
	if got := len(service.virtualStreamsCache); got >= 100 {
		t.Fatalf("byte ceiling did not evict entries: retained %d", got)
	}
}

func TestVirtualStreamRequestRejectsTraversalAndMalformedEpisodes(t *testing.T) {
	for _, raw := range []string{
		"virtual://series/tt123/1",
		"virtual://series/tt123/0/1",
		"virtual://series/tt123/%2e%2e/1",
		"virtual://movie/tt123/extra",
		"virtual://movie/%2fetc",
	} {
		if _, _, err := virtualStreamRequest(raw, 0, ""); err == nil {
			t.Fatalf("virtualStreamRequest(%q) succeeded", raw)
		}
	}
}

func TestVirtualStreamRequestAcceptsNamespacedFallbackIDs(t *testing.T) {
	tests := []struct {
		name      string
		uri       string
		mediaType string
		ids       map[string]string
		season    int32
		episode   int32
	}{
		{
			name:      "tmdb movie",
			uri:       "virtual://movie/tmdb/603",
			mediaType: "movie",
			ids:       map[string]string{"tmdb": "603"},
		},
		{
			name:      "tvdb episode",
			uri:       "virtual://series/tvdb/393159/3/1",
			mediaType: "episode",
			ids:       map[string]string{"tvdb": "393159"},
			season:    3,
			episode:   1,
		},
		{
			name:      "tmdb episode",
			uri:       "virtual://series/tmdb/202555/1/2",
			mediaType: "episode",
			ids:       map[string]string{"tmdb": "202555"},
			season:    1,
			episode:   2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, _, err := virtualStreamRequest(tt.uri, 0, "")
			if err != nil {
				t.Fatalf("virtualStreamRequest(): %v", err)
			}
			if request.GetMediaType() != tt.mediaType ||
				request.GetSeasonNumber() != tt.season ||
				request.GetEpisodeNumber() != tt.episode {
				t.Fatalf("request = %+v", request)
			}
			if len(request.GetExternalIds()) != len(tt.ids) {
				t.Fatalf("external IDs = %v, want %v", request.GetExternalIds(), tt.ids)
			}
			for provider, want := range tt.ids {
				if got := request.GetExternalIds()[provider]; got != want {
					t.Fatalf("%s ID = %q, want %q", provider, got, want)
				}
			}
		})
	}
}

func newVirtualPlaybackTestService(
	t *testing.T,
	resolvers ...func(context.Context, *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error),
) (*Service, map[int]*atomic.Int32) {
	t.Helper()
	manifest := testPluginManifest(t, "test.virtual", "1.0.0")
	manifest.Capabilities = []*pluginv1.CapabilityDescriptor{{Type: virtualStreamProviderCapabilityType, Id: testVirtualCapabilityID}}
	installPath := writeInstalledPluginManifest(t, manifest)
	store := newFakeServiceInstallationStore()
	store.listCapabilities = []*Capability{{Type: virtualStreamProviderCapabilityType, ID: testVirtualCapabilityID}}
	host := &fakeVirtualPluginHost{clients: make(map[int]pluginClient)}
	calls := make(map[int]*atomic.Int32)
	for index, resolver := range resolvers {
		id := 101 + index
		installation := &Installation{ID: id, PluginID: "test.virtual", Version: "1.0.0", InstallPath: installPath, Enabled: true}
		store.byID[id] = installation
		store.byPluginID[installation.PluginID] = append(store.byPluginID[installation.PluginID], installation)
		counter := &atomic.Int32{}
		calls[id] = counter
		resolve := resolver
		host.clients[id] = &fakePluginClient{
			manifest: manifest,
			virtualStreamClient: pluginhost.NewVirtualStreamProviderClientForTest(&fakeVirtualStreamGRPCClient{
				resolveFunc: func(ctx context.Context, request *pluginv1.ResolveVirtualStreamRequest) (*pluginv1.ResolveVirtualStreamResponse, error) {
					counter.Add(1)
					if request.GetCapabilityId() != testVirtualCapabilityID {
						t.Errorf("capability ID = %q", request.GetCapabilityId())
					}
					return resolve(ctx, request)
				},
			}, time.Second),
		}
	}
	return &Service{installations: store, host: host}, calls
}

func virtualResponse(candidates ...*pluginv1.VirtualStreamCandidate) *pluginv1.ResolveVirtualStreamResponse {
	return &pluginv1.ResolveVirtualStreamResponse{Result: &pluginv1.VirtualStreamResult{
		ResultId: "result", ProviderId: "test.provider", Candidates: candidates,
	}}
}

func virtualCandidate(id, streamURL string) *pluginv1.VirtualStreamCandidate {
	return &pluginv1.VirtualStreamCandidate{
		CandidateId: id, ProviderId: "test.provider", TemporaryUri: streamURL, Rank: 1,
		Resolution: &pluginv1.VirtualStreamResolution{Label: "1080p"},
		VideoCodec: "h264", AudioCodec: "aac", Container: "mp4",
	}
}
