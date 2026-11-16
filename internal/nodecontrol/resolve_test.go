package nodecontrol

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

const resolveSecret = "resolve-test-secret"

// recipeMap is a recipe store keyed by transport id; err fails every read.
type recipeMap struct {
	mu    sync.Mutex
	cards map[string]playback.RecipeCard
	err   error
}

func (m *recipeMap) Lookup(_ context.Context, transportID string) (*playback.RecipeCard, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, false, m.err
	}
	card, ok := m.cards[transportID]
	if !ok {
		return nil, false, nil
	}
	return &card, true, nil
}

func (m *recipeMap) put(transportID string, card playback.RecipeCard) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cards[transportID] = card
}

func (m *recipeMap) fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// denySet is a deny marker store.
type denySet map[string]bool

func (d denySet) Denied(_ context.Context, sessionID string) bool { return d[sessionID] }

// resolveCore is the core's resolve route behind RequireNode, over an
// in-memory node table, and the client of each node it registers.
type resolveCore struct {
	t       *testing.T
	nodes   *fakeNodes
	recipes *recipeMap
	deny    denySet
	catalog *fakeCatalog
	url     string
	keys    map[int]string
}

// newResolveCore registers transcode nodes by id and URL and serves resolve
// for them from recipes, or from an in-memory recipe map when recipes is nil.
func newResolveCore(t *testing.T, nodeURLs map[int]string, recipes RecipeSource) *resolveCore {
	t.Helper()
	c := &resolveCore{t: t, nodes: &fakeNodes{}, recipes: &recipeMap{cards: map[string]playback.RecipeCard{}}, deny: denySet{}, catalog: &fakeCatalog{}, keys: map[int]string{}}
	for id, url := range nodeURLs {
		c.keys[id] = c.nodes.add(t, nodepool.Node{ID: id, Name: "gpu", Type: nodepool.NodeTypeTranscode, URL: url, Enabled: true})
	}
	if recipes == nil {
		recipes = c.recipes
	}
	revocations, _ := recipes.(RecipeRevoker)
	server := NewServer(Options{
		Nodes: c.nodes, Identities: c.nodes,
		StreamSecret: func() string { return resolveSecret },
		Recipes:      recipes,
		Deny:         c.deny,
		Revocations:  revocations,
		Grants:       stubGrants{},
		Files:        c.catalog,
		Themes:       c.catalog,
	})
	core := httptest.NewServer(mountForTest(server, c.nodes))
	t.Cleanup(core.Close)
	c.url = core.URL
	return c
}

// client is node nodeID's control plane client.
func (c *resolveCore) client(nodeID int) *Client {
	c.t.Helper()
	client, err := NewClient(c.url, c.keys[nodeID])
	if err != nil {
		c.t.Fatal(err)
	}
	return client
}

// resolve asks the core as node nodeID and returns the resolution, or the
// refusal's status and reason.
func (c *resolveCore) resolve(nodeID int, purpose, transportID, token string) (*Resolution, int, string) {
	c.t.Helper()
	resolution, err := c.client(nodeID).Resolve(c.t.Context(), ResolveRequest{TransportID: transportID, Purpose: purpose, StreamToken: token})
	if err != nil {
		if Status(err) == 0 {
			c.t.Fatalf("resolve: %v", err)
		}
		return nil, Status(err), Reason(err)
	}
	return resolution, http.StatusOK, ""
}

