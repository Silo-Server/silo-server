package workercontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestExecutionConfigPreservesOperatorSettingsAndSnapshots(t *testing.T) {
	c := newLocalConfig(Options{FFmpeg: "/local/ffmpeg", HWAccel: "qsv", HWDevice: "/dev/dri/renderD130", Scratch: "/local/scratch", MaxJobs: 2})
	previous := c.Config()
	config := workerwire.ExecutionConfig{Revision: "r1", SegmentRetentionSeconds: 120, SubtitleSyncCapacity: 12, Drain: true, OutputAuthorizationEpoch: "opaque-epoch"}
	if err := c.apply(config); err != nil {
		t.Fatal(err)
	}
	next := c.Config()
	if next.Playback.FFmpegPath != "/local/ffmpeg" || next.Playback.HWAccel != "qsv" || next.Playback.HWDevice != "/dev/dri/renderD130" || next.Playback.TranscodeDir != "/local/scratch" || next.Playback.SubtitleSyncNodeCapacity != 2 || next.Playback.SegmentRetentionSeconds != 120 {
		t.Fatalf("operator settings changed or config not applied: %+v", next.Playback)
	}
	if previous.Playback.SegmentRetentionSeconds != 600 {
		t.Fatal("published previous snapshot mutated")
	}
	if next != c.Config() {
		t.Fatal("unchanged settings did not preserve reload-fence identity")
	}
	if err := c.apply(config); err != nil {
		t.Fatal(err)
	}
	if next != c.Config() {
		t.Fatal("identical signed refresh changed reload-fence identity")
	}
	if !c.NodeDrainRequested() || c.OutputAuthorizationEpoch() != "opaque-epoch" {
		t.Fatal("drain/epoch not applied")
	}
}

