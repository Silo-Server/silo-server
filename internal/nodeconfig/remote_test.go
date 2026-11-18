package nodeconfig

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
)

// fakeCore answers FetchConfig like the core: the current document, 304 when
// the caller names its revision, or the error set on it.
type fakeCore struct {
	mu       sync.Mutex
	revision int
	doc      nodecontrol.NodeConfig
	err      error
	reads    []time.Time
}

func newFakeCore(body nodecontrol.NodeConfigBody) *fakeCore {
	core := &fakeCore{}
	core.set(body)
	return core
}

func (c *fakeCore) set(body nodecontrol.NodeConfigBody) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revision++
	c.doc = nodecontrol.NodeConfig{Revision: "rev-" + strconv.Itoa(c.revision), NodeConfigBody: body}
}

func (c *fakeCore) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

func (c *fakeCore) readTimes() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.reads...)
}

func (c *fakeCore) FetchConfig(_ context.Context, etag string) (*nodecontrol.NodeConfig, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads = append(c.reads, time.Now())
	if c.err != nil {
		return nil, false, c.err
	}
	if etag == `"`+c.doc.Revision+`"` {
		return nil, true, nil
	}
	doc := c.doc
	return &doc, false, nil
}

func remoteBody() nodecontrol.NodeConfigBody {
	return nodecontrol.NodeConfigBody{
		Node:     nodecontrol.NodeIdentity{ID: 7, Name: "gpu-1", URL: "http://gpu-1:8080"},
		Playback: nodecontrol.PlaybackConfig{FFmpegPath: "ffmpeg", HWAccel: "vaapi", TranscodeDir: "/tmp/node-transcode"},
		Logging:  nodecontrol.LoggingConfig{Level: "info", CaptureLevel: "warn"},
	}
}

var errCoreDown = errors.New("connection refused")

// A drain request changes no setting the node runs with: the config pointer
// stays, so no handler refuses work over it, and the drain callbacks run.
// A failed poll keeps everything.
func TestRemoteDrainChangeKeepsTheConfigAndRunsCallbacks(t *testing.T) {
	core := newFakeCore(remoteBody())
	remote := NewRemote(core, "")
	remote.OnLoad(func(cfg *config.Config) { cfg.Playback.FFmpegPath = "/resolved/" + cfg.Playback.FFmpegPath })
	if err := remote.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := remote.Config()
	var drains []bool
	remote.OnNodeDrainChange(func(requested bool) { drains = append(drains, requested) })

	body := remoteBody()
	body.DrainRequested = true
	core.set(body)
	if err := remote.ForceReload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if remote.Config() != before || !remote.NodeDrainRequested() || len(drains) != 1 || !drains[0] {
		t.Fatalf("after a drain request: same config %v, drain %v, callbacks %v", remote.Config() == before, remote.NodeDrainRequested(), drains)
	}
	if before.Playback.FFmpegPath != "/resolved/ffmpeg" {
		t.Fatalf("normalizer not applied: %q", before.Playback.FFmpegPath)
	}

	core.fail(errCoreDown)
	if err := remote.ForceReload(t.Context()); err == nil {
		t.Fatal("a failed read reported success")
	}
	if remote.Config() != before || !remote.NodeDrainRequested() {
		t.Fatal("a failed read changed the configuration")
	}
}

// While the core refuses the key, every call the node makes gets 401; they
// ask for a configuration read at most once a poll interval.
func TestRemotePacesRereadsWhileTheKeyIsRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		core := newFakeCore(remoteBody())
		remote := NewRemote(core, "")
		if err := remote.Load(t.Context()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go remote.Run(ctx)

		core.fail(&nodecontrol.StatusError{Path: nodecontrol.PathConfig, Status: http.StatusUnauthorized})
		remote.ReportUnauthorized()
		synctest.Wait()
		if !remote.KeyRejected() {
			t.Fatal("a 401 on another call did not lead to a read that found the key rejected")
		}
		readsAfterRejection := len(core.readTimes())
		for range 20 {
			remote.ReportUnauthorized()
			time.Sleep(time.Second) //nolint:forbidigo // advances the synctest bubble's fake clock
			synctest.Wait()
		}
		// Run's own poll at 30 s falls outside the 20 s above.
		if got := len(core.readTimes()); got != readsAfterRejection {
			t.Fatalf("20 refused calls in 20 s caused %d reads, want none", got-readsAfterRejection)
		}

		core.fail(nil)
		time.Sleep(RemotePollInterval) //nolint:forbidigo // advances the synctest bubble's fake clock
		synctest.Wait()
		if remote.KeyRejected() {
			t.Fatal("a read the core answered did not clear the rejection")
		}
	})
}
