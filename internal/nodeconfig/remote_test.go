package nodeconfig

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
)

// fakeCore answers FetchConfig like the core: the current document, or 304
// when the caller names its revision, or the error set on it.
type fakeCore struct {
	mu       sync.Mutex
	revision int
	doc      nodecontrol.NodeConfig
	err      error
	etags    []string
	served   chan struct{}
}

func newFakeCore(body nodecontrol.NodeConfigBody) *fakeCore {
	core := &fakeCore{served: make(chan struct{}, 16)}
	core.set(body)
	return core
}

func (c *fakeCore) set(body nodecontrol.NodeConfigBody) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The revision only has to change when the body does.
	c.revision++
	c.doc = nodecontrol.NodeConfig{Revision: "rev-" + strconv.Itoa(c.revision), NodeConfigBody: body}
}

func (c *fakeCore) fail(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

func (c *fakeCore) FetchConfig(_ context.Context, etag string) (*nodecontrol.NodeConfig, bool, error) {
	c.mu.Lock()
	defer func() {
		c.mu.Unlock()
		select {
		case c.served <- struct{}{}:
		default:
		}
	}()
	c.etags = append(c.etags, etag)
	if c.err != nil {
		return nil, false, c.err
	}
	if etag == `"`+c.doc.Revision+`"` {
		return nil, true, nil
	}
	doc := c.doc
	return &doc, false, nil
}

func (c *fakeCore) lastETag() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.etags[len(c.etags)-1]
}

func remoteBody() nodecontrol.NodeConfigBody {
	return nodecontrol.NodeConfigBody{
		Node:         nodecontrol.NodeIdentity{ID: 7, Name: "gpu-1", URL: "http://gpu-1:8080"},
		SourceAccess: "local",
		Playback: nodecontrol.PlaybackConfig{FFmpegPath: "ffmpeg", HWAccel: "vaapi", HWDevice: "/dev/dri/renderD129",
			TranscodeDir: "/tmp/node-transcode", SegmentRetentionSeconds: 300},
		Download:  nodecontrol.DownloadConfig{ArtifactDir: "/srv/artifacts", ArtifactMaxBytes: 1 << 30},
		Subtitles: nodecontrol.SubtitlesConfig{SyncNodeCapacity: 3},
		Logging:   nodecontrol.LoggingConfig{Level: "debug", Format: "json", Quiet: "x", CaptureLevel: "warn"},
	}
}

func loadedRemote(t *testing.T, core *fakeCore) *Remote {
	t.Helper()
	remote := NewRemote(core, ":9090")
	remote.OnLoad(func(cfg *config.Config) { cfg.Playback.FFmpegPath = "/resolved/" + cfg.Playback.FFmpegPath })
	if err := remote.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return remote
}

func TestRemoteBuildsTheNodesConfig(t *testing.T) {
	remote := loadedRemote(t, newFakeCore(remoteBody()))
	cfg := remote.Config()
	if cfg.Server.Mode != "transcode" || cfg.Server.Listen != ":9090" || cfg.Server.LogLevel != "debug" ||
		cfg.Server.LogFormat != "json" || cfg.Server.LogQuiet != "x" {
		t.Fatalf("server config = %+v", cfg.Server)
	}
	if cfg.Playback.FFmpegPath != "/resolved/ffmpeg" || cfg.Playback.HWAccel != "vaapi" || cfg.Playback.HWDevice != "/dev/dri/renderD129" ||
		cfg.Playback.TranscodeDir != "/tmp/node-transcode" || cfg.Playback.SegmentRetentionSeconds != 300 || cfg.Playback.SubtitleSyncNodeCapacity != 3 {
		t.Fatalf("playback config = %+v", cfg.Playback)
	}
	if cfg.Download.ArtifactDir != "/srv/artifacts" || cfg.Download.ArtifactMaxBytes != 1<<30 {
		t.Fatalf("download config = %+v", cfg.Download)
	}
	if cfg.Auth.JWTSecret != "" || cfg.Database.URL != "" {
		t.Fatal("a node config carries a secret or a database")
	}
	if id, ok := remote.NodeRowID(); !ok || id != 7 {
		t.Fatalf("NodeRowID = %d, %v", id, ok)
	}
	if url, ok := remote.NodeRegisteredURL(); !ok || url != "http://gpu-1:8080" {
		t.Fatalf("NodeRegisteredURL = %q, %v", url, ok)
	}
	if remote.NodeName() != "gpu-1" || remote.SourceAccess() != "local" || remote.CaptureLevel() != "warn" || remote.NodeDrainRequested() {
		t.Fatalf("identity = %q %q %q %v", remote.NodeName(), remote.SourceAccess(), remote.CaptureLevel(), remote.NodeDrainRequested())
	}
}

// Handlers refuse work when the config pointer changes under them, so an
// unchanged document, and one whose only change is not configuration, must
// keep it.
func TestRemoteSwapsThePointerOnlyWhenTheConfigChanges(t *testing.T) {
	core := newFakeCore(remoteBody())
	remote := loadedRemote(t, core)
	first := remote.Config()
	var changes int
	remote.OnChange(func(_, _ *config.Config) { changes++ })
	drains := make(chan bool, 4)
	remote.OnNodeDrainChange(func(requested bool) { drains <- requested })

	if err := remote.ForceReload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if core.lastETag() != `"`+remote.Revision()+`"` {
		t.Fatalf("poll sent If-None-Match %q, want the current revision", core.lastETag())
	}
	if remote.Config() != first {
		t.Fatal("a 304 swapped the config pointer")
	}

	drained := remoteBody()
	drained.DrainRequested = true
	drained.SourceAccess = "core"
	drained.Node.Name = "gpu-renamed"
	core.set(drained)
	if err := remote.ForceReload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if remote.Config() != first {
		t.Fatal("a drain, source access or name change swapped the config pointer")
	}
	if !remote.NodeDrainRequested() || remote.SourceAccess() != "core" || remote.NodeName() != "gpu-renamed" {
		t.Fatal("the new document's drain, source access or name was not adopted")
	}
	if got := <-drains; !got {
		t.Fatal("drain callback got false")
	}

	changed := drained
	changed.Playback.HWAccel = "nvenc"
	core.set(changed)
	if err := remote.ForceReload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if remote.Config() == first || remote.Config().Playback.HWAccel != "nvenc" {
		t.Fatal("a configuration change did not swap the pointer")
	}
	if changes != 1 {
		t.Fatalf("OnChange ran %d times, want 1", changes)
	}
}

