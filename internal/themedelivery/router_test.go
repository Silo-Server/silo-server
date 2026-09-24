package themedelivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

const testSecret = "theme-router-secret"

type fakePlanner struct {
	proxies, transcodes []*nodepool.Node
	released            []string
}

func (p *fakePlanner) PlanRoute(req nodepool.RouteRequest) nodepool.Plan {
	pick := func(nodes []*nodepool.Node, eligible func(*nodepool.Node) bool) *nodepool.Node {
		for _, n := range nodes {
			if eligible == nil || eligible(n) {
				return n
			}
		}
		return nil
	}
	var plan nodepool.Plan
	if req.NeedsTranscode {
		if plan.TranscodeNode = pick(p.transcodes, req.TranscodeEligible); plan.TranscodeNode == nil {
			return nodepool.Plan{}
		}
	}
	if req.NeedsProxy {
		if plan.ProxyNode = pick(p.proxies, req.ProxyEligible); plan.ProxyNode == nil {
			return nodepool.Plan{}
		}
	}
	return plan
}

func (p *fakePlanner) ReleaseSession(id string) { p.released = append(p.released, id) }

type fakeRecipes struct {
	cards map[string]playback.RecipeCard
	ttls  map[string]time.Duration
	err   error
}

func (s *fakeRecipes) Enabled() bool { return true }

func (s *fakeRecipes) PutTTL(_ context.Context, id string, card playback.RecipeCard, ttl time.Duration) error {
	if s.err != nil {
		return s.err
	}
	if s.cards == nil {
		s.cards, s.ttls = map[string]playback.RecipeCard{}, map[string]time.Duration{}
	}
	s.cards[id], s.ttls[id] = card, ttl
	return nil
}

