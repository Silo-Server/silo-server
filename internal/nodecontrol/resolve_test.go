package nodecontrol_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodecontrol/nodecontroltest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

const resolveSecret = "resolve-test-secret"

// recipeMap is a recipe store keyed by transport id.
type recipeMap struct {
	mu    sync.Mutex
	cards map[string]playback.RecipeCard
	// err is a store that cannot be read.
	err error
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

// denySet is a deny marker store.
type denySet map[string]bool

func (d denySet) Denied(_ context.Context, sessionID string) bool { return d[sessionID] }

// resolveNodeURL is the registered URL of every fixture node: the URL the
// fixture's recipe cards were started on.
const resolveNodeURL = "http://node"

// nodeURLs is the stream node table, by id; err fails every read.
type nodeURLs struct {
	urls map[int]string
	err  error
}

func (n nodeURLs) GetByID(_ context.Context, id int) (*nodepool.Node, error) {
	identity, err := n.GetIdentity(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return &nodepool.Node{ID: identity.ID, URL: identity.URL}, nil
}

func (n nodeURLs) GetIdentity(_ context.Context, id int) (nodepool.Identity, error) {
	if n.err != nil {
		return nodepool.Identity{}, n.err
	}
	url, ok := n.urls[id]
	if !ok {
		return nodepool.Identity{}, nodepool.ErrNodeNotFound
	}
	return nodepool.Identity{ID: id, Type: "transcode", URL: url}, nil
}

type resolveFixture struct {
	recipes *recipeMap
	deny    denySet
	client  *nodecontrol.Client
}

func newResolveFixture(t *testing.T, nodeID int) *resolveFixture {
	t.Helper()
	return newResolveFixtureWithNodes(t, nodeID, nodeURLs{urls: map[int]string{nodeID: resolveNodeURL}})
}

func newResolveFixtureWithNodes(t *testing.T, nodeID int, nodes nodecontrol.NodeDirectory) *resolveFixture {
	t.Helper()
	f := &resolveFixture{recipes: &recipeMap{cards: map[string]playback.RecipeCard{}}, deny: denySet{}}
	f.client = nodecontroltest.Resolver(t, nodeID, nodecontrol.Options{
		Nodes:        nodes,
		StreamSecret: func() string { return resolveSecret },
		Recipes:      f.recipes,
		Deny:         f.deny,
	})
	return f
}

func (f *resolveFixture) resolve(t *testing.T, purpose, transportID, token string) (*nodecontrol.Resolution, int, string) {
	t.Helper()
	resolution, err := f.client.Resolve(t.Context(), nodecontrol.ResolveRequest{TransportID: transportID, Purpose: purpose, StreamToken: token})
	if err != nil {
		status := nodecontrol.Status(err)
		if status == 0 {
			t.Fatalf("resolve: %v", err)
		}
		return nil, status, nodecontrol.Reason(err)
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

// nativeCard is a complete native transcode recipe for a plan-scoped
// transport of playback session sessionID.
func nativeCard(sessionID, transportID string) playback.RecipeCard {
	card := playback.NewRecipeCard(7, "profile-1", 42, resolveNodeURL, playback.TranscodeOpts{
		SessionID: sessionID, InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 6,
	})
	card.TranscodeTransportID = transportID
	return card
}

func wantRefusal(t *testing.T, gotStatus int, gotReason string, status int, reason string) {
	t.Helper()
	if gotStatus != status || gotReason != reason {
		t.Fatalf("resolve = %d %q, want %d %q", gotStatus, gotReason, status, reason)
	}
}

func TestResolveHLSFromACompleteToken(t *testing.T) {
	f := newResolveFixture(t, 3)
	card := nativeCard("viewer-1", "viewer-1-plan")
	resolution, status, reason := f.resolve(t, nodecontrol.ResolvePurposeHLS, "viewer-1-plan", signClaims(t, card.ToClaims(), resolveSecret))
	if status != http.StatusOK {
		t.Fatalf("resolve = %d %q", status, reason)
	}
	if resolution.Recipe.TargetCodecVideo != "h264" || resolution.Recipe.SegmentDuration != 6 || resolution.Recipe.InputPath != "/media/movie.mkv" {
		t.Fatalf("recipe = %+v", resolution.Recipe)
	}
	if resolution.PlaybackSessionID != "viewer-1" || resolution.UserID != 7 || resolution.ProfileID != "profile-1" || resolution.MediaFileID != 42 {
		t.Fatalf("identity = %+v", resolution)
	}
}

// The jellycompat node-hop token is identity-only and a header-authenticated
// attempt forwards none: both resolve from the stored recipe.
func TestResolveHLSFallsBackToTheStoredRecipe(t *testing.T) {
	f := newResolveFixture(t, 3)
	stored := nativeCard("compat-1", "")
	stored.TargetCodecVideo = "hevc"
	f.recipes.cards["compat-1"] = stored
	identityOnly := signClaims(t, playback.RecipeCard{SessionID: "compat-1", UserID: 7, PlayMethod: playback.PlayTranscode, InputPath: "/media/movie.mkv"}.ToClaims(), resolveSecret)
	for name, token := range map[string]string{"identity-only token": identityOnly, "no token": ""} {
		resolution, status, reason := f.resolve(t, nodecontrol.ResolvePurposeHLS, "compat-1", token)
		if status != http.StatusOK {
			t.Fatalf("%s: resolve = %d %q", name, status, reason)
		}
		if resolution.Recipe.TargetCodecVideo != "hevc" || resolution.PlaybackSessionID != "compat-1" || resolution.UserID != 7 || resolution.MediaFileID != 42 {
			t.Fatalf("%s: resolution = %+v", name, resolution)
		}
	}
}

// A node that rebuilds a lost session files it under the playback session's
// own start, not the rebuild's: the token's original start, else the stored
// recipe's, else the token's issue time.
func TestResolveHLSCarriesTheSessionStart(t *testing.T) {
	f := newResolveFixture(t, 3)
	original := time.Unix(1_700_000_000, 0).UTC()
	withStart := nativeCard("viewer-1", "viewer-1-plan")
	withStart.OriginalStartedAt = original
	resolution, status, reason := f.resolve(t, nodecontrol.ResolvePurposeHLS, "viewer-1-plan", signClaims(t, withStart.ToClaims(), resolveSecret))
	if status != http.StatusOK {
		t.Fatalf("token with an original start: resolve = %d %q", status, reason)
	}
	if !resolution.StartedAt.Equal(original) || resolution.StartedAtSource != string(streamtoken.StartedAtSourceClaim) {
		t.Fatalf("token with an original start: started at = %v %q", resolution.StartedAt, resolution.StartedAtSource)
	}

	stored := nativeCard("compat-1", "")
	stored.OriginalStartedAt = original
	f.recipes.cards["compat-1"] = stored
	resolution, status, reason = f.resolve(t, nodecontrol.ResolvePurposeHLS, "compat-1", "")
	if status != http.StatusOK {
		t.Fatalf("stored recipe: resolve = %d %q", status, reason)
	}
	if !resolution.StartedAt.Equal(original) || resolution.StartedAtSource != string(streamtoken.StartedAtSourceClaim) {
		t.Fatalf("stored recipe: started at = %v %q", resolution.StartedAt, resolution.StartedAtSource)
	}

	before := time.Now().Add(-time.Second)
	issued := nativeCard("viewer-2", "viewer-2-plan")
	resolution, status, reason = f.resolve(t, nodecontrol.ResolvePurposeHLS, "viewer-2-plan", signClaims(t, issued.ToClaims(), resolveSecret))
	if status != http.StatusOK {
		t.Fatalf("token without an original start: resolve = %d %q", status, reason)
	}
	if resolution.StartedAt.Before(before) || resolution.StartedAtSource != string(streamtoken.StartedAtSourceIssuedAt) {
		t.Fatalf("token without an original start: started at = %v %q, want its issue time", resolution.StartedAt, resolution.StartedAtSource)
	}
}

func TestResolveHLSRefusals(t *testing.T) {
	f := newResolveFixture(t, 3)
	f.recipes.cards["incomplete"] = playback.RecipeCard{SessionID: "incomplete", PlayMethod: playback.PlayTranscode}
	f.recipes.cards["keyed-wrong"] = nativeCard("viewer-x", "another-transport")
	legacyCopy := playback.NewRecipeCard(7, "profile-1", 42, "", playback.TranscodeOpts{SessionID: "legacy-copy", TargetCodecVideo: "copy", SegmentDuration: 2})
	legacyCopy.PlayMethod, legacyCopy.CopyFMP4RecipeVersion = playback.PlayTranscode, ""
	f.recipes.cards["legacy-copy"] = legacyCopy
	f.deny["denied-transport"] = true
	f.deny["denied-viewer"] = true
	f.recipes.cards["stored-denied"] = nativeCard("denied-viewer", "stored-denied")

	for _, test := range []struct {
		name, transport, token string
		status                 int
		reason                 string
	}{
		{"store miss", "unknown", "", http.StatusNotFound, nodecontrol.ResolveNotFound},
		{"incomplete stored recipe", "incomplete", "", http.StatusNotFound, nodecontrol.ResolveNotFound},
		{"stored recipe of another transport", "keyed-wrong", "", http.StatusNotFound, nodecontrol.ResolveNotFound},
		{"unversioned copy recipe", "legacy-copy", "", http.StatusNotFound, nodecontrol.ResolveNotFound},
		{"bad signature", "t-1", signClaims(t, nativeCard("v", "t-1").ToClaims(), "wrong-secret"), http.StatusNotFound, nodecontrol.ResolveNotFound},
		{"empty-key token", "t-1", signClaims(t, nativeCard("v", "t-1").ToClaims(), ""), http.StatusNotFound, nodecontrol.ResolveNotFound},
		{"token for another transport", "t-1", signClaims(t, nativeCard("v", "t-2").ToClaims(), resolveSecret), http.StatusNotFound, nodecontrol.ResolveNotFound},
		{"direct-play token", "t-1", signClaims(t, playback.NewDirectRecipeCard("t-1", 7, "p", 42).ToClaims(), resolveSecret), http.StatusNotFound, nodecontrol.ResolveNotFound},
		{"denied transport", "denied-transport", "", http.StatusGone, nodecontrol.ResolveDenied},
		{"denied token session", "t-3", signClaims(t, nativeCard("denied-viewer", "t-3").ToClaims(), resolveSecret), http.StatusGone, nodecontrol.ResolveDenied},
		{"denied stored session", "stored-denied", "", http.StatusGone, nodecontrol.ResolveDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, status, reason := f.resolve(t, nodecontrol.ResolvePurposeHLS, test.transport, test.token)
			wantRefusal(t, status, reason, test.status, test.reason)
		})
	}
}

// Transport ids are not secrets: a node may only rebuild a transcode that was
// started on it, whether the card arrives in a token or from the store, and
// whether the planner's node id or only the dialed URL binds it.
func TestResolveHLSBindsTheAuthenticatedNode(t *testing.T) {
	const self, other = 3, 12
	f := newResolveFixtureWithNodes(t, self, nodeURLs{urls: map[int]string{self: "http://node-3:8090", other: "http://node-12:8090"}})
	bound := func(transportID string, executionNodeID int, nodeURL string) playback.RecipeCard {
		card := nativeCard("viewer-"+transportID, transportID)
		card.RoutingExecutionNodeID = executionNodeID
		card.TranscodeNodeURL = nodeURL
		return card
	}
	for _, test := range []struct {
		name   string
		card   playback.RecipeCard
		status int
		reason string
	}{
		{"own node id", bound("own-id", self, "http://node-12:8090"), http.StatusOK, ""},
		{"another node id", bound("other-id", other, "http://node-3:8090"), http.StatusForbidden, nodecontrol.ResolveWrongRoute},
		{"own URL respelled", bound("own-url", 0, " HTTP://Node-3:8090/ "), http.StatusOK, ""},
		{"another node URL", bound("other-url", 0, "http://node-12:8090"), http.StatusForbidden, nodecontrol.ResolveWrongRoute},
		{"unbound", bound("unbound", 0, ""), http.StatusForbidden, nodecontrol.ResolveWrongRoute},
	} {
		t.Run(test.name+" token", func(t *testing.T) {
			transportID := test.card.TranscodeTransportID
			_, status, reason := f.resolve(t, nodecontrol.ResolvePurposeHLS, transportID, signClaims(t, test.card.ToClaims(), resolveSecret))
			wantRefusal(t, status, reason, test.status, test.reason)
		})
		t.Run(test.name+" store", func(t *testing.T) {
			transportID := test.card.TranscodeTransportID
			f.recipes.mu.Lock()
			f.recipes.cards[transportID] = test.card
			f.recipes.mu.Unlock()
			_, status, reason := f.resolve(t, nodecontrol.ResolvePurposeHLS, transportID, "")
			wantRefusal(t, status, reason, test.status, test.reason)
		})
	}
}

// A transcode moved off an unreachable node keeps its transport id, and its
// client keeps presenting the token minted for the node it left. The core
// records the node it runs on now in the stored recipe, which resolve prefers
// over any token: the new node resolves with the old token, the node the
// transport left is refused even though the token names it, and a store that
// cannot be read is no evidence either way.
func TestResolveHLSPrefersTheStoredRecipeOfAMovedTransport(t *testing.T) {
	const left, now = 3, 12
	directory := nodeURLs{urls: map[int]string{left: "http://node-3:8090", now: "http://node-12:8090"}}
	minted := nativeCard("viewer-1", "moved-transport")
	minted.RoutingWorkload, minted.RoutingExecutionNodeID, minted.TranscodeNodeURL = string(noderouting.WorkloadVideoTranscode), left, "http://node-3:8090"
	token := signClaims(t, minted.ToClaims(), resolveSecret)
	moved := minted
	moved.RoutingExecutionNodeID, moved.TranscodeNodeURL = now, "http://node-12:8090"
	moved.TargetBitrateKbps = 4321

	newNode := newResolveFixtureWithNodes(t, now, directory)
	newNode.recipes.cards["moved-transport"] = moved
	resolution, status, reason := newNode.resolve(t, nodecontrol.ResolvePurposeHLS, "moved-transport", token)
	if status != http.StatusOK {
		t.Fatalf("new node: resolve = %d %q", status, reason)
	}
	if resolution.Recipe.RoutingExecutionNodeID != now || resolution.Recipe.TargetBitrateKbps != 4321 || resolution.PlaybackSessionID != "viewer-1" {
		t.Fatalf("new node: resolution = %+v, want the stored card", resolution)
	}

	oldNode := newResolveFixtureWithNodes(t, left, directory)
	oldNode.recipes.cards["moved-transport"] = moved
	_, status, reason = oldNode.resolve(t, nodecontrol.ResolvePurposeHLS, "moved-transport", token)
	wantRefusal(t, status, reason, http.StatusForbidden, nodecontrol.ResolveWrongRoute)

	oldNode.recipes.mu.Lock()
	oldNode.recipes.err = errors.New("redis: connection refused")
	oldNode.recipes.mu.Unlock()
	_, status, reason = oldNode.resolve(t, nodecontrol.ResolvePurposeHLS, "moved-transport", token)
	wantRefusal(t, status, reason, http.StatusServiceUnavailable, "")

	// A stored card the stop deleted leaves the token, which still binds the
	// node it was minted for and no other.
	newNode.recipes.mu.Lock()
	delete(newNode.recipes.cards, "moved-transport")
	newNode.recipes.mu.Unlock()
	_, status, reason = newNode.resolve(t, nodecontrol.ResolvePurposeHLS, "moved-transport", token)
	wantRefusal(t, status, reason, http.StatusForbidden, nodecontrol.ResolveWrongRoute)
}

// A core that cannot read the node's registered URL cannot tell whose
// URL-bound recipe it holds: the resolve is unavailable, not refused.
func TestResolveHLSUnreadableNodeIsUnavailable(t *testing.T) {
	f := newResolveFixtureWithNodes(t, 3, nodeURLs{err: errors.New("database unavailable")})
	f.recipes.cards["url-bound"] = nativeCard("viewer-1", "url-bound")
	_, status, _ := f.resolve(t, nodecontrol.ResolvePurposeHLS, "url-bound", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("resolve = %d, want 503", status)
	}
}

// remuxClaims is a progressive remux token routed to node nodeID.
func remuxClaims(nodeID int, mediaPath string) streamtoken.Claims {
	return streamtoken.Claims{
		SessionID: "viewer-remux", UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
		MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://node", TranscodeTransportID: "remux-transport",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: nodeID, RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeID: 2,
		OriginalStartedAtUnixNano: time.Unix(1_700_000_000, 0).UnixNano(),
	}
}

func (f *resolveFixture) authorize(claims streamtoken.Claims) {
	f.recipes.mu.Lock()
	defer f.recipes.mu.Unlock()
	f.recipes.cards[claims.TranscodeTransportID] = playback.RecipeCardFromClaims(&claims)
}

func TestResolveRemux(t *testing.T) {
	f := newResolveFixture(t, 11)
	claims := remuxClaims(11, "/media/movie.mkv")
	f.authorize(claims)
	resolution, status, reason := f.resolve(t, nodecontrol.ResolvePurposeRemux, "remux-transport", signClaims(t, claims, resolveSecret))
	if status != http.StatusOK {
		t.Fatalf("resolve = %d %q", status, reason)
	}
	if resolution.Recipe.InputPath != "/media/movie.mkv" || resolution.Recipe.PlayMethod != playback.PlayRemux {
		t.Fatalf("recipe = %+v", resolution.Recipe)
	}
	if resolution.PlaybackSessionID != "viewer-remux" || resolution.UserID != 7 || resolution.ProfileID != "profile-1" || resolution.MediaFileID != 42 {
		t.Fatalf("identity = %+v", resolution)
	}
	if !resolution.StartedAt.Equal(time.Unix(1_700_000_000, 0)) || resolution.StartedAtSource != string(streamtoken.StartedAtSourceClaim) {
		t.Fatalf("started at = %v %q", resolution.StartedAt, resolution.StartedAtSource)
	}
	if resolution.Theme != nil {
		t.Fatalf("theme = %+v for a video remux", resolution.Theme)
	}
}

func TestResolveRemuxRefusals(t *testing.T) {
	f := newResolveFixture(t, 11)
	valid := remuxClaims(11, "/media/movie.mkv")
	f.authorize(valid)

	otherNode := remuxClaims(12, "/media/movie.mkv")
	otherNode.TranscodeTransportID = "other-node"
	f.authorize(otherNode)
	noNode := remuxClaims(0, "/media/movie.mkv")
	noNode.TranscodeTransportID = "no-node"
	f.authorize(noNode)
	apiEgress := remuxClaims(11, "/media/movie.mkv")
	apiEgress.TranscodeTransportID, apiEgress.RoutingEgress = "api-egress", string(noderouting.EgressAPI)
	f.authorize(apiEgress)
	unauthorized := remuxClaims(11, "/media/movie.mkv")
	unauthorized.TranscodeTransportID = "never-stored"
	stale := remuxClaims(11, "/media/movie.mkv")
	stale.TranscodeTransportID = "stale"
	f.authorize(stale)
	stale.MediaPath = "/media/other.mkv"
	denied := remuxClaims(11, "/media/movie.mkv")
	denied.TranscodeTransportID, denied.SessionID = "denied-remux", "denied-viewer"
	f.authorize(denied)
	f.deny["denied-viewer"] = true
	downmix := remuxClaims(11, "/media/movie.mkv")
	downmix.TranscodeTransportID, downmix.PlayMethod = "downmix", streamtoken.PlayMethodAudioDownmixRemux
	downmix.TranscodeAudio, downmix.TargetCodecAudio, downmix.SourceAudioChannels, downmix.TargetAudioChannels = true, "aac", 6, 6
	f.authorize(downmix)

	for _, test := range []struct {
		name, transport, token string
		status                 int
		reason                 string
	}{
		{"no token", "remux-transport", "", http.StatusForbidden, nodecontrol.ResolveInvalidToken},
		{"bad signature", "remux-transport", signClaims(t, valid, "wrong-secret"), http.StatusForbidden, nodecontrol.ResolveInvalidToken},
		{"empty-key token", "remux-transport", signClaims(t, valid, ""), http.StatusForbidden, nodecontrol.ResolveInvalidToken},
		{"another transport in the URL", "not-the-token's", signClaims(t, valid, resolveSecret), http.StatusForbidden, nodecontrol.ResolveWrongRoute},
		{"another node's route", "other-node", signClaims(t, otherNode, resolveSecret), http.StatusForbidden, nodecontrol.ResolveWrongRoute},
		{"no node id", "no-node", signClaims(t, noNode, resolveSecret), http.StatusForbidden, nodecontrol.ResolveWrongRoute},
		{"API egress", "api-egress", signClaims(t, apiEgress, resolveSecret), http.StatusForbidden, nodecontrol.ResolveWrongRoute},
		{"authority never stored", "never-stored", signClaims(t, unauthorized, resolveSecret), http.StatusGone, nodecontrol.ResolveStopped},
		{"authority for another recipe", "stale", signClaims(t, stale, resolveSecret), http.StatusGone, nodecontrol.ResolveStopped},
		{"denied playback session", "denied-remux", signClaims(t, denied, resolveSecret), http.StatusGone, nodecontrol.ResolveDenied},
		{"downmix of the wrong shape", "downmix", signClaims(t, downmix, resolveSecret), http.StatusNotFound, nodecontrol.ResolveNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, status, reason := f.resolve(t, nodecontrol.ResolvePurposeRemux, test.transport, test.token)
			wantRefusal(t, status, reason, test.status, test.reason)
		})
	}
}

// A theme conversion resolves only while the theme file is the one the token
// describes, and carries the theme's identity the recipe card has no field
// for.
func TestResolveThemeConversion(t *testing.T) {
	f := newResolveFixture(t, 11)
	path := filepath.Join(t.TempDir(), "theme.mp3")
	if err := os.WriteFile(path, []byte("theme-audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	claims := remuxClaims(11, path)
	claims.TranscodeTransportID, claims.PlayMethod = "theme-transport", streamtoken.PlayMethodThemeAAC
	claims.TranscodeAudio, claims.AudioOnly, claims.TargetCodecAudio = true, true, themesongs.CodecAAC
	claims.ThemeID, claims.ThemeSize, claims.ThemeModifiedUnixNano = 5, info.Size(), info.ModTime().Truncate(time.Microsecond).UnixNano()
	f.authorize(claims)
	token := signClaims(t, claims, resolveSecret)

	resolution, status, reason := f.resolve(t, nodecontrol.ResolvePurposeRemux, "theme-transport", token)
	if status != http.StatusOK {
		t.Fatalf("resolve = %d %q", status, reason)
	}
	if resolution.Theme == nil || *resolution.Theme != (nodecontrol.ResolvedTheme{ID: 5, Size: info.Size(), ModifiedUnixNano: claims.ThemeModifiedUnixNano}) {
		t.Fatalf("theme = %+v", resolution.Theme)
	}

	if err := os.WriteFile(path, []byte("replaced theme audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, status, reason = f.resolve(t, nodecontrol.ResolvePurposeRemux, "theme-transport", token)
	wantRefusal(t, status, reason, http.StatusGone, nodecontrol.ResolveSourceChanged)

	video := claims
	video.TranscodeTransportID, video.AudioOnly = "theme-video", false
	f.authorize(video)
	_, status, reason = f.resolve(t, nodecontrol.ResolvePurposeRemux, "theme-video", signClaims(t, video, resolveSecret))
	wantRefusal(t, status, reason, http.StatusNotFound, nodecontrol.ResolveNotFound)
}

// A core without a stream secret verifies nothing, and a malformed request is
// refused before any lookup.
func TestResolveRequestGuards(t *testing.T) {
	unavailable := nodecontroltest.Resolver(t, 3, nodecontrol.Options{StreamSecret: func() string { return "" }})
	_, err := unavailable.Resolve(t.Context(), nodecontrol.ResolveRequest{TransportID: "t", Purpose: nodecontrol.ResolvePurposeRemux, StreamToken: "x"})
	if nodecontrol.Status(err) != http.StatusServiceUnavailable {
		t.Fatalf("resolve without a secret = %v, want 503", err)
	}

	f := newResolveFixture(t, 3)
	for name, req := range map[string]nodecontrol.ResolveRequest{
		"unknown purpose": {TransportID: "t", Purpose: "download"},
		"no transport":    {Purpose: nodecontrol.ResolvePurposeHLS},
	} {
		if _, err := f.client.Resolve(t.Context(), req); nodecontrol.Status(err) != http.StatusBadRequest {
			t.Fatalf("%s = %v, want 400", name, err)
		}
	}
}

// A recipe store that cannot be read (a Redis blip) is no evidence that the
// session ended: resolve answers 503, which the node retries, instead of the
// 404 or 410 that would end the stream.
func TestResolveUnreadableRecipeStoreIsUnavailable(t *testing.T) {
	f := newResolveFixture(t, 11)
	claims := remuxClaims(11, "/media/movie.mkv")
	f.authorize(claims)
	f.recipes.cards["compat-1"] = nativeCard("compat-1", "")
	f.recipes.mu.Lock()
	f.recipes.err = errors.New("redis: connection refused")
	f.recipes.mu.Unlock()

	_, status, reason := f.resolve(t, nodecontrol.ResolvePurposeHLS, "compat-1", "")
	wantRefusal(t, status, reason, http.StatusServiceUnavailable, "")
	_, status, reason = f.resolve(t, nodecontrol.ResolvePurposeRemux, "remux-transport", signClaims(t, claims, resolveSecret))
	wantRefusal(t, status, reason, http.StatusServiceUnavailable, "")
}

// identityMap is a node identity registry, by id.
type identityMap map[int]nodepool.Identity

func (m identityMap) Identity(id int) (nodepool.Identity, bool) {
	identity, ok := m[id]
	return identity, ok
}

// Resolve binds a URL-bound card to the calling node through its registered
// URL. The credential registry answers it, so a database outage does not
// stop a rebuild; a node the registry misses is still read from the table.
func TestResolveReadsTheNodeFromTheRegistryDuringADatabaseOutage(t *testing.T) {
	f := &resolveFixture{recipes: &recipeMap{cards: map[string]playback.RecipeCard{}}, deny: denySet{}}
	f.client = nodecontroltest.Resolver(t, 3, nodecontrol.Options{
		Nodes:        nodeURLs{err: errors.New("database unavailable")},
		Identities:   identityMap{3: {ID: 3, Name: "gpu-3", Type: nodepool.NodeTypeTranscode, URL: resolveNodeURL}},
		StreamSecret: func() string { return resolveSecret },
		Recipes:      f.recipes,
		Deny:         f.deny,
	})
	f.recipes.cards["url-bound"] = nativeCard("viewer-1", "url-bound")
	if _, status, reason := f.resolve(t, nodecontrol.ResolvePurposeHLS, "url-bound", ""); status != http.StatusOK {
		t.Fatalf("resolve during a database outage = %d %q, want 200 from the registry", status, reason)
	}

	missed := newResolveFixtureWithNodes(t, 3, nodeURLs{err: errors.New("database unavailable")})
	missed.recipes.cards["url-bound"] = nativeCard("viewer-1", "url-bound")
	if _, status, _ := missed.resolve(t, nodecontrol.ResolvePurposeHLS, "url-bound", ""); status != http.StatusServiceUnavailable {
		t.Fatalf("resolve without the registry = %d, want the table read's 503", status)
	}
}