func TestRemoteKeepsTheLastConfigWhenTheCoreFails(t *testing.T) {
	core := newFakeCore(remoteBody())
	remote := loadedRemote(t, core)
	first := remote.Config()
	core.fail(errors.New("core down"))
	if err := remote.ForceReload(context.Background()); err == nil {
		t.Fatal("ForceReload hid the failure")
	}
	if remote.Config() != first || remote.NodeName() != "gpu-1" {
		t.Fatal("a failed read dropped the last configuration")
	}
}

// /admin/reload-config asks for a read at once instead of at the next poll.
func TestRemoteRequestReloadRefetchesNow(t *testing.T) {
	core := newFakeCore(remoteBody())
	remote := loadedRemote(t, core)
	<-core.served // the Load read
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go remote.Run(ctx)
	changed := remoteBody()
	changed.Playback.HWDevice = "/dev/dri/renderD130"
	core.set(changed)
	remote.RequestReload()
	select {
	case <-core.served:
	case <-time.After(5 * time.Second):
		t.Fatal("RequestReload did not read the configuration")
	}
	deadline := time.Now().Add(5 * time.Second)
	for remote.Config().Playback.HWDevice != "/dev/dri/renderD130" {
		if time.Now().After(deadline) {
			t.Fatal("the reread document was not applied")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Startup keeps asking until the core answers.
func TestRemoteLoadRetriesUntilTheCoreAnswers(t *testing.T) {
	core := newFakeCore(remoteBody())
	core.fail(errors.New("core starting"))
	remote := NewRemote(core, ":8080")
	done := make(chan error, 1)
	go func() { done <- remote.Load(context.Background()) }()
	<-core.served
	core.fail(nil)
	select {
	case err := <-done:
		if err != nil || remote.Config() == nil {
			t.Fatalf("Load = %v, config %v", err, remote.Config())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Load did not retry")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	core.fail(errors.New("down"))
	if err := NewRemote(core, ":8080").Load(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load on a canceled context = %v", err)
	}
}

// A 401 means the core no longer accepts the node's key (rotated, or the node
// deleted). The node reports it until a read succeeds again; any other
// failure is not a key problem.
func TestRemoteRecordsARejectedKeyUntilAReadSucceeds(t *testing.T) {
	core := newFakeCore(remoteBody())
	remote := loadedRemote(t, core)
	if remote.KeyRejected() {
		t.Fatal("a loaded node reports a rejected key")
	}

	core.fail(errors.New("core down"))
	_ = remote.ForceReload(context.Background())
	if remote.KeyRejected() {
		t.Fatal("an unreachable core reads as a rejected key")
	}

	core.fail(&nodecontrol.StatusError{Path: nodecontrol.PathConfig, Status: http.StatusUnauthorized})
	if err := remote.ForceReload(context.Background()); err == nil {
		t.Fatal("ForceReload hid the 401")
	}
	if !remote.KeyRejected() {
		t.Fatal("a 401 config read did not record the rejected key")
	}
	core.fail(errors.New("core down"))
	_ = remote.ForceReload(context.Background())
	if !remote.KeyRejected() {
		t.Fatal("a failed read that proves nothing cleared the rejected key")
	}

	// The unchanged document answers 304, which the core only sends to a
	// verified node.
	core.fail(nil)
	if err := remote.ForceReload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if remote.KeyRejected() {
		t.Fatal("a successful read did not clear the rejected key")
	}
}

// A node whose key the core refuses gets 401 on every call: each report and
// each log batch. The first asks for a configuration read at once, so health
// shows the rejected key promptly; while it stays rejected, the rest ask for
// at most one read per poll interval instead of one each.
func TestRemoteReportUnauthorizedReadsAtMostOncePerPollWhileRejected(t *testing.T) {
	core := newFakeCore(remoteBody())
	remote := loadedRemote(t, core)
	now := time.Unix(1_700_000_000, 0)
	remote.now = func() time.Time { return now }
	// requested takes the read Run would make, if one was asked for.
	requested := func() bool {
		select {
		case <-remote.reloadCh:
			return true
		default:
			return false
		}
	}

	remote.ReportUnauthorized()
	if !requested() {
		t.Fatal("the first 401 after a healthy state did not ask for a read")
	}
	core.fail(&nodecontrol.StatusError{Path: nodecontrol.PathConfig, Status: http.StatusUnauthorized})
	_ = remote.ForceReload(context.Background())
	if !remote.KeyRejected() {
		t.Fatal("the read did not record the rejected key")
	}

	for range 100 {
		remote.ReportUnauthorized()
	}
	if requested() {
		t.Fatal("401s within a poll interval of the refused read asked for another read")
	}
	now = now.Add(RemotePollInterval)
	for range 100 {
		remote.ReportUnauthorized()
	}
	if !requested() {
		t.Fatal("no read was asked for a poll interval after the refused one")
	}
	for range 100 {
		remote.ReportUnauthorized()
	}
	if requested() {
		t.Fatal("more than one read was asked for in one poll interval")
	}
}
