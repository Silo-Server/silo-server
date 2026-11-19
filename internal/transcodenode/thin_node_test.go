package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// This file runs a transcode node against the core's real node control routes
// in process: a real Server wired through nodecontrol.Client and
// nodeconfig.Remote to a real nodecontrol.Server over HTTP, with the core's
// Redis on miniredis and the catalog faked. FFmpeg is a script.

const (
	thinNodeID       = 7
	thinOtherNodeID  = 8
	thinStreamSecret = "core-stream-secret"
	thinMediaFileID  = 42
)

// thinCoreNodes is the core's stream_nodes rows and keys.
type thinCoreNodes struct {
	mu   sync.Mutex
	rows map[int]*nodepool.Node
	keys map[int]string
}

func (n *thinCoreNodes) GetByID(_ context.Context, id int) (*nodepool.Node, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	row, ok := n.rows[id]
	if !ok {
		return nil, nodepool.ErrNodeNotFound
	}
	copied := *row
	return &copied, nil
}

func (n *thinCoreNodes) Identity(id int) (nodepool.Identity, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	row, ok := n.rows[id]
	if !ok {
		return nodepool.Identity{}, false
	}
	return nodepool.Identity{ID: row.ID, Name: row.Name, Type: row.Type, URL: row.URL}, true
}

func (n *thinCoreNodes) NodeKey(id int) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if key := n.keys[id]; key != "" {
		return key, nil
	}
	return "", nodepool.ErrNoNodeKey
}

func (n *thinCoreNodes) ReloadNodeKey(id int) (string, error) { return n.NodeKey(id) }

func (n *thinCoreNodes) setKey(id int, key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.keys[id] = key
}

// thinCatalog is the core's media_files catalog: file ids at their paths.
type thinCatalog struct {
	mu    sync.Mutex
	files map[int]string
}

func (c *thinCatalog) IsActiveFile(_ context.Context, id int, path string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.files[id] == path, nil
}

func (c *thinCatalog) IsActiveTheme(context.Context, int64, string) (bool, error) { return false, nil }

func (c *thinCatalog) remove(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.files, id)
}

// thinRevoker revokes through the core's recipe store, or fails with err.
type thinRevoker struct {
	*noderecipe.Store
	mu  sync.Mutex
	err error
}

func (r *thinRevoker) RevokeNodeID(ctx context.Context, nodeID int) error {
	r.mu.Lock()
	err := r.err
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return r.Store.RevokeNodeID(ctx, nodeID)
}

type discardLogs struct{}

func (discardLogs) InsertNodeBatch(context.Context, string, []opslog.Entry) error { return nil }

type discardBus struct{}

func (discardBus) Publish(context.Context, string, cache.Event) error          { return nil }
func (discardBus) Subscribe(context.Context, string, cache.EventHandler) error { return nil }
func (discardBus) Close() error                                                { return nil }

// thinCluster is one core and one transcode node talking to it.
type thinCluster struct {
	t         *testing.T
	redis     *redis.Client
	nodes     *thinCoreNodes
	catalog   *thinCatalog
	settings  map[string]string
	settingMu sync.Mutex
	recipes   *noderecipe.Store
	revoker   *thinRevoker
	deny      *playback.StreamDeny
	grants    *nodesource.Grants
	nodeKey   string
	media     string
	// throttled makes the core answer every node call 429, as its per-node
	// budget does; throttledCalls counts those answers.
	throttled      atomic.Bool
	throttledCalls atomic.Int64

	remote   *nodeconfig.Remote
	client   *nodecontrol.Client
	opener   *nodesource.Opener
	tracker  *nodesessions.LocalTracker
	reporter *nodecontrol.Reporter
	node     *Server
	nodeHTTP *httptest.Server
}

// thinClusterOptions vary the cluster newThinClusterWith builds.
type thinClusterOptions struct {
	// media is the library file the catalog lists; a few bytes by default.
	media string
	// ffmpeg is the node's FFmpeg; a script by default.
	ffmpeg string
	// sourceAccess is the node row's source access; local by default.
	sourceAccess string
	// wrapSources wraps the core's source bytes route.
	wrapSources func(http.Handler) http.Handler
}