func signClaims(t *testing.T, claims streamtoken.Claims, secret string) string {
	t.Helper()
	token, err := streamtoken.Sign(claims, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

const (
	nodeA, nodeB       = 3, 12
	nodeAURL, nodeBURL = "http://node-3:8090", "http://node-12:8090"
)

var twoNodes = map[int]string{nodeA: nodeAURL, nodeB: nodeBURL}

// nativeCard is a complete native transcode recipe of playback session
// sessionID, run under a plan-scoped transport on node nodeA.
func nativeCard(sessionID, transportID string) playback.RecipeCard {
	card := playback.NewRecipeCard(7, "profile-1", 42, nodeAURL, playback.TranscodeOpts{
		SessionID: sessionID, InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 6,
	})
	card.TranscodeTransportID = transportID
	card.RoutingWorkload, card.RoutingExecutionNodeID = string(noderouting.WorkloadVideoTranscode), nodeA
	return card
}

func wantRefusal(t *testing.T, gotStatus int, gotReason string, status int, reason string) {
	t.Helper()
	if gotStatus != status || gotReason != reason {
		t.Fatalf("resolve = %d %q, want %d %q", gotStatus, gotReason, status, reason)
	}
}

// A lost HLS session resolves from a complete token, from the stored card
// when the token is identity-only or absent, and carries the playback
// identity and the session's own start, never the rebuild's.
func TestResolveHLSRecipeSources(t *testing.T) {
	core := newResolveCore(t, twoNodes, nil)
	original := time.Unix(1_700_000_000, 0).UTC()

	minted := nativeCard("viewer-1", "viewer-1-plan")
	minted.OriginalStartedAt = original
	resolution, status, reason := core.resolve(nodeA, ResolvePurposeHLS, "viewer-1-plan", signClaims(t, minted.ToClaims(), resolveSecret))
	if status != http.StatusOK {
		t.Fatalf("complete token: resolve = %d %q", status, reason)
	}
	if resolution.Recipe.TargetCodecVideo != "h264" || resolution.Recipe.InputPath != "/media/movie.mkv" ||
		resolution.PlaybackSessionID != "viewer-1" || resolution.UserID != 7 || resolution.ProfileID != "profile-1" || resolution.MediaFileID != 42 {
		t.Fatalf("complete token: resolution = %+v", resolution)
	}
	if !resolution.StartedAt.Equal(original) || resolution.StartedAtSource != string(streamtoken.StartedAtSourceClaim) {
		t.Fatalf("complete token: started at = %v %q", resolution.StartedAt, resolution.StartedAtSource)
	}

	// jellycompat: keyed by the upstream session, an identity-only node-hop
	// token or none at all.
	stored := nativeCard("compat-1", "")
	stored.TargetCodecVideo, stored.OriginalStartedAt = "hevc", original
	core.recipes.put("compat-1", stored)
	identityOnly := signClaims(t, playback.RecipeCard{SessionID: "compat-1", UserID: 7, PlayMethod: playback.PlayTranscode, InputPath: "/media/movie.mkv"}.ToClaims(), resolveSecret)
	for name, token := range map[string]string{"identity-only token": identityOnly, "no token": ""} {
		resolution, status, reason := core.resolve(nodeA, ResolvePurposeHLS, "compat-1", token)
		if status != http.StatusOK {
			t.Fatalf("%s: resolve = %d %q", name, status, reason)
		}
		if resolution.Recipe.TargetCodecVideo != "hevc" || resolution.PlaybackSessionID != "compat-1" || !resolution.StartedAt.Equal(original) {
			t.Fatalf("%s: resolution = %+v", name, resolution)
		}
	}

	before := time.Now().Add(-time.Second)
	resolution, status, reason = core.resolve(nodeA, ResolvePurposeHLS, "viewer-2-plan", signClaims(t, nativeCard("viewer-2", "viewer-2-plan").ToClaims(), resolveSecret))
	if status != http.StatusOK || resolution.StartedAt.Before(before) || resolution.StartedAtSource != string(streamtoken.StartedAtSourceIssuedAt) {
		t.Fatalf("token without an original start: %d %q, started at %+v, want its issue time", status, reason, resolution)
	}
}

func TestResolveHLSRefusals(t *testing.T) {
	core := newResolveCore(t, twoNodes, nil)
	core.recipes.put("incomplete", playback.RecipeCard{SessionID: "incomplete", PlayMethod: playback.PlayTranscode, TranscodeNodeURL: nodeAURL})
	core.recipes.put("keyed-wrong", nativeCard("viewer-x", "another-transport"))
	legacyCopy := playback.NewRecipeCard(7, "profile-1", 42, nodeAURL, playback.TranscodeOpts{SessionID: "legacy-copy", TargetCodecVideo: "copy", SegmentDuration: 2})
	legacyCopy.PlayMethod, legacyCopy.CopyFMP4RecipeVersion = playback.PlayTranscode, ""
	core.recipes.put("legacy-copy", legacyCopy)
	core.recipes.put("stored-denied", nativeCard("denied-viewer", "stored-denied"))
	core.deny["denied-transport"] = true
	core.deny["denied-viewer"] = true

	for _, test := range []struct {
		name, transport, token string
		status                 int
		reason                 string
	}{
		{"store miss", "unknown", "", http.StatusNotFound, ResolveNotFound},
		{"incomplete stored recipe", "incomplete", "", http.StatusNotFound, ResolveNotFound},
		{"stored recipe of another transport", "keyed-wrong", "", http.StatusNotFound, ResolveNotFound},
		{"unversioned copy recipe", "legacy-copy", "", http.StatusNotFound, ResolveNotFound},
		{"bad signature", "t-1", signClaims(t, nativeCard("v", "t-1").ToClaims(), "wrong-secret"), http.StatusNotFound, ResolveNotFound},
		{"token for another transport", "t-1", signClaims(t, nativeCard("v", "t-2").ToClaims(), resolveSecret), http.StatusNotFound, ResolveNotFound},
		{"direct-play token", "t-1", signClaims(t, playback.NewDirectRecipeCard("t-1", 7, "p", 42).ToClaims(), resolveSecret), http.StatusNotFound, ResolveNotFound},
		{"denied transport", "denied-transport", "", http.StatusGone, ResolveDenied},
		{"denied token session", "t-3", signClaims(t, nativeCard("denied-viewer", "t-3").ToClaims(), resolveSecret), http.StatusGone, ResolveDenied},
		{"denied stored session", "stored-denied", "", http.StatusGone, ResolveDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, status, reason := core.resolve(nodeA, ResolvePurposeHLS, test.transport, test.token)
			wantRefusal(t, status, reason, test.status, test.reason)
		})
	}

	// A store that cannot be read (a Redis blip) is no evidence that the
	// session ended: 503, which the node retries, never a 404 that ends it.
	core.recipes.fail(errors.New("redis: connection refused"))
	_, status, reason := core.resolve(nodeA, ResolvePurposeHLS, "stored-denied", "")
	wantRefusal(t, status, reason, http.StatusServiceUnavailable, "")
}

// Transport ids are not secrets: a node may only rebuild a transcode bound to
// it, whether the card arrives in a token or from the store, and whether the
// execution node id or only the dialed URL binds it.
func TestResolveHLSBindsTheCallingNode(t *testing.T) {
	core := newResolveCore(t, twoNodes, nil)
	bound := func(transportID string, executionNodeID int, nodeURL string) playback.RecipeCard {
		card := nativeCard("viewer-"+transportID, transportID)
		card.RoutingExecutionNodeID, card.TranscodeNodeURL = executionNodeID, nodeURL
		return card
	}
	for _, test := range []struct {
		name   string
		card   playback.RecipeCard
		status int
		reason string
	}{
		{"own node id", bound("own-id", nodeA, nodeBURL), http.StatusOK, ""},
		{"another node id", bound("other-id", nodeB, nodeAURL), http.StatusForbidden, ResolveWrongRoute},
		{"own URL respelled", bound("own-url", 0, " HTTP://Node-3:8090/ "), http.StatusOK, ""},
		{"another node URL", bound("other-url", 0, nodeBURL), http.StatusForbidden, ResolveWrongRoute},
		{"unbound", bound("unbound", 0, ""), http.StatusForbidden, ResolveWrongRoute},
	} {
		t.Run(test.name+" token", func(t *testing.T) {
			_, status, reason := core.resolve(nodeA, ResolvePurposeHLS, test.card.TranscodeTransportID, signClaims(t, test.card.ToClaims(), resolveSecret))
			wantRefusal(t, status, reason, test.status, test.reason)
		})
		t.Run(test.name+" store", func(t *testing.T) {
			core.recipes.put(test.card.TranscodeTransportID, test.card)
			_, status, reason := core.resolve(nodeA, ResolvePurposeHLS, test.card.TranscodeTransportID, "")
			wantRefusal(t, status, reason, test.status, test.reason)
		})
	}
}

// A transcode moved off an unreachable node keeps its transport id, and its
// client keeps presenting the token minted for the node it left. The move
// stores a card naming the node it runs on now, which wins over the token:
// the new node resolves with the old token, and the node the transport left
// is refused although the token names it. Once a stop deletes the card, the
// token binds only the node it was minted for.
func TestResolveHLSMovedTokenSessionResolvesOnItsNewNode(t *testing.T) {
	core := newResolveCore(t, twoNodes, nil)
	minted := nativeCard("viewer-1", "moved-transport")
	token := signClaims(t, minted.ToClaims(), resolveSecret)
	moved := minted
	moved.RoutingExecutionNodeID, moved.TranscodeNodeURL = nodeB, nodeBURL
	moved.EncoderHWAccel = "nvenc"
	core.recipes.put("moved-transport", moved)

	resolution, status, reason := core.resolve(nodeB, ResolvePurposeHLS, "moved-transport", token)
	if status != http.StatusOK {
		t.Fatalf("new node: resolve = %d %q", status, reason)
	}
	if resolution.Recipe.RoutingExecutionNodeID != nodeB || resolution.Recipe.EncoderHWAccel != "nvenc" || resolution.PlaybackSessionID != "viewer-1" {
		t.Fatalf("new node: resolution = %+v, want the stored card", resolution.Recipe)
	}
	_, status, reason = core.resolve(nodeA, ResolvePurposeHLS, "moved-transport", token)
	wantRefusal(t, status, reason, http.StatusForbidden, ResolveWrongRoute)

	core.recipes.mu.Lock()
	delete(core.recipes.cards, "moved-transport")
	core.recipes.mu.Unlock()
	_, status, reason = core.resolve(nodeB, ResolvePurposeHLS, "moved-transport", token)
	wantRefusal(t, status, reason, http.StatusForbidden, ResolveWrongRoute)
}

// Where both a complete stored card and a complete token describe a
// transport, track identity and the selected (Default) subtitle rendition
// come from the token, and only the presentation metadata the token has no
// room for from the card, as a node merged them when it read both itself.
func TestResolveHLSKeepsTheTokenRenditionSelection(t *testing.T) {
	core := newResolveCore(t, twoNodes, nil)
	card := nativeCard("viewer-1", "renditions")
	card.SubtitleRenditionSource = playback.SubtitleRenditionSource{Size: 100, ModTimeUnixNano: 5}
	card.SubtitleRenditionTracks = []playback.SubtitleRenditionTrack{
		{StreamIndex: 2, TrackNumber: 3, Name: "English", Language: "eng"},
		{StreamIndex: 3, TrackNumber: 4, Name: "French", Language: "fre", Default: true},
	}
	core.recipes.put("renditions", card)
	selected := card
	selected.SubtitleRenditionTracks = []playback.SubtitleRenditionTrack{{TrackNumber: 3, Default: true}, {TrackNumber: 4}}

	resolution, status, reason := core.resolve(nodeA, ResolvePurposeHLS, "renditions", signClaims(t, selected.ToClaims(), resolveSecret))
	if status != http.StatusOK {
		t.Fatalf("resolve = %d %q", status, reason)
	}
	got := resolution.Recipe.SubtitleRenditionTracks
	if len(got) != 2 || got[0].Name != "English" || got[1].Language != "fre" || !got[0].Default || got[1].Default {
		t.Fatalf("renditions = %+v, want the stored names with the token's default", got)
	}
}

// remuxClaims is a progressive remux token routed to node nodeID.
func remuxClaims(nodeID int, mediaPath string) streamtoken.Claims {
	return streamtoken.Claims{
		SessionID: "viewer-remux", UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
		MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: nodeAURL, TranscodeTransportID: "remux-transport",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: nodeID, RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeID: 2,
		OriginalStartedAtUnixNano: time.Unix(1_700_000_000, 0).UnixNano(),
	}
}

func (c *resolveCore) authorize(claims streamtoken.Claims) {
	c.recipes.put(claims.TranscodeTransportID, playback.RecipeCardFromClaims(&claims))
}

func TestResolveRemux(t *testing.T) {
	core := newResolveCore(t, twoNodes, nil)
	valid := remuxClaims(nodeA, "/media/movie.mkv")
	core.authorize(valid)
	resolution, status, reason := core.resolve(nodeA, ResolvePurposeRemux, "remux-transport", signClaims(t, valid, resolveSecret))
	if status != http.StatusOK {
		t.Fatalf("resolve = %d %q", status, reason)
	}
	if resolution.Recipe.InputPath != "/media/movie.mkv" || resolution.PlaybackSessionID != "viewer-remux" || resolution.UserID != 7 ||
		resolution.MediaFileID != 42 || !resolution.StartedAt.Equal(time.Unix(1_700_000_000, 0)) || resolution.Theme != nil {
		t.Fatalf("resolution = %+v", resolution)
	}

	otherNode := remuxClaims(nodeB, "/media/movie.mkv")
	otherNode.TranscodeTransportID = "other-node"
	core.authorize(otherNode)
	noNode := remuxClaims(0, "/media/movie.mkv")
	noNode.TranscodeTransportID = "no-node"
	core.authorize(noNode)
	apiEgress := remuxClaims(nodeA, "/media/movie.mkv")
	apiEgress.TranscodeTransportID, apiEgress.RoutingEgress = "api-egress", string(noderouting.EgressAPI)
	core.authorize(apiEgress)
	unauthorized := remuxClaims(nodeA, "/media/movie.mkv")
	unauthorized.TranscodeTransportID = "never-stored"
	stale := remuxClaims(nodeA, "/media/movie.mkv")
	stale.TranscodeTransportID = "stale"
	core.authorize(stale)
	stale.MediaPath = "/media/other.mkv"
	denied := remuxClaims(nodeA, "/media/movie.mkv")
	denied.TranscodeTransportID, denied.SessionID = "denied-remux", "denied-viewer"
	core.authorize(denied)
	core.deny["denied-viewer"] = true
	downmix := remuxClaims(nodeA, "/media/movie.mkv")
	downmix.TranscodeTransportID, downmix.PlayMethod = "downmix", streamtoken.PlayMethodAudioDownmixRemux
	downmix.TranscodeAudio, downmix.TargetCodecAudio, downmix.SourceAudioChannels, downmix.TargetAudioChannels = true, "aac", 6, 6
	core.authorize(downmix)

	for _, test := range []struct {
		name, transport, token string
		status                 int
		reason                 string
	}{
		{"no token", "remux-transport", "", http.StatusForbidden, ResolveInvalidToken},
		{"bad signature", "remux-transport", signClaims(t, valid, "wrong-secret"), http.StatusForbidden, ResolveInvalidToken},
		{"another transport in the URL", "not-the-token's", signClaims(t, valid, resolveSecret), http.StatusForbidden, ResolveWrongRoute},
		{"another node's route", "other-node", signClaims(t, otherNode, resolveSecret), http.StatusForbidden, ResolveWrongRoute},
		{"no node id", "no-node", signClaims(t, noNode, resolveSecret), http.StatusForbidden, ResolveWrongRoute},
		{"API egress", "api-egress", signClaims(t, apiEgress, resolveSecret), http.StatusForbidden, ResolveWrongRoute},
		{"authority never stored", "never-stored", signClaims(t, unauthorized, resolveSecret), http.StatusGone, ResolveStopped},
		{"authority for another recipe", "stale", signClaims(t, stale, resolveSecret), http.StatusGone, ResolveStopped},
		{"denied playback session", "denied-remux", signClaims(t, denied, resolveSecret), http.StatusGone, ResolveDenied},
		{"downmix of the wrong shape", "downmix", signClaims(t, downmix, resolveSecret), http.StatusNotFound, ResolveNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, status, reason := core.resolve(nodeA, ResolvePurposeRemux, test.transport, test.token)
			wantRefusal(t, status, reason, test.status, test.reason)
		})
	}

	core.recipes.fail(errors.New("redis: connection refused"))
	_, status, reason = core.resolve(nodeA, ResolvePurposeRemux, "remux-transport", signClaims(t, valid, resolveSecret))
	wantRefusal(t, status, reason, http.StatusServiceUnavailable, "")
}

