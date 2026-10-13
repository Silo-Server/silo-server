package mediasample

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/executionlease"
	"github.com/Silo-Server/silo-server/internal/workerinput"
)

func TestRegisteredSourceRevokedBeforeSheetRepairCannotPublishInitialSheets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.mkv")
	if err := os.WriteFile(path, []byte("admitted input"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, input, _ := registeredSamplingInput(t, path)
	handle, ok := workerinput.Lookup(ctx, input)
	if !ok {
		t.Fatal("source not admitted")
	}
	seconds := secondsFrom(5, 10, 11)
	var frames []fakeSheetFrame
	for i, at := range seconds {
		if i != 1 {
			frames = append(frames, tagged(byte(10*(i+1)), at))
		}
	}
	initial := &fakeSheets{format: "matroska,webm", frames: frames}
	runs := 0
	run := func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		err := initial.exec(ctx, name, args, stdin, stdout, stderr)
		if slices.Contains(args, "concat") {
			runs++
			if runs == 1 {
				_ = handle.Close()
			}
		}
		return err
	}
	req := sheetsRequest(seconds, 6, 2)
	req.Input = input
	result, err := Runner{Exec: run}.Run(ctx, req)
	if ctx.Err() != nil {
		t.Fatal("fixture must retain the outer request")
	}
	if err == nil || len(result.Sheets) != 0 {
		t.Fatalf("revoked repair published initial sheets: sheets=%d frames=%+v err=%v", len(result.Sheets), result.SheetFrames, err)
	}
	if runs != 1 {
		t.Fatalf("revoked source reached repair executor: runs%d", runs)
	}
}

func TestRegisteredRemoteLongGOPRepairMatchesLocalSheets(t *testing.T) {
	ffmpeg, caps := realFFmpeg(t)
	path := filepath.Join(t.TempDir(), "long-gop.mkv")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "nullsrc=s=64x36:r=24:d=60,geq=lum='clip(4*T\\,0\\,255)':cb=128:cr=128",
		"-c:v", "libx264", "-threads", "1", "-g", "600", "-keyint_min", "600", "-sc_threshold", "0", "-bf", "2", path}
	if output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("generate long-GOP fixture: %v: %s", err, output)
	}
	req := Request{
		Input: path, Samples: &Samples{Seconds: []float64{5, 15, 25, 35, 45, 55}},
		Sheets: &SheetsOutput{TileWidth: 64, TileHeight: 36, Columns: 3, Rows: 2, Quality: 90}, Threads: 1,
	}
	if err := caps.Require(req); err != nil {
		t.Skipf("ffmpeg cannot sample sheets: %v", err)
	}
	runner := Runner{FFmpegPath: ffmpeg}
	local, err := runner.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if local.SheetFrames != (SheetFrames{Decoded: 6, Refined: 3}) {
		t.Fatalf("fixture did not exercise repeated-cell repair: %+v", local.SheetFrames)
	}
	remoteCtx, input, requests := registeredSamplingInput(t, path)
	req.Input = input
	remote, err := runner.Run(remoteCtx, req)
	if err != nil {
		t.Fatalf("registered remote repair: %v", err)
	}
	if !reflect.DeepEqual(local, remote) {
		t.Fatalf("remote repaired frames/timing or sheets differ: local=%+v remote=%+v", local, remote)
	}
	if requests.Load() == 0 {
		t.Fatal("sheet repair did not read the admitted HTTP source")
	}
}

func TestCapabilitiesRetainsAdmissionUntilDetachedListingFinishes(t *testing.T) {
	fake := newFakeListings()
	fake.block = make(chan struct{})
	release := sync.OnceFunc(func() { close(fake.block) })
	t.Cleanup(release)
	useListings(t, fake)
	entered := make(chan struct{})
	var once sync.Once
	capsCache.Lock()
	previous := capsCache.list
	capsCache.list = func(ctx context.Context, name string, stdin []byte, args ...string) ([]byte, error) {
		once.Do(func() { close(entered) })
		return previous(ctx, name, stdin, args...)
	}
	capsCache.Unlock()
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	var refs atomic.Int32
	refs.Store(1)
	leaseReleased := make(chan struct{})
	ctx := executionlease.WithRetainer(parent, func() (func(), error) {
		refs.Add(1)
		return func() { refs.Add(-1); close(leaseReleased) }, nil
	})
	binary := fakeBinary(t)
	completed := make(chan error, 1)
	go func() { _, err := LoadCapabilities(ctx, binary); completed <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("listing did not start")
	}
	cancel()
	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller did not cancel")
	}
	refs.Add(-1) // The request releases its original local admission.
	if count := refs.Load(); count != 1 {
		t.Fatalf("detached listing lost admitted capacity: refs%d", count)
	}
	release()
	select {
	case <-leaseReleased:
	case <-time.After(time.Second):
		t.Fatal("completed listing retained admission")
	}
	if refs.Load() != 0 || fake.calls.Load() != 4 {
		t.Fatalf("listing completion refs%d commands%d", refs.Load(), fake.calls.Load())
	}
}

func TestCapabilitiesRefusesDetachedListingWithoutAdmission(t *testing.T) {
	fake := newFakeListings()
	useListings(t, fake)
	denied := errors.New("work admission no longer held")
	ctx := executionlease.WithRetainer(t.Context(), func() (func(), error) { return nil, denied })
	if _, err := LoadCapabilities(ctx, fakeBinary(t)); !errors.Is(err, denied) || fake.calls.Load() != 0 {
		t.Fatalf("unadmitted listing ran: commands%d err=%v", fake.calls.Load(), err)
	}
}