func newThinCluster(t *testing.T) *thinCluster {
	return newThinClusterWith(t, thinClusterOptions{})
}

//nolint:funlen // Wires a core and a node as production does, in one place.
func newThinClusterWith(t *testing.T, opts thinClusterOptions) *thinCluster {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	dir := t.TempDir()
	media := opts.media
	if media == "" {
		media = filepath.Join(dir, "movie.mkv")
		if err := os.WriteFile(media, []byte("source bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ffmpeg := opts.ffmpeg
	if ffmpeg == "" {
		ffmpeg = filepath.Join(dir, "ffmpeg")
		// A remux writes to pipe:1, an HLS transcode to a playlist; probes exit.
		script := "#!/bin/sh\ncase \" $* \" in\n*' pipe:1 '*) printf node-remux-bytes ;;\n*.m3u8*) while :; do sleep 0.1; done ;;\nesac\n"
		if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sourceAccess := opts.sourceAccess
	if sourceAccess == "" {
		sourceAccess = nodepool.SourceAccessLocal
	}
	wrapSources := opts.wrapSources
	if wrapSources == nil {
		wrapSources = func(next http.Handler) http.Handler { return next }
	}
	nodeKey, err := nodepool.GenerateNodeKey(thinNodeID)
	if err != nil {
		t.Fatal(err)
	}
	recipes := noderecipe.NewStore(rdb, 0)
	c := &thinCluster{
		t: t, redis: rdb, nodeKey: nodeKey, media: media, recipes: recipes,
		nodes: &thinCoreNodes{
			rows: map[int]*nodepool.Node{thinNodeID: {ID: thinNodeID, Name: "gpu-7", Type: nodepool.NodeTypeTranscode, URL: "http://gpu-7:8080", Enabled: true, SourceAccess: sourceAccess}},
			keys: map[int]string{thinNodeID: nodeKey},
		},
		catalog: &thinCatalog{files: map[int]string{thinMediaFileID: media}},
		settings: map[string]string{
			"playback.ffmpeg_path":   ffmpeg,
			"playback.hw_accel":      playback.HWAccelNone,
			"playback.transcode_dir": filepath.Join(dir, "transcode"),
			"download.artifact_dir":  filepath.Join(dir, "artifacts"),
			"auth.jwt_secret":        thinStreamSecret,
		},
		revoker: &thinRevoker{Store: recipes},
		deny:    playback.NewStreamDeny(rdb),
		grants:  nodesource.NewGrants(func() string { return thinStreamSecret }),
	}
	core := nodecontrol.NewServer(nodecontrol.Options{
		Nodes: c.nodes, Identities: c.nodes, Redis: rdb, Logs: discardLogs{}, Events: discardBus{},
		Settings: func(context.Context) (map[string]string, error) {
			c.settingMu.Lock()
			defer c.settingMu.Unlock()
			return maps.Clone(c.settings), nil
		},
		StreamSecret: func() string { return thinStreamSecret },
		Recipes:      recipes, Deny: c.deny, Revocations: c.revoker,
		Grants: c.grants, Files: c.catalog, Themes: c.catalog,
	})
	// The budgets as internal/api's router applies them.
	mux := http.NewServeMux()
	mux.Handle("GET "+nodecontrol.PathConfig, core.LimitNode(http.HandlerFunc(core.HandleConfig)))
	mux.Handle("POST "+nodecontrol.PathReport, core.LimitNode(http.HandlerFunc(core.HandleReport)))
	mux.Handle("POST "+nodecontrol.PathResolve, core.LimitNodePlayback(http.HandlerFunc(core.HandleResolve)))
	mux.Handle("POST "+nodecontrol.PathSourcesOpen, core.LimitNodePlayback(http.HandlerFunc(core.HandleSourceOpen)))
	mux.Handle("POST "+nodecontrol.PathRevokeRecipes, core.LimitNode(http.HandlerFunc(core.HandleRevokeRecipes)))
	mux.Handle("GET "+nodecontrol.PathSources, wrapSources(http.HandlerFunc(core.HandleSource)))
	throttle := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c.throttled.Load() {
				c.throttledCalls.Add(1)
				w.Header().Set("Retry-After", "1")
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	coreHTTP := httptest.NewServer(nodecontrol.RequireNode(c.nodes)(throttle(mux)))
	t.Cleanup(coreHTTP.Close)

	// The node, wired as runTranscodeNode wires it.
	c.client, err = nodecontrol.NewClient(coreHTTP.URL, nodeKey)
	if err != nil {
		t.Fatal(err)
	}
	c.remote = nodeconfig.NewRemote(c.client, ":0")
	c.client.SetOnUnauthorized(c.remote.ReportUnauthorized)
	if err := c.remote.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.opener = nodesource.NewOpener(c.client.OpenSource, slog.New(slog.DiscardHandler))
	c.opener.EnableCoreAccess(nodesource.CoreAccess{Fetch: c.client.ReadSource, Mode: c.remote.SourceAccess})
	c.opener.SetRenewInterval(20 * time.Millisecond)
	c.tracker = nodesessions.NewLocalTracker()
	c.node = NewServer(c.remote, c.tracker)
	c.node.SetNodeKey(nodeKey)
	c.node.SetCore(c.client)
	c.node.SetSourceOpener(c.opener)
	c.node.SetKeyRejected(c.remote.KeyRejected)
	c.reporter = nodecontrol.NewReporter(c.client, c.tracker.Snapshot, nil)
	c.nodeHTTP = httptest.NewServer(c.node.Handler())
	t.Cleanup(func() {
		c.nodeHTTP.Close()
		_ = c.node.Shutdown(context.Background())
		_ = c.opener.Close()
	})
	return c
}

// setSetting changes a server setting the node's configuration document
// carries.
func (c *thinCluster) setSetting(key, value string) {
	c.settingMu.Lock()
	defer c.settingMu.Unlock()
	c.settings[key] = value
}

// call sends a request to the node signed as the core.
func (c *thinCluster) call(method, path string, body any, header http.Header) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.nodeHTTP.URL+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	maps.Copy(req.Header, header)
	nodeauthtest.Sign(c.t, req, c.nodeKey)
	resp, err := c.nodeHTTP.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// grant is the core's grant for the media file, naming node nodeID.
func (c *thinCluster) grant(nodeID int) string {
	c.t.Helper()
	grant, err := c.grants.ForFile(c.t.Context(), nodeID, &models.MediaFile{ID: thinMediaFileID, FilePath: c.media})
	if err != nil {
		c.t.Fatal(err)
	}
	return grant
}

// storeRecipe stores the HLS recipe the core starts transportID with, bound
// to node boundTo.
func (c *thinCluster) storeRecipe(transportID string, boundTo int) {
	c.t.Helper()
	card := playback.NewRecipeCard(3, "profile-3", thinMediaFileID, "http://gpu-7:8080", playback.TranscodeOpts{
		InputPath: c.media, SessionID: "viewer-" + transportID, TranscodeTransportID: transportID,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
	})
	card.RoutingExecutionNodeID = boundTo
	if err := c.recipes.Put(c.t.Context(), transportID, card); err != nil {
		c.t.Fatal(err)
	}
}

// rebuild has the node rebuild transportID as a request for its lost session
// would, without waiting on a playlist the stand-in FFmpeg never writes.
func (c *thinCluster) rebuild(transportID string) {
	c.t.Helper()
	request := httptest.NewRequestWithContext(c.t.Context(), http.MethodGet, "/transcode/"+transportID+"/master.m3u8", nil)
	session, err := c.node.reconstructLost(request, transportID, -1)
	if err != nil || session == nil || !c.node.hostsSession(transportID) {
		c.t.Fatalf("lost session %s not rebuilt: %v, %v", transportID, session, err)
	}
}

func (c *thinCluster) health() HealthResponse {
	c.t.Helper()
	status, body := c.call(http.MethodGet, "/api/v1/health", nil, nil)
	var health HealthResponse
	if status != http.StatusOK || json.Unmarshal(body, &health) != nil {
		c.t.Fatalf("health = %d %s", status, body)
	}
	return health
}

// waitFor waits for cond, which observes state another goroutine changes.
func (c *thinCluster) waitFor(what string, cond func() bool) {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			c.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // polls the session map, the observable state a withdrawn source changes
	}
}

func TestThinNodeAgainstTheCore(t *testing.T) {
	c := newThinCluster(t)
	for _, step := range []struct {
		name string
		run  func(*testing.T, *thinCluster)
	}{
		{"health names the control protocol", testThinNodeHealth},
		{"start opens its grant and the core's Redis lists the session after a report", testThinNodeStart},
		{"a lost session rebuilds through resolve unless refused", testThinNodeRebuild},
		{"a progressive remux resolves and reads its grant", testThinNodeRemux},
		{"a source the core withdraws stops its session", testThinNodeWithdrawnSource},
		{"a force reload revokes through the core first", testThinNodeForceReload},
		{"a rejected key shows in health until a read succeeds", testThinNodeRejectedKey},
		{"a throttled core is a transient failure on every route", testThinNodeThrottledCore},
		{"the configuration pointer changes only with the configuration", testThinNodeConfigPointer},
	} {
		t.Run(step.name, func(t *testing.T) { step.run(t, c) })
	}
}

func testThinNodeHealth(t *testing.T, c *thinCluster) {
	if health := c.health(); health.NodeControlVersion != nodepool.NodeControlVersion || health.KeyRejected {
		t.Fatalf("health = %+v", health)
	}
}

func testThinNodeStart(t *testing.T, c *thinCluster) {
	ctx := t.Context()
	start := TranscodeStartRequest{
		SessionID: "started-transport", PlaybackSessionID: "viewer-started", UserID: 3, ProfileID: "profile-3", MediaFileID: thinMediaFileID,
		InputPath: "/a/path/the/node/must/not/use.mkv", SourceGrant: c.grant(thinNodeID),
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
	}
	if status, body := c.call(http.MethodPost, "/transcode/start", start, nil); status != http.StatusAccepted {
		t.Fatalf("start = %d %s", status, body)
	}
	c.node.mu.RLock()
	session := c.node.sessions["started-transport"]
	c.node.mu.RUnlock()
	if session == nil || session.Opts().InputPath != c.media {
		t.Fatalf("session = %v, want one reading the granted file %q", session, c.media)
	}
	if err := c.reporter.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	listed, err := nodesessions.ListAll(ctx, c.redis, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != "started-transport" || listed.Sessions[0].NodeName != "gpu-7" ||
		listed.Sessions[0].AuthUserID != 3 || listed.Sessions[0].MediaFileID != thinMediaFileID {
		t.Fatalf("core lists %+v, want the node's session under its registered name and playback identity", listed.Sessions)
	}

	otherNode := start
	otherNode.SessionID, otherNode.SourceGrant = "other-node-grant", c.grant(thinOtherNodeID)
	if status, _ := c.call(http.MethodPost, "/transcode/start", otherNode, nil); status != http.StatusBadRequest {
		t.Fatalf("start with another node's grant = %d, want 400", status)
	}
}

func testThinNodeRebuild(t *testing.T, c *thinCluster) {
	ctx := t.Context()
	manifest := func(transportID string) int {
		status, _ := c.call(http.MethodGet, "/transcode/"+transportID+"/master.m3u8", nil, nil)
		return status
	}
	c.storeRecipe("lost-transport", thinNodeID)
	c.rebuild("lost-transport")
	if got := c.node.playbackSessionID("lost-transport"); got != "viewer-lost-transport" {
		t.Fatalf("rebuilt session files telemetry under %q", got)
	}

	c.storeRecipe("denied-transport", thinNodeID)
	c.deny.Deny(ctx, "viewer-denied-transport")
	if status := manifest("denied-transport"); status != http.StatusGone || c.node.hostsSession("denied-transport") {
		t.Fatalf("denied session = %d, want 410 and nothing rebuilt", status)
	}

	c.storeRecipe("elsewhere-transport", thinOtherNodeID)
	if status := manifest("elsewhere-transport"); status != http.StatusNotFound || c.node.hostsSession("elsewhere-transport") {
		t.Fatalf("session bound to another node = %d, want 404 and nothing rebuilt", status)
	}

	if status, _ := c.call(http.MethodDelete, "/transcode/lost-transport", nil, nil); status != http.StatusNoContent {
		t.Fatalf("stop = %d", status)
	}
	if status := manifest("lost-transport"); status != http.StatusGone || c.node.hostsSession("lost-transport") {
		t.Fatalf("stopped session = %d, want 410 from the node's own fence", status)
	}
}

func testThinNodeRemux(t *testing.T, c *thinCluster) {
	ctx := t.Context()
	claims := streamtoken.Claims{
		SessionID: "viewer-remux", UserID: 3, ProfileID: "profile-3", MediaFileID: thinMediaFileID, MediaPath: c.media,
		PlayMethod: string(playback.PlayRemux), TranscodeNode: "http://gpu-7:8080", TranscodeTransportID: "remux-transport",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: thinNodeID, RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeID: 2,
	}
	token, err := streamtoken.Sign(claims, thinStreamSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.recipes.Put(ctx, "remux-transport", playback.RecipeCardFromClaims(&claims)); err != nil {
		t.Fatal(err)
	}
	header := http.Header{streamTokenHeader: {token}}
	if status, body := c.call(http.MethodGet, "/remux/remux-transport", nil, header); status != http.StatusOK || string(body) != "node-remux-bytes" {
		t.Fatalf("remux = %d %q", status, body)
	}
	if status, _ := c.call(http.MethodGet, "/remux/remux-transport", nil, http.Header{streamTokenHeader: {"forged"}}); status != http.StatusUnauthorized {
		t.Fatalf("remux with a forged token = %d, want 401", status)
	}
}

func testThinNodeWithdrawnSource(t *testing.T, c *thinCluster) {
	start := TranscodeStartRequest{
		SessionID: "withdrawn-transport", SourceGrant: c.grant(thinNodeID),
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
	}
	if status, body := c.call(http.MethodPost, "/transcode/start", start, nil); status != http.StatusAccepted {
		t.Fatalf("start = %d %s", status, body)
	}
	c.catalog.remove(thinMediaFileID)
	c.waitFor("the withdrawn session to end", func() bool { return !c.node.hostsSession("withdrawn-transport") })
	c.catalog.mu.Lock()
	c.catalog.files[thinMediaFileID] = c.media
	c.catalog.mu.Unlock()
}

func testThinNodeForceReload(t *testing.T, c *thinCluster) {
	ctx := t.Context()
	c.storeRecipe("reload-transport", thinNodeID)
	c.rebuild("reload-transport")

	c.revoker.mu.Lock()
	c.revoker.err = errors.New("redis unavailable")
	c.revoker.mu.Unlock()
	if status, _ := c.call(http.MethodPost, "/admin/force-reload", nil, nil); status != http.StatusInternalServerError {
		t.Fatalf("force reload with a failed revocation = %d, want 500", status)
	}
	c.revoker.mu.Lock()
	c.revoker.err = nil
	c.revoker.mu.Unlock()

	if status, _ := c.call(http.MethodPost, "/admin/force-reload", nil, nil); status != http.StatusNoContent {
		t.Fatalf("force reload = %d", status)
	}
	if card, ok, err := c.recipes.Lookup(ctx, "reload-transport"); err != nil || ok {
		t.Fatalf("recipe after force reload = %v, %v, %v; want revoked", card, ok, err)
	}
}

func testThinNodeRejectedKey(t *testing.T, c *thinCluster) {
	ctx := t.Context()
	rotated, err := nodepool.GenerateNodeKey(thinNodeID)
	if err != nil {
		t.Fatal(err)
	}
	c.nodes.setKey(thinNodeID, rotated)
	if err := c.remote.ForceReload(ctx); nodecontrol.Status(err) != http.StatusUnauthorized {
		t.Fatalf("read with a rotated key = %v, want 401", err)
	}
	if !c.health().KeyRejected {
		t.Fatal("health does not report the rejected key")
	}
	c.nodes.setKey(thinNodeID, c.nodeKey)
	if err := c.remote.ForceReload(ctx); err != nil {
		t.Fatal(err)
	}
	if c.health().KeyRejected {
		t.Fatal("a read the core answered did not clear key_rejected")
	}
}

func testThinNodeConfigPointer(t *testing.T, c *thinCluster) {
	ctx := t.Context()
	before := c.remote.Config()
	if err := c.remote.ForceReload(ctx); err != nil || c.remote.Config() != before {
		t.Fatalf("an unchanged read replaced the configuration (err %v)", err)
	}
	c.setSetting("playback.hw_device", "/dev/dri/renderD129")
	if err := c.remote.ForceReload(ctx); err != nil {
		t.Fatal(err)
	}
	if after := c.remote.Config(); after == before || after.Playback.HWDevice != "/dev/dri/renderD129" {
		t.Fatalf("a changed setting did not reach the node: %q", after.Playback.HWDevice)
	}
}

// The core's per-node budgets answer 429. On every route the node calls that
// is a failure a retry heals: the relay gets a retryable 503, a running
// source keeps renewing, a configuration read keeps the configuration, and
// nothing reads as a rejected key.
func testThinNodeThrottledCore(t *testing.T, c *thinCluster) {
	ctx := t.Context()
	start := TranscodeStartRequest{
		SessionID: "throttled-renewal", SourceGrant: c.grant(thinNodeID),
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
	}
	if status, body := c.call(http.MethodPost, "/transcode/start", start, nil); status != http.StatusAccepted {
		t.Fatalf("start = %d %s", status, body)
	}
	c.storeRecipe("throttled-rebuild", thinNodeID)
	claims := streamtoken.Claims{
		SessionID: "viewer-throttled-remux", UserID: 3, ProfileID: "profile-3", MediaFileID: thinMediaFileID, MediaPath: c.media,
		PlayMethod: string(playback.PlayRemux), TranscodeNode: "http://gpu-7:8080", TranscodeTransportID: "throttled-remux",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: thinNodeID, RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeID: 2,
	}
	token, err := streamtoken.Sign(claims, thinStreamSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.recipes.Put(ctx, "throttled-remux", playback.RecipeCardFromClaims(&claims)); err != nil {
		t.Fatal(err)
	}
	before := c.remote.Config()

	c.throttled.Store(true)
	defer c.throttled.Store(false)

	// resolve, on a progressive remux and on a lost session's rebuild.
	if status, _ := c.call(http.MethodGet, "/remux/throttled-remux", nil, http.Header{streamTokenHeader: {token}}); status != http.StatusServiceUnavailable {
		t.Fatalf("remux while throttled = %d, want 503", status)
	}
	if status, _ := c.call(http.MethodGet, "/transcode/throttled-rebuild/master.m3u8", nil, nil); status != http.StatusServiceUnavailable ||
		c.node.hostsSession("throttled-rebuild") {
		t.Fatalf("rebuild while throttled = %d, want 503 and nothing rebuilt", status)
	}
	// sources/open, on a job start and on renewal.
	next := start
	next.SessionID, next.SourceGrant = "throttled-start", c.grant(thinNodeID)
	if status, _ := c.call(http.MethodPost, "/transcode/start", next, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("start while throttled = %d, want 503", status)
	}
	answered := c.throttledCalls.Load()
	c.waitFor("several throttled renewals", func() bool { return c.throttledCalls.Load() >= answered+3 })
	if !c.node.hostsSession("throttled-renewal") {
		t.Fatal("a throttled renewal ended the running session")
	}
	// sources/open, on a chapter extraction: the core extracts locally.
	chapter := chapterthumbs.RemoteExtractRequest{SourceGrant: c.grant(thinNodeID)}
	if status, body := c.call(http.MethodPost, "/chapter-thumbnails/extract", chapter, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("chapter extraction while throttled = %d %s, want 503", status, body)
	} else {
		assertChapterFallsBackToLocal(t, body)
	}
	// config.
	if err := c.remote.ForceReload(ctx); nodecontrol.Status(err) != http.StatusTooManyRequests {
		t.Fatalf("read while throttled = %v, want 429", err)
	}
	if c.remote.Config() != before || c.remote.KeyRejected() || c.health().KeyRejected {
		t.Fatal("a throttled read changed the configuration or reported the key rejected")
	}

	c.throttled.Store(false)
	if status, body := c.call(http.MethodGet, "/remux/throttled-remux", nil, http.Header{streamTokenHeader: {token}}); status != http.StatusOK || string(body) != "node-remux-bytes" {
		t.Fatalf("remux after the budget refilled = %d %q", status, body)
	}
	if status, _ := c.call(http.MethodDelete, "/transcode/throttled-renewal", nil, nil); status != http.StatusNoContent {
		t.Fatalf("stop = %d", status)
	}
}