func node(t *testing.T, id int, url string, features []string, aac bool) *nodepool.Node {
	t.Helper()
	info := playback.HWAccelInfo{TransportFeatures: features}
	if aac {
		info.Transformations = []playback.TransformationV3{{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3}}
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	return &nodepool.Node{ID: id, URL: url, Healthy: true, Enabled: true, Capabilities: raw}
}

func themeProxy(t *testing.T) *nodepool.Node {
	return node(t, 11, "http://proxy-a", []string{playback.TransportFeatureThemeAudioEgressV1, playback.TransportFeatureProgressiveRemuxRelayV1}, true)
}

func themeTranscode(t *testing.T) *nodepool.Node {
	return node(t, 21, "http://transcode-a", []string{playback.TransportFeatureThemeAudioExecutionV1, playback.TransportFeatureProgressiveRemuxExecutionV1}, true)
}

func themeRequest(delivery themesongs.Delivery) Request {
	modified := time.Unix(1_700_000_000, 123_000).UTC()
	file := themesongs.File{Song: themesongs.Song{ID: "42", Container: "ogg"}, AudioCodec: "vorbis", AudioChannels: 2, BitrateKbps: 160, Path: "/media/Movie/theme.ogg", Size: 4096, Modified: modified}
	return Request{File: file, Delivery: delivery, Conversion: themesongs.ConversionFor(file), UserID: 7, ProfileID: "profile", ExpiresAt: time.Now().Add(4 * time.Minute)}
}

func newRouter(planner *fakePlanner, recipes RecipeStore, policy config.PlaybackRoutingPolicy, localConversion bool) *Router {
	return &Router{
		Planner:         planner,
		Secret:          func() string { return testSecret },
		Recipes:         recipes,
		Policy:          func() config.PlaybackRoutingPolicy { return policy },
		LocalConversion: func(context.Context) bool { return localConversion },
	}
}

func tokenClaims(t *testing.T, location, proxyURL string) *streamtoken.Claims {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(location, proxyURL+ProxyPath) {
		t.Fatalf("location %q is not a %s theme route", location, proxyURL)
	}
	claims, err := streamtoken.Verify(strings.TrimPrefix(parsed.Path, ProxyPath), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestDefaultPolicyRoutesOriginalThroughProxy(t *testing.T) {
	planner := &fakePlanner{proxies: []*nodepool.Node{themeProxy(t)}, transcodes: []*nodepool.Node{themeTranscode(t)}}
	req := themeRequest(themesongs.DeliveryOriginal)
	result, err := newRouter(planner, &fakeRecipes{}, config.DefaultPlaybackRoutingPolicy(), true).Resolve(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Shape.ID != "direct_proxy" || result.Local() {
		t.Fatalf("shape = %+v url=%q", result.Shape, result.URL)
	}
	claims := tokenClaims(t, result.URL, "http://proxy-a")
	if claims.PlayMethod != streamtoken.PlayMethodThemeDirect || claims.MediaPath != req.File.Path ||
		claims.RoutingWorkload != string(noderouting.WorkloadDirectPlay) || claims.RoutingExecution != string(noderouting.ExecutionNone) ||
		claims.RoutingEgress != string(noderouting.EgressProxy) || claims.RoutingEgressNodeID != 11 ||
		claims.ThemeID != 42 || claims.ThemeSize != 4096 || claims.ThemeModifiedUnixNano != req.File.Modified.UnixNano() ||
		!strings.HasPrefix(claims.SessionID, IDPrefix) || claims.TranscodeAudio || claims.UserID != 7 || claims.ProfileID != "profile" {
		t.Fatalf("claims = %+v", claims)
	}
	if claims.ExpiresAt == nil || claims.ExpiresAt.After(req.ExpiresAt.Add(time.Second)) {
		t.Fatalf("token outlives its grant: %v > %v", claims.ExpiresAt, req.ExpiresAt)
	}
}

func TestDefaultPolicyConvertsOnTranscodeNodeRelayedByProxy(t *testing.T) {
	planner := &fakePlanner{proxies: []*nodepool.Node{themeProxy(t)}, transcodes: []*nodepool.Node{themeTranscode(t)}}
	recipes := &fakeRecipes{}
	req := themeRequest(themesongs.DeliveryConverted)
	result, err := newRouter(planner, recipes, config.DefaultPlaybackRoutingPolicy(), true).Resolve(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Shape.ID != noderouting.ShapeProgressiveRemuxTranscodeProxy {
		t.Fatalf("shape = %+v", result.Shape)
	}
	claims := tokenClaims(t, result.URL, "http://proxy-a")
	card, ok := recipes.cards[claims.SessionID]
	if !ok {
		t.Fatal("transcode conversion published without its node authority")
	}
	if ttl := recipes.ttls[claims.SessionID]; ttl <= 0 || ttl > 4*time.Minute+recipeGrace {
		t.Fatalf("recipe ttl = %v", ttl)
	}
	// These are the fields the transcode node compares before it starts FFmpeg.
	stored := card.ToClaims()
	if stored.SessionID != claims.SessionID || stored.MediaPath != claims.MediaPath || stored.PlayMethod != claims.PlayMethod ||
		stored.TranscodeNode != claims.TranscodeNode || stored.TranscodeTransportID != claims.SessionID ||
		stored.RoutingWorkload != claims.RoutingWorkload || stored.RoutingExecution != claims.RoutingExecution ||
		stored.RoutingExecutionNodeID != claims.RoutingExecutionNodeID || stored.RoutingEgress != claims.RoutingEgress ||
		stored.RoutingEgressNodeID != claims.RoutingEgressNodeID {
		t.Fatalf("stored authority %+v does not match token %+v", stored, claims)
	}
	if claims.PlayMethod != streamtoken.PlayMethodThemeAAC || claims.TranscodeNode != "http://transcode-a" || claims.RoutingExecutionNodeID != 21 ||
		claims.RoutingEgressNodeID != 11 || !claims.TranscodeAudio || !claims.AudioOnly || claims.TargetCodecAudio != "aac" ||
		claims.TargetAudioChannels != 2 || claims.TargetAudioBitrateKbps != 192 {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestOlderWorkersAreSkipped(t *testing.T) {
	oldTranscode := node(t, 22, "http://transcode-old", []string{playback.TransportFeatureProgressiveRemuxExecutionV1}, true)
	planner := &fakePlanner{proxies: []*nodepool.Node{themeProxy(t)}, transcodes: []*nodepool.Node{oldTranscode}}
	recipes := &fakeRecipes{}
	result, err := newRouter(planner, recipes, config.DefaultPlaybackRoutingPolicy(), true).Resolve(t.Context(), themeRequest(themesongs.DeliveryConverted))
	if err != nil {
		t.Fatal(err)
	}
	if result.Shape.ID != "progressive_remux_proxy" || len(recipes.cards) != 0 {
		t.Fatalf("shape = %+v recipes=%v", result.Shape, recipes.cards)
	}
	claims := tokenClaims(t, result.URL, "http://proxy-a")
	if claims.RoutingExecution != string(noderouting.ExecutionProxy) || claims.RoutingExecutionNodeID != 11 || claims.TranscodeNode != "" {
		t.Fatalf("claims = %+v", claims)
	}

	oldProxy := node(t, 12, "http://proxy-old", []string{playback.TransportFeatureProgressiveRemuxRelayV1}, true)
	result, err = newRouter(&fakePlanner{proxies: []*nodepool.Node{oldProxy}}, recipes, config.DefaultPlaybackRoutingPolicy(), true).Resolve(t.Context(), themeRequest(themesongs.DeliveryOriginal))
	if err != nil || !result.Local() || result.Shape.ID != "direct_api" {
		t.Fatalf("older proxy result = %+v err=%v", result, err)
	}
}

func TestRoutingPolicyBoundaries(t *testing.T) {
	proxyOnly := config.DefaultPlaybackRoutingPolicy()
	proxyOnly.DirectPlayEgress = config.PlaybackEgressProxyOnly
	if _, err := newRouter(&fakePlanner{}, &fakeRecipes{}, proxyOnly, true).Resolve(t.Context(), themeRequest(themesongs.DeliveryOriginal)); !errors.Is(err, ErrCapacityUnavailable) {
		t.Fatalf("proxy_only without a theme proxy: %v", err)
	}
	noSecret := newRouter(&fakePlanner{proxies: []*nodepool.Node{themeProxy(t)}}, &fakeRecipes{}, proxyOnly, true)
	noSecret.Secret = func() string { return "" }
	if _, err := noSecret.Resolve(t.Context(), themeRequest(themesongs.DeliveryOriginal)); !errors.Is(err, ErrPolicyUnsatisfied) {
		t.Fatalf("proxy_only without a token secret: %v", err)
	}
	apiOnly := config.DefaultPlaybackRoutingPolicy()
	apiOnly.DirectPlayEgress = config.PlaybackEgressAPIOnly
	result, err := newRouter(&fakePlanner{proxies: []*nodepool.Node{themeProxy(t)}}, &fakeRecipes{}, apiOnly, true).Resolve(t.Context(), themeRequest(themesongs.DeliveryOriginal))
	if err != nil || !result.Local() {
		t.Fatalf("api_only result = %+v err=%v", result, err)
	}
	// Conversion with no worker and no local AAC recipe has nowhere to run.
	if _, err := newRouter(&fakePlanner{}, &fakeRecipes{}, config.DefaultPlaybackRoutingPolicy(), false).Resolve(t.Context(), themeRequest(themesongs.DeliveryConverted)); err == nil {
		t.Fatal("conversion resolved without an executor")
	}
	result, err = (&Router{}).Resolve(t.Context(), themeRequest(themesongs.DeliveryOriginal))
	if err != nil || !result.Local() {
		t.Fatalf("zero router result = %+v err=%v", result, err)
	}
}

func TestUnpublishableRouteYieldsToTheNext(t *testing.T) {
	planner := &fakePlanner{proxies: []*nodepool.Node{themeProxy(t)}, transcodes: []*nodepool.Node{themeTranscode(t)}}
	recipes := &fakeRecipes{err: errors.New("redis down")}
	result, err := newRouter(planner, recipes, config.DefaultPlaybackRoutingPolicy(), true).Resolve(t.Context(), themeRequest(themesongs.DeliveryConverted))
	if err != nil {
		t.Fatal(err)
	}
	if result.Shape.ID != "progressive_remux_proxy" || len(planner.released) == 0 || !strings.HasPrefix(planner.released[0], IDPrefix) {
		t.Fatalf("shape = %+v released=%v", result.Shape, planner.released)
	}

	// A client on an overlay network cannot reach a proxy with no origin there.
	overlay := netaccess.Path{Provider: "tailscale"}
	req := themeRequest(themesongs.DeliveryOriginal)
	req.AccessPath = overlay
	result, err = newRouter(&fakePlanner{proxies: []*nodepool.Node{themeProxy(t)}}, &fakeRecipes{}, config.DefaultPlaybackRoutingPolicy(), true).Resolve(t.Context(), req)
	if err != nil || !result.Local() {
		t.Fatalf("overlay result = %+v err=%v", result, err)
	}
}

func TestConversionSeekTravelsInTheURL(t *testing.T) {
	planner := &fakePlanner{proxies: []*nodepool.Node{themeProxy(t)}}
	req := themeRequest(themesongs.DeliveryConverted)
	req.SeekSeconds = 12.5
	result, err := newRouter(planner, &fakeRecipes{}, config.DefaultPlaybackRoutingPolicy(), false).Resolve(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil || parsed.Query().Get("seek") != "12.500" {
		t.Fatalf("url = %q", result.URL)
	}
	if _, err := newRouter(planner, &fakeRecipes{}, config.DefaultPlaybackRoutingPolicy(), false).Resolve(t.Context(), Request{}); !errors.Is(err, themesongs.ErrNotFound) {
		t.Fatalf("empty request: %v", err)
	}
}

type listingPlanner struct {
	fakePlanner
}

func (p *listingPlanner) ProxyNodeURLs() []string { return urls(p.proxies) }
func (p *listingPlanner) ProxyNodeByURL(u string) (*nodepool.Node, bool) {
	return byURL(p.proxies, u)
}
func (p *listingPlanner) TranscodeNodeURLs() []string { return urls(p.transcodes) }
func (p *listingPlanner) TranscodeNodeByURL(u string) (*nodepool.Node, bool) {
	return byURL(p.transcodes, u)
}

func urls(nodes []*nodepool.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.URL)
	}
	return out
}

func byURL(nodes []*nodepool.Node, u string) (*nodepool.Node, bool) {
	for _, n := range nodes {
		if n.URL == u {
			return n, true
		}
	}
	return nil, false
}

func TestCanConvertMatchesResolvableRoutes(t *testing.T) {
	egressOnly := node(t, 13, "http://proxy-egress", []string{playback.TransportFeatureThemeAudioEgressV1}, false)
	relayProxy := node(t, 14, "http://proxy-relay", []string{playback.TransportFeatureThemeAudioEgressV1, playback.TransportFeatureProgressiveRemuxRelayV1}, false)
	oldTranscode := node(t, 23, "http://transcode-old", []string{playback.TransportFeatureThemeAudioExecutionV1}, true)
	for _, tc := range []struct {
		name    string
		planner *listingPlanner
		recipes RecipeStore
		want    bool
	}{
		{"converting proxy", &listingPlanner{fakePlanner{proxies: []*nodepool.Node{themeProxy(t)}}}, nil, true},
		{"transcode node behind a relaying proxy", &listingPlanner{fakePlanner{proxies: []*nodepool.Node{relayProxy}, transcodes: []*nodepool.Node{themeTranscode(t)}}}, &fakeRecipes{}, true},
		{"transcode node without a relaying proxy", &listingPlanner{fakePlanner{proxies: []*nodepool.Node{egressOnly}, transcodes: []*nodepool.Node{themeTranscode(t)}}}, &fakeRecipes{}, false},
		{"transcode node without a recipe store", &listingPlanner{fakePlanner{proxies: []*nodepool.Node{relayProxy}, transcodes: []*nodepool.Node{themeTranscode(t)}}}, nil, false},
		{"transcode node without progressive execution", &listingPlanner{fakePlanner{proxies: []*nodepool.Node{relayProxy}, transcodes: []*nodepool.Node{oldTranscode}}}, &fakeRecipes{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := &Router{Planner: tc.planner}
			if tc.recipes != nil {
				router.Recipes = tc.recipes
			}
			if got := router.CanConvert(t.Context()); got != tc.want {
				t.Fatalf("CanConvert = %v, want %v", got, tc.want)
			}
		})
	}
	if !(&Router{LocalConversion: func(context.Context) bool { return true }}).CanConvert(t.Context()) {
		t.Fatal("local AAC recipe not counted")
	}
}