func TestSignedSettingsForceReloadAndReentrantDrain(t *testing.T) {
	corePublic, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	nodePublic, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	var mode atomic.Int32
	var requests atomic.Int32
	core := httptest.NewUnstartedServer(nil)
	origin := "http://" + core.Listener.Addr().String()
	verifier := &workeridentity.Verifier{Origin: origin, Domain: workeridentity.DomainNodeProof, AllowHTTP: true, ResolveKey: func(int) (ed25519.PublicKey, error) { return nodePublic, nil }}
	core.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if err := verifier.Verify(request, 17); err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		if mode.Load() == 2 {
			w.WriteHeader(503)
			return
		}
		var input struct {
			BootID   string `json:"boot_id"`
			Nonce    string `json:"nonce"`
			Sequence uint64 `json:"sequence"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		settings := workerwire.ExecutionConfig{Revision: "r1", SegmentRetentionSeconds: 120, SubtitleSyncCapacity: 8, Drain: true, OutputAuthorizationEpoch: "epoch"}
		if mode.Load() == 1 {
			settings.Revision = "r2"
			settings.OutputAuthorizationEpoch = ""
		}
		snapshot := workerwire.Snapshot{Version: 1, NodeID: 17, BootID: input.BootID, Nonce: input.Nonce, Sequence: input.Sequence + 1, Active: map[string]uint64{}, ValidForMillis: 2000, ExecutionConfig: &settings}
		body, _ := json.Marshal(snapshot)
		encoded, err := workeridentity.EncodeGrant(workeridentity.Grant{Version: 1, Domain: workeridentity.DomainSnapshotGrant, Audience: "http://worker.example.test", NodeID: 17, ExpiresAt: time.Now().Add(time.Minute).Unix(), Body: body}, coreKey)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"grant": encoded})
	})
	core.Start()
	defer core.Close()
	r, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: origin, CorePublicKey: corePublic, NodePrivateKey: nodeKey, NodeURL: "http://worker.example.test", AllowHTTP: true}, Scratch: t.TempDir(), MaxJobs: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(context.Background()) }()
	var callbacks atomic.Int32
	r.config.OnNodeDrainChange(func(bool) {
		callbacks.Add(1)
		if err := r.config.ForceReload(t.Context()); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = r.config.ForceReload(ctx); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 1 || requests.Load() != 2 || !r.config.NodeDrainRequested() || r.config.Config().Playback.SubtitleSyncNodeCapacity != 2 {
		t.Fatalf("settings/callbacks not refreshed: callbacks%d requests%d", callbacks.Load(), requests.Load())
	}
	mode.Store(1)
	if r.config.ForceReload(ctx) == nil {
		t.Fatal("signed blank epoch was admitted")
	}
	if r.config.OutputAuthorizationEpoch() != "epoch" {
		t.Fatal("invalid settings partly installed")
	}
	mode.Store(2)
	if r.config.ForceReload(ctx) == nil {
		t.Fatal("failed signed refresh hidden")
	}
}

func TestExecutionConfigDrainCallbackCanReadAndRefresh(t *testing.T) {
	c := newLocalConfig(Options{})
	var refreshed bool
	c.setRefresh(func(context.Context) error { refreshed = true; _ = c.Config(); return nil })
	var changes []bool
	c.OnNodeDrainChange(func(drain bool) {
		if c.NodeDrainRequested() != drain {
			t.Error("callback ran before committed drain")
		}
		changes = append(changes, drain)
		if err := c.ForceReload(t.Context()); err != nil {
			t.Error(err)
		}
	})
	base := workerwire.ExecutionConfig{Revision: "r1", SegmentRetentionSeconds: 600, SubtitleSyncCapacity: 1, Drain: true, OutputAuthorizationEpoch: "epoch"}
	if err := c.apply(base); err != nil {
		t.Fatal(err)
	}
	if err := c.apply(base); err != nil {
		t.Fatal(err)
	}
	base.Revision, base.Drain = "r2", false
	if err := c.apply(base); err != nil {
		t.Fatal(err)
	}
	if !refreshed || len(changes) != 2 || !changes[0] || changes[1] {
		t.Fatalf("changes=%v refreshed=%v", changes, refreshed)
	}
	c.setRefresh(func(context.Context) error { return errors.New("offline") })
	if c.ForceReload(t.Context()) == nil {
		t.Fatal("failed signed refresh hidden")
	}
}

func TestExecutionConfigRejectsInvalidAndConflictingRevision(t *testing.T) {
	c := newLocalConfig(Options{})
	base := workerwire.ExecutionConfig{Revision: "r1", SegmentRetentionSeconds: 600, SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: "epoch"}
	if err := c.apply(base); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []workerwire.ExecutionConfig{
		{}, {Revision: "r2", SegmentRetentionSeconds: -1, SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: "epoch"},
		{Revision: "r2", SegmentRetentionSeconds: 600, SubtitleSyncCapacity: 0},
		{Revision: "r1", SegmentRetentionSeconds: 120, SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: "epoch"},
	} {
		if err := c.apply(invalid); err == nil {
			t.Fatalf("accepted invalid config: %+v", invalid)
		}
	}
	if c.Config().Playback.SegmentRetentionSeconds != 600 {
		t.Fatal("invalid config partially applied")
	}
}

func TestExecutionConfigConcurrentReadsRefreshAndDrain(t *testing.T) {
	c := newLocalConfig(Options{MaxJobs: 2})
	c.setRefresh(func(context.Context) error { return nil })
	c.OnNodeDrainChange(func(bool) { _ = c.Config(); _ = c.NodeDrainRequested() })
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				_ = c.Config()
				_ = c.OutputAuthorizationEpoch()
				_ = c.ForceReload(t.Context())
			}
		})
	}
	for i := range 100 {
		if err := c.apply(workerwire.ExecutionConfig{Revision: string(rune(1000 + i)), SegmentRetentionSeconds: 600, SubtitleSyncCapacity: 4, Drain: i%2 == 0, OutputAuthorizationEpoch: "epoch"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