// A theme conversion resolves only while the theme file is the one the token
// describes, and carries the theme's identity the recipe card has no field
// for.
func TestResolveThemeConversion(t *testing.T) {
	core := newResolveCore(t, twoNodes, nil)
	path := filepath.Join(t.TempDir(), "theme.mp3")
	if err := os.WriteFile(path, []byte("theme-audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	claims := remuxClaims(nodeA, path)
	claims.TranscodeTransportID, claims.PlayMethod = "theme-transport", streamtoken.PlayMethodThemeAAC
	claims.TranscodeAudio, claims.AudioOnly, claims.TargetCodecAudio = true, true, themesongs.CodecAAC
	claims.ThemeID, claims.ThemeSize, claims.ThemeModifiedUnixNano = 5, info.Size(), info.ModTime().Truncate(time.Microsecond).UnixNano()
	core.authorize(claims)
	token := signClaims(t, claims, resolveSecret)

	resolution, status, reason := core.resolve(nodeA, ResolvePurposeRemux, "theme-transport", token)
	if status != http.StatusOK || resolution.Theme == nil ||
		*resolution.Theme != (ResolvedTheme{ID: 5, Size: info.Size(), ModifiedUnixNano: claims.ThemeModifiedUnixNano}) {
		t.Fatalf("resolve = %d %q %+v", status, reason, resolution)
	}
	if err := os.WriteFile(path, []byte("replaced theme audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, status, reason = core.resolve(nodeA, ResolvePurposeRemux, "theme-transport", token)
	wantRefusal(t, status, reason, http.StatusGone, ResolveSourceChanged)
}

// A core without a stream secret verifies nothing, and a malformed request is
// refused before any lookup.
func TestResolveRequestGuards(t *testing.T) {
	nodes := &fakeNodes{}
	key := nodes.add(t, nodepool.Node{ID: nodeA, Type: nodepool.NodeTypeTranscode, URL: nodeAURL})
	core := httptest.NewServer(mountForTest(NewServer(Options{Nodes: nodes, StreamSecret: func() string { return "" }}), nodes))
	t.Cleanup(core.Close)
	client, err := NewClient(core.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resolve(t.Context(), ResolveRequest{TransportID: "t", Purpose: ResolvePurposeRemux, StreamToken: "x"}); Status(err) != http.StatusServiceUnavailable {
		t.Fatalf("resolve without a secret = %v, want 503", err)
	}
	for name, req := range map[string]ResolveRequest{
		"unknown purpose": {TransportID: "t", Purpose: "download"},
		"no transport":    {Purpose: ResolvePurposeHLS},
	} {
		if _, err := client.Resolve(t.Context(), req); Status(err) != http.StatusBadRequest {
			t.Fatalf("%s = %v, want 400", name, err)
		}
	}
}

// A node tearing its sessions down for a force reload asks the core to revoke
// its recipes (revoke-recipes). Afterwards no progressive remux starts and no
// stored HLS card rebuilds on that node. A session whose client holds a
// complete token still rebuilds on the node the token names, as a node that
// verifies tokens itself does after a force reload: the token, not the store,
// was its authority. A session moved to a node that revoked its recipes is
// refused there, because its token names the node it left.
func TestResolveAfterANodeRevokesItsRecipes(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := noderecipe.NewStore(rdb, time.Hour)
	core := newResolveCore(t, twoNodes, store)
	put := func(transportID string, card playback.RecipeCard) {
		t.Helper()
		if err := store.Put(t.Context(), transportID, card); err != nil {
			t.Fatal(err)
		}
	}

	remux := remuxClaims(nodeA, "/media/movie.mkv")
	put(remux.TranscodeTransportID, playback.RecipeCardFromClaims(&remux))
	remuxToken := signClaims(t, remux, resolveSecret)
	tokenless := nativeCard("header-auth", "header-auth-plan")
	put(tokenless.TranscodeTransportID, tokenless)
	urlBound := nativeCard("legacy", "legacy-plan")
	urlBound.RoutingWorkload, urlBound.RoutingExecutionNodeID = "", 0
	put(urlBound.TranscodeTransportID, urlBound)
	withToken := nativeCard("viewer", "viewer-plan")
	put(withToken.TranscodeTransportID, withToken)
	viewerToken := signClaims(t, withToken.ToClaims(), resolveSecret)
	movedIn := nativeCard("moved", "moved-plan")
	movedInToken := signClaims(t, movedIn.ToClaims(), resolveSecret)
	movedIn.RoutingExecutionNodeID, movedIn.TranscodeNodeURL = nodeB, nodeBURL
	put(movedIn.TranscodeTransportID, movedIn)

	for _, check := range []struct {
		node                      int
		purpose, transport, token string
	}{
		{nodeA, ResolvePurposeRemux, remux.TranscodeTransportID, remuxToken},
		{nodeA, ResolvePurposeHLS, tokenless.TranscodeTransportID, ""},
		{nodeA, ResolvePurposeHLS, urlBound.TranscodeTransportID, ""},
		{nodeA, ResolvePurposeHLS, withToken.TranscodeTransportID, viewerToken},
		{nodeB, ResolvePurposeHLS, movedIn.TranscodeTransportID, movedInToken},
	} {
		if _, status, reason := core.resolve(check.node, check.purpose, check.transport, check.token); status != http.StatusOK {
			t.Fatalf("%s before the force reload = %d %q", check.transport, status, reason)
		}
	}

	for _, node := range []int{nodeA, nodeB} {
		if err := core.client(node).RevokeRecipes(t.Context()); err != nil {
			t.Fatalf("node %d revoke-recipes: %v", node, err)
		}
	}

	_, status, reason := core.resolve(nodeA, ResolvePurposeRemux, remux.TranscodeTransportID, remuxToken)
	wantRefusal(t, status, reason, http.StatusGone, ResolveStopped)
	_, status, reason = core.resolve(nodeA, ResolvePurposeHLS, tokenless.TranscodeTransportID, "")
	wantRefusal(t, status, reason, http.StatusNotFound, ResolveNotFound)
	_, status, reason = core.resolve(nodeA, ResolvePurposeHLS, urlBound.TranscodeTransportID, "")
	wantRefusal(t, status, reason, http.StatusNotFound, ResolveNotFound)
	if _, status, reason := core.resolve(nodeA, ResolvePurposeHLS, withToken.TranscodeTransportID, viewerToken); status != http.StatusOK {
		t.Fatalf("complete token after the force reload = %d %q, want the token's recipe", status, reason)
	}
	_, status, reason = core.resolve(nodeB, ResolvePurposeHLS, movedIn.TranscodeTransportID, movedInToken)
	wantRefusal(t, status, reason, http.StatusForbidden, ResolveWrongRoute)
}

// failingRevoker is a recipe store that cannot be written.
type failingRevoker struct{ recipeMap }

func (*failingRevoker) RevokeNodeID(context.Context, int) error  { return errors.New("redis down") }
func (*failingRevoker) RevokeNode(context.Context, string) error { return errors.New("redis down") }

// A node revokes only its own recipes: the core takes the id and URL from the
// node's authenticated identity, so another node's cards, bound by id or by
// URL, survive. A store that cannot be written is 503, which the node retries.
func TestRevokeRecipesRevokesOnlyTheCallingNode(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := noderecipe.NewStore(rdb, time.Hour)
	core := newResolveCore(t, twoNodes, store)
	cards := map[string]playback.RecipeCard{}
	for _, node := range []struct {
		id  int
		url string
	}{{nodeA, nodeAURL}, {nodeB, nodeBURL}} {
		byID := nativeCard("by-id", "")
		byID.TranscodeTransportID = "by-id-" + node.url
		byID.RoutingExecutionNodeID, byID.TranscodeNodeURL = node.id, node.url
		byURL := byID
		byURL.TranscodeTransportID = "by-url-" + node.url
		byURL.RoutingWorkload, byURL.RoutingExecutionNodeID = "", 0
		for _, card := range []playback.RecipeCard{byID, byURL} {
			if err := store.Put(t.Context(), card.TranscodeTransportID, card); err != nil {
				t.Fatal(err)
			}
			cards[card.TranscodeTransportID] = card
		}
	}

	if err := core.client(nodeA).RevokeRecipes(t.Context()); err != nil {
		t.Fatal(err)
	}
	for transport, card := range cards {
		_, kept := store.Get(t.Context(), transport)
		if want := card.TranscodeNodeURL != nodeAURL; kept != want {
			t.Fatalf("card %s kept = %v after node A revoked, want %v", transport, kept, want)
		}
	}

	failing := newResolveCore(t, twoNodes, &failingRevoker{})
	if err := failing.client(nodeA).RevokeRecipes(t.Context()); Status(err) != http.StatusServiceUnavailable {
		t.Fatalf("revoke with an unwritable store = %v, want 503", err)
	}
}
