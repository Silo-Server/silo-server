package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/nodesource/nodesourcetest"
)

// The staging timeout allows two minutes plus the source at 4 MiB/s, a slow
// link between node and core, at most four hours, so a large remux read
// through the relay is not cut short.
func TestStagedSubtitleTimeoutScalesWithTheSource(t *testing.T) {
	for _, tc := range []struct {
		size int64
		want time.Duration
	}{
		{0, 2 * time.Minute},
		{1 << 20, 2 * time.Minute},
		{4 << 30, 2*time.Minute + 1024*time.Second},
		{50 << 30, 2*time.Minute + 12800*time.Second},
		{60 << 30, 4 * time.Hour},
		{1 << 40, 4 * time.Hour},
	} {
		if got := stagedSubtitleTimeout(tc.size); got != tc.want {
			t.Errorf("stagedSubtitleTimeout(%d) = %s, want %s", tc.size, got, tc.want)
		}
	}
}

// Staged fonts must look like fonts and are named by their stream index: the
// attachment's own filename, which the source chooses, never becomes a path.
// An attachment that does not look like a font is left out, as libass would
// fail to load it, rather than failing the staging.
func TestSplitStagedFontsChecksAndRenamesFonts(t *testing.T) {
	ttf := append([]byte("\x00\x01\x00\x00"), bytes.Repeat([]byte{1}, 12)...)
	otf := append([]byte("OTTO"), bytes.Repeat([]byte{2}, 8)...)
	script := []byte("#!/bin/sh\nrm -rf /\n")
	streams := []attachmentProbeStream{
		{Index: 3, ExtraDataSize: int64(len(ttf)), CodecType: "attachment", CodecName: "ttf", Tags: map[string]string{"filename": "../../x.ttf"}},
		{Index: 4, ExtraDataSize: int64(len(script)), CodecType: "attachment", CodecName: "ttf"},
		{Index: 5, ExtraDataSize: int64(len(otf)), CodecType: "attachment", Tags: map[string]string{"filename": "/etc/cron.d/evil", "mimetype": "font/otf"}},
	}
	fonts, err := splitStagedFonts(streams, bytes.Join([][]byte{ttf, script, otf}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(fonts) != 2 || fonts[0].Name != "attachment-3.ttf" || fonts[1].Name != "attachment-5.ttf" ||
		!bytes.Equal(fonts[0].Data, ttf) || !bytes.Equal(fonts[1].Data, otf) {
		t.Fatalf("fonts %+v", fonts)
	}
	if _, err := splitStagedFonts(streams[:1], ttf[:8]); err == nil {
		t.Fatal("fonts that differ from their probed sizes were staged")
	}
}

// The fonts extracted are the first within the count and byte bounds, as
// ListAttachedSubtitleFonts keeps them; an attachment without data, or one
// past the byte budget, is skipped rather than failing the staging.
func TestBoundedFontStreamsKeepsTheFirstWithinBounds(t *testing.T) {
	var streams []attachmentProbeStream
	streams = append(streams,
		attachmentProbeStream{Index: 1, ExtraDataSize: 0},
		attachmentProbeStream{Index: 2, ExtraDataSize: maxSubtitleFontBytes - 100},
		attachmentProbeStream{Index: 3, ExtraDataSize: 200},
	)
	for i := range maxSubtitleFontAttachments + 5 {
		streams = append(streams, attachmentProbeStream{Index: 10 + i, ExtraDataSize: 1})
	}
	kept := boundedFontStreams(streams)
	if len(kept) != maxSubtitleFontAttachments || kept[0].Index != 2 || kept[1].Index != 10 || kept[len(kept)-1].Index != 10+maxSubtitleFontAttachments-2 {
		t.Fatalf("kept %d fonts, first %+v", len(kept), kept[:2])
	}
	var total int64
	for _, stream := range kept {
		total += stream.ExtraDataSize
	}
	if total > maxSubtitleFontBytes {
		t.Fatalf("kept %d bytes of fonts", total)
	}
}

func TestFontAttachmentStreamsKeepsEachStreamOnce(t *testing.T) {
	listing := `{"streams":[{"index":2,"codec_type":"attachment","codec_name":"png","extradata_size":10,"tags":{"filename":"cover.png"}},` +
		`{"index":3,"codec_type":"attachment","codec_name":"ttf","extradata_size":10},` +
		`{"index":3,"codec_type":"attachment","codec_name":"ttf","extradata_size":10}]}`
	streams, err := fontAttachmentStreams([]byte(listing))
	if err != nil || len(streams) != 1 || streams[0].Index != 3 {
		t.Fatalf("streams %+v, %v; want the font once", streams, err)
	}
	if _, err := fontAttachmentStreams([]byte("not json")); err == nil {
		t.Fatal("an unreadable listing was accepted")
	}
}

// An extracted track must start as an ASS script, a byte order mark aside.
func TestCheckASSScriptRequiresTheScriptInfoSection(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		text string
		ok   bool
	}{
		"script":      {"[Script Info]\nScriptType: v4.00+\n\n[Events]\n", true},
		"bom":         {utf8BOM + "[Script Info]\n[Events]\n", true},
		"blank first": {"\r\n[Script Info]\n", true},
		"events only": {"Dialogue: x\n[Events]\n", false}, //nolint:misspell // ASS event keyword
		"empty":       {"", false},
		"other text":  {"WEBVTT\n\n00:00.000 --> 00:01.000\n[Script Info]\n", false},
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := os.WriteFile(path, []byte(tc.text), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := checkASSScript(path); (err == nil) != tc.ok {
			t.Errorf("%s: checkASSScript = %v, want ok %t", name, err, tc.ok)
		}
	}
}

// A staged track is the script's only track (si=0), with the source's fonts
// in its fontsdir; a local source keeps its alias and track index.
func TestSubtitleBurnInFilterForAStagedTrack(t *testing.T) {
	opts := TranscodeOpts{InputPath: "/media/movie.mkv", SubtitleTrackIndex: 2}
	opts.subtitleFilterInputPath = "/tmp/session/subtitle-source.media"
	if got := subtitleBurnInFilter(opts, true); got != "subtitles=filename='/tmp/session/subtitle-source.media':si=2" {
		t.Fatalf("aliased filter %q", got)
	}
	useStagedSubtitle(&opts, "/tmp/a:b/subtitle-0f")
	want := `subtitles='/tmp/a\:b/subtitle-0f/selected.ass':si=0:fontsdir='/tmp/a\:b/subtitle-0f/fonts'`
	if got := subtitleBurnInFilter(opts, false); got != want {
		t.Fatalf("staged filter\n got %q\nwant %q", got, want)
	}
}

// A relay URL never reaches the subtitles filter: without a staged track a
// relayed burn-in is refused, released handle or not.
func TestCheckSubtitleFilterInputRefusesARelayURL(t *testing.T) {
	h := nodesourcetest.Relayed(t, writeSource(t))
	opts := TranscodeOpts{InputPath: h.Input(), SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "ass", TargetCodecVideo: "h264"}
	if err := checkSubtitleFilterInput(opts); !errors.Is(err, ErrSubtitleStaging) {
		t.Fatalf("unstaged relayed burn-in = %v", err)
	}
	_ = h.Close()
	if err := checkSubtitleFilterInput(opts); !errors.Is(err, ErrSubtitleStaging) {
		t.Fatalf("unstaged burn-in of a released relay URL = %v", err)
	}
	useStagedSubtitle(&opts, t.TempDir())
	if err := checkSubtitleFilterInput(opts); err != nil {
		t.Fatalf("staged burn-in = %v", err)
	}
	opts.TargetCodecVideo = "copy"
	opts.subtitleFilterInputPath = ""
	if err := checkSubtitleFilterInput(opts); err != nil {
		t.Fatalf("copied video = %v", err)
	}
}

func writeSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.mkv")
	if err := os.WriteFile(path, []byte("relayed source bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeStaging is a staging cache whose stagings are scripted: each writes a
// complete staging of size bytes once release is closed, or fails with err.
type fakeStaging struct {
	cache   *RelayedSubtitleStaging
	runs    atomic.Int32
	release chan struct{}
	err     error
	size    int
	inputs  sync.Map
	now     atomic.Int64
}

func newFakeStaging(t *testing.T) *fakeStaging {
	t.Helper()
	cache, err := NewRelayedSubtitleStaging(filepath.Join(t.TempDir(), SubtitleStagingDirName))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeStaging{cache: cache, release: make(chan struct{}), size: 100}
	f.now.Store(time.Now().UnixNano())
	cache.now = func() time.Time { return time.Unix(0, f.now.Load()) }
	cache.stage = func(ctx context.Context, job stagingJob, dir string) error {
		f.runs.Add(1)
		f.inputs.Store(job.input, true)
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if !nodesource.IsRemote(job.input) {
			return errors.New("the staging's own handle is not open")
		}
		if f.err != nil {
			return f.err
		}
		script := assScriptHeader + "\n" + strings.Repeat("a", f.size-len(assScriptHeader)-1)
		if err := os.WriteFile(filepath.Join(dir, stagedSubtitleFile), []byte(script), 0o600); err != nil {
			return err
		}
		return os.Mkdir(filepath.Join(dir, stagedSubtitleFontsDir), 0o700)
	}
	return f
}

func (f *fakeStaging) advance(d time.Duration) { f.now.Add(int64(d)) }

func (f *fakeStaging) opts(h *nodesource.Handle, track int) TranscodeOpts {
	return TranscodeOpts{InputPath: h.Input(), SubtitleBurnIn: true, SubtitleTrackIndex: track, SubtitleCodec: "ass",
		TargetCodecVideo: "h264", SubtitleStaging: f.cache}
}

// waitStaged waits for a lease's staging to end and returns its result.
func waitStaged(t *testing.T, lease *subtitleLease) (string, error) {
	t.Helper()
	select {
	case <-lease.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the staging never ended")
	}
	return lease.result()
}

// One staging runs per source revision, track and codec, whatever the number
// of starts, detached from the request that began it and on a source handle
// of its own, which it releases when done.
func TestStagedSubtitleIsSharedAndDetached(t *testing.T) {
	f := newFakeStaging(t)
	relay := nodesourcetest.CoreRelay(t)
	path := writeSource(t)
	first, err := relay.Open(t.Context(), nodesourcetest.Grant(path))
	if err != nil {
		t.Fatal(err)
	}
	second, err := relay.Open(t.Context(), nodesourcetest.Grant(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()

	requestCtx, endRequest := context.WithCancel(t.Context())
	lease, err := acquireStagedSubtitle(requestCtx, f.opts(first, 0))
	if err != nil {
		t.Fatal(err)
	}
	// The request that began it, and the job's handle, go away.
	endRequest()
	_ = first.Close()
	joined, err := acquireStagedSubtitle(t.Context(), f.opts(second, 0))
	if err != nil {
		t.Fatal(err)
	}
	if joined.e != lease.e || lease.ready() {
		t.Fatal("a second start on the same track did not join the running staging")
	}
	close(f.release)
	dir, err := waitStaged(t, joined)
	if err != nil {
		t.Fatalf("staging = %v", err)
	}
	if size, ok := stagedSubtitleReady(dir); !ok || size != int64(f.size) {
		t.Fatalf("staged %s: %d bytes, ready %t", dir, size, ok)
	}
	if runs := f.runs.Load(); runs != 1 {
		t.Fatalf("%d stagings ran, want 1", runs)
	}
	f.inputs.Range(func(input, _ any) bool {
		if input == first.Input() || input == second.Input() {
			t.Errorf("the staging read a job's own handle")
		}
		return true
	})
	// The staging's own handle is released once it ends.
	waitFor(t, func() bool {
		open := false
		f.inputs.Range(func(input, _ any) bool {
			open = open || nodesource.IsRemote(input.(string))
			return true
		})
		return !open
	})

	// A later start reuses the completed staging; another track stages anew.
	again, err := acquireStagedSubtitle(t.Context(), f.opts(second, 0))
	if err != nil || !again.ready() || f.runs.Load() != 1 {
		t.Fatalf("reuse = %v, ready %t after %d runs", err, again.ready(), f.runs.Load())
	}
	other, err := acquireStagedSubtitle(t.Context(), f.opts(second, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitStaged(t, other); err != nil || f.runs.Load() != 2 {
		t.Fatalf("another track = %v after %d runs", err, f.runs.Load())
	}
	for _, l := range []*subtitleLease{lease, joined, again, other} {
		l.release()
	}
}

// A failed staging answers the starts that come within
// stagedSubtitleFailureTTL with its failure, without reading the source
// again; one after it stages again.
func TestStagedSubtitleFailureIsRememberedBriefly(t *testing.T) {
	f := newFakeStaging(t)
	f.err = errors.New("ffmpeg failed")
	close(f.release)
	h := nodesourcetest.Relayed(t, writeSource(t))
	lease, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitStaged(t, lease); !errors.Is(err, ErrSubtitleStaging) || !errors.Is(err, f.err) {
		t.Fatalf("staging = %v", err)
	}
	lease.release()
	retry, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil || !retry.failed() || f.runs.Load() != 1 {
		t.Fatalf("retry within the TTL = %v, failed %t after %d runs", err, retry.failed(), f.runs.Load())
	}
	retry.release()
	f.advance(stagedSubtitleFailureTTL)
	f.err = nil
	later, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitStaged(t, later); err != nil || f.runs.Load() != 2 {
		t.Fatalf("later start = %v after %d runs", err, f.runs.Load())
	}
	later.release()
}

// The cache keeps its stagings within its byte bound: a new staging, which
// counts at its caps (stagingReserveBytes), first evicts the least recently
// used unleased completed one, never a leased one, and is refused when
// nothing more can go. Its sweep removes those unused for its age.
func TestStagedSubtitleCacheIsBounded(t *testing.T) {
	f := newFakeStaging(t)
	close(f.release)
	f.cache.maxBytes = stagingReserveBytes + 150
	h := nodesourcetest.Relayed(t, writeSource(t))
	stage := func(track int) *subtitleLease {
		t.Helper()
		lease, err := acquireStagedSubtitle(t.Context(), f.opts(h, track))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := waitStaged(t, lease); err != nil {
			t.Fatal(err)
		}
		f.advance(time.Second)
		return lease
	}
	leased := stage(0)
	unused := stage(1)
	unused.release()
	f.advance(time.Second)
	third := stage(2) // 200 bytes and one in progress: the unleased track 1 goes
	if isRealDir(unused.e.dir) || !isRealDir(leased.e.dir) || !isRealDir(third.e.dir) {
		t.Fatalf("eviction kept track 1 %t, track 0 %t, track 2 %t", isRealDir(unused.e.dir), isRealDir(leased.e.dir), isRealDir(third.e.dir))
	}
	// Every staging is leased: no room for one in progress.
	if _, err := acquireStagedSubtitle(t.Context(), f.opts(h, 3)); !errors.Is(err, ErrSubtitleStagingBusy) {
		t.Fatalf("a staging past the bound = %v, want ErrSubtitleStagingBusy", err)
	}
	if !isRealDir(leased.e.dir) || !isRealDir(third.e.dir) {
		t.Fatal("a leased staging was evicted")
	}
	leased.release()
	fourth := stage(3) // the released track 0 goes
	if isRealDir(leased.e.dir) || !isRealDir(fourth.e.dir) {
		t.Fatal("a staging that fits after an eviction was not made room for")
	}
	third.release()
	fourth.release()
	if removed := f.cache.Sweep(time.Hour); removed != 0 {
		t.Fatalf("swept %d recent stagings", removed)
	}
	f.advance(2 * time.Hour)
	if removed := f.cache.Sweep(time.Hour); removed != 2 || isRealDir(third.e.dir) || isRealDir(fourth.e.dir) {
		t.Fatalf("sweep removed %d stagings", removed)
	}
}

// A staging in progress counts at its caps, so the bound holds before any
// byte is written: a third concurrent staging that would take the cache
// past it is refused until one completes.
func TestStagingInProgressCountsAtItsCaps(t *testing.T) {
	f := newFakeStaging(t)
	f.cache.slots = make(chan struct{}, 3)
	f.cache.maxBytes = 2*stagingReserveBytes + 1000
	h := nodesourcetest.Relayed(t, writeSource(t))
	first, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := acquireStagedSubtitle(t.Context(), f.opts(h, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireStagedSubtitle(t.Context(), f.opts(h, 2)); !errors.Is(err, ErrSubtitleStagingBusy) {
		t.Fatalf("a third staging in progress = %v, want ErrSubtitleStagingBusy", err)
	}
	close(f.release)
	for _, lease := range []*subtitleLease{first, second} {
		if _, err := waitStaged(t, lease); err != nil {
			t.Fatal(err)
		}
	}
	third, err := acquireStagedSubtitle(t.Context(), f.opts(h, 2))
	if err != nil {
		t.Fatalf("a staging that fits once the others completed = %v", err)
	}
	if _, err := waitStaged(t, third); err != nil {
		t.Fatal(err)
	}
	for _, lease := range []*subtitleLease{first, second, third} {
		lease.release()
	}
}

// At most the cache's slots of stagings run at once. A start that needs
// another waits for a slot within its own context, behind at most maxQueued
// others; beyond them it is refused, and one whose context ends stops
// waiting. A start that joins a staging already begun needs no slot.
func TestStagingAdmissionIsBounded(t *testing.T) {
	f := newFakeStaging(t)
	f.cache.maxQueued = 1
	h := nodesourcetest.Relayed(t, writeSource(t))
	queued := func() int {
		f.cache.mu.Lock()
		defer f.cache.mu.Unlock()
		return f.cache.queued
	}
	var leases []*subtitleLease
	for track := range 2 {
		lease, err := acquireStagedSubtitle(t.Context(), f.opts(h, track))
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
	}
	waitCtx, stopWaiting := context.WithCancel(t.Context())
	waited := make(chan error, 1)
	go func() {
		_, err := acquireStagedSubtitle(waitCtx, f.opts(h, 2))
		waited <- err
	}()
	waitFor(t, func() bool { return queued() == 1 })
	if _, err := acquireStagedSubtitle(t.Context(), f.opts(h, 3)); !errors.Is(err, ErrSubtitleStagingBusy) {
		t.Fatalf("a start past the queue = %v, want ErrSubtitleStagingBusy", err)
	}
	joined, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil || joined.e != leases[0].e {
		t.Fatalf("joining a running staging = %v", err)
	}
	leases = append(leases, joined)
	stopWaiting()
	if err := <-waited; !errors.Is(err, ErrSubtitleStagingBusy) || !errors.Is(err, context.Canceled) {
		t.Fatalf("a waiter whose request ended = %v", err)
	}
	if queued() != 0 || f.runs.Load() != 2 {
		t.Fatalf("%d queued, %d stagings run", queued(), f.runs.Load())
	}

	// A waiter gets the slot a completed staging gives back.
	go func() {
		lease, err := acquireStagedSubtitle(t.Context(), f.opts(h, 4))
		if err == nil {
			_, err = waitStaged(t, lease)
			lease.release()
		}
		waited <- err
	}()
	waitFor(t, func() bool { return queued() == 1 })
	close(f.release)
	if err := <-waited; err != nil {
		t.Fatalf("a queued start = %v", err)
	}
	if f.runs.Load() != 3 {
		t.Fatalf("%d stagings ran, want 3", f.runs.Load())
	}
	for _, lease := range leases {
		lease.release()
	}
}

// A staging in progress that no session leases any more is canceled once
// its grace ends, and leaves the cache, so the next start stages afresh
// rather than joining its cancellation. A session that leases it within the
// grace keeps it running.
func TestUnleasedStagingIsCanceledAfterItsGrace(t *testing.T) {
	f := newFakeStaging(t)
	f.cache.idleGrace = time.Hour
	h := nodesourcetest.Relayed(t, writeSource(t))
	lease, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil {
		t.Fatal(err)
	}
	lease.release()
	again, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil || again.e != lease.e || again.e.closed() {
		t.Fatalf("a start within the grace = %v; joined %t", err, again.e == lease.e)
	}

	f.cache.idleGrace = 10 * time.Millisecond
	again.release()
	if _, err := waitStaged(t, again); !errors.Is(err, context.Canceled) {
		t.Fatalf("an unleased staging after its grace = %v, want canceled", err)
	}
	waitFor(t, func() bool {
		f.cache.mu.Lock()
		defer f.cache.mu.Unlock()
		return len(f.cache.running) == 0 && len(f.cache.slots) == 0
	})
	close(f.release)
	fresh, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil || fresh.e == lease.e {
		t.Fatalf("the next start = %v, joined the canceled staging %t", err, fresh.e == lease.e)
	}
	if _, err := waitStaged(t, fresh); err != nil || f.runs.Load() != 2 {
		t.Fatalf("the next staging = %v after %d runs", err, f.runs.Load())
	}
	fresh.release()
}

// A failure that follows from the source's bytes is remembered until the
// sweep, not for stagedSubtitleFailureTTL: staging that revision again would
// read the whole source to fail the same way.
func TestDeterministicStagingFailureIsKeptUntilTheSweep(t *testing.T) {
	f := newFakeStaging(t)
	f.err = deterministic(errors.New("the extracted track is not an ASS script"))
	close(f.release)
	h := nodesourcetest.Relayed(t, writeSource(t))
	lease, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitStaged(t, lease); !errors.Is(err, f.err) {
		t.Fatalf("staging = %v", err)
	}
	lease.release()
	f.advance(10 * stagedSubtitleFailureTTL)
	f.cache.Sweep(time.Hour) // the failure is younger than the sweep's age
	retry, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil || !retry.failed() || f.runs.Load() != 1 {
		t.Fatalf("a start past the failure TTL = %v, failed %t after %d runs", err, retry.failed(), f.runs.Load())
	}
	retry.release()
	f.advance(2 * time.Hour)
	f.cache.Sweep(time.Hour)
	f.err = nil
	later, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitStaged(t, later); err != nil || f.runs.Load() != 2 {
		t.Fatalf("a start after the sweep = %v after %d runs", err, f.runs.Load())
	}
	later.release()
}

// Evicting a staging removes its directory outside the cache's mutex and
// outside the session's: a slow removal holds up neither other starts nor
// requests for the session releasing it.
func TestEvictionRemovesOutsideTheLocks(t *testing.T) {
	f := newFakeStaging(t)
	close(f.release)
	removing := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	f.cache.removeDir = func(dir string) error {
		if strings.Contains(filepath.Base(dir), "removed-") {
			once.Do(func() { close(removing) })
			<-unblock
		}
		return os.RemoveAll(dir)
	}
	ffmpeg, _ := fakeFFmpeg(t)
	h := nodesourcetest.Relayed(t, writeSource(t))
	f.cache.maxBytes = stagingReserveBytes
	lease, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitStaged(t, lease); err != nil {
		t.Fatal(err)
	}
	lease.release()
	session, err := StartTranscode(t.Context(), preparingOpts(t, f, h, ffmpeg))
	if err != nil {
		t.Fatal(err)
	}
	f.cache.maxBytes = 0 // the session's release evicts its staging
	closed := make(chan error, 1)
	go func() { closed <- session.Close() }()
	select {
	case <-removing:
	case <-time.After(10 * time.Second):
		t.Fatal("the release never evicted the staging")
	}
	// The removal holds neither the cache's mutex nor the session's.
	locked := make(chan struct{})
	go func() {
		f.cache.mu.Lock()
		f.cache.mu.Unlock() //nolint:staticcheck // only waits for the mutex
		session.IsRunning()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		close(unblock)
		t.Fatal("a staging's removal holds the cache's or the session's mutex")
	}
	close(unblock)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

// A node's restart adopts the complete stagings a previous run of this
// layout left, once their content checks out as staging's own does, and
// removes the rest: partial ones, those of another layout version, and those
// whose script or fonts are not what staging writes.
func TestNewRelayedSubtitleStagingAdoptsCompleteStagings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), SubtitleStagingDirName)
	const script = "[Script Info]\nScriptType: v4.00+\n"
	font := append([]byte("\x00\x01\x00\x00"), bytes.Repeat([]byte{1}, 12)...)
	write := func(name, script string, fonts ...[]byte) string {
		t.Helper()
		staging := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Join(staging, stagedSubtitleFontsDir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staging, stagedSubtitleFile), []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		for i, data := range fonts {
			if err := os.WriteFile(filepath.Join(staging, stagedSubtitleFontsDir, fmt.Sprintf("attachment-%d.ttf", i)), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return staging
	}
	complete := write(stagingKey(`"file-1-2-3"`, 0, "ass"), script, font)
	partial := write(stagingTempPrefix+"0123-1", script)
	unversioned := write("0123456789abcdef0123456789abcdef", script, font)
	otherVersion := write("v0-0123456789abcdef0123456789abcdef", script, font)
	notASS := write(stagingKey(`"file-1-2-3"`, 1, "ass"), "WEBVTT\n\n", font)
	notFont := write(stagingKey(`"file-1-2-3"`, 2, "ass"), script, []byte("#!/bin/sh\nexit 0\n"))
	broken := filepath.Join(dir, stagingKey(`"file-1-2-3"`, 3, "ass"))
	if err := os.MkdirAll(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	cache, err := NewRelayedSubtitleStaging(dir)
	if err != nil {
		t.Fatal(err)
	}
	if e := cache.entries[filepath.Base(complete)]; e == nil || !e.ended || e.err != nil || len(cache.entries) != 1 {
		t.Fatalf("adopted %v", cache.entries)
	}
	if !isRealDir(complete) {
		t.Fatal("the complete staging was removed")
	}
	for _, removed := range []string{partial, unversioned, otherVersion, notASS, notFont, broken} {
		if isRealDir(removed) {
			t.Errorf("%s survived adoption", filepath.Base(removed))
		}
	}
}

// waitFor polls cond, failing the test after ten seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// subtitleFixture makes an MKV with an ASS track of script, an attached
// TrueType font whose attachment filename tries to escape the font directory,
// and, after it, an attachment that claims to be a font but is not, or skips
// the test without FFmpeg or a font to attach.
func subtitleFixture(t *testing.T, script string) (mkv, font string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	fonts, _ := filepath.Glob("/usr/share/fonts/truetype/*/*.ttf")
	if len(fonts) == 0 {
		t.Skip("no TrueType font under /usr/share/fonts to attach")
	}
	dir := t.TempDir()
	ass := filepath.Join(dir, "sub.ass")
	if err := os.WriteFile(ass, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	notFont := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(notFont, []byte("not a font at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mkv = filepath.Join(dir, "fixture.mkv")
	out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=4",
		"-i", ass, "-attach", fonts[0], "-metadata:s:t:0", "mimetype=application/x-truetype-font", "-metadata:s:t:0", "filename=../../x.ttf",
		"-attach", notFont, "-metadata:s:t:1", "mimetype=font/ttf", "-metadata:s:t:1", "filename=fake.ttf",
		"-map", "0", "-map", "1", "-c:v", "mpeg4", "-c:s", "ass", mkv).CombinedOutput()
	if err != nil {
		t.Fatalf("make fixture: %v\n%s", err, out)
	}
	return mkv, fonts[0]
}

const fixtureScriptHeader = "[Script Info]\nScriptType: v4.00+\nPlayResX: 320\nPlayResY: 240\n\n[V4+ Styles]\n" +
	"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
	"Style: Default,Attached,28,&H0000FFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1\n\n" +
	"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"

// A relayed source's selected track and attached font are staged under
// generated names; an attachment that claims to be a font but is not is left
// out rather than failing the staging, as libass rendering the source would
// skip it.
func TestStageSubtitleWithRealFFmpeg(t *testing.T) {
	mkv, font := subtitleFixture(t, fixtureScriptHeader+
		"Dialogue: 0,0:00:00.50,0:00:02.00,Default,,0,0,0,,Hello: relayed\nDialogue: 0,0:00:02.20,0:00:03.80,Default,,0,0,0,,Second line\n") //nolint:misspell // ASS event keyword
	h := nodesourcetest.Relayed(t, mkv)
	dir := t.TempDir()
	if err := stageSubtitle(t.Context(), stagingJob{ffmpeg: ResolveFFmpegPath(""), input: h.Input(), track: 0, codec: "ass"}, dir); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(dir, stagedSubtitleFile))
	if err != nil || !strings.Contains(string(script), "Dialogue: 0,0:00:00.50,0:00:02.00,Default,,0,0,0,,Hello: relayed") { //nolint:misspell // ASS event keyword
		t.Fatalf("staged script %q, %v", script, err)
	}
	staged, err := os.ReadDir(filepath.Join(dir, stagedSubtitleFontsDir))
	if err != nil || len(staged) != 1 || staged[0].Name() != "attachment-2.ttf" {
		t.Fatalf("staged fonts %v, %v; want the real font alone", staged, err)
	}
	want, _ := os.ReadFile(font)
	if got, _ := os.ReadFile(filepath.Join(dir, stagedSubtitleFontsDir, "attachment-2.ttf")); !bytes.Equal(got, want) {
		t.Fatal("the staged font differs from the attachment")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "x.ttf")); err == nil {
		t.Fatal("the attachment's own filename became a path")
	}
	if _, ok := stagedSubtitleReady(dir); !ok {
		t.Fatal("the staging is not complete")
	}
}

// A script past the old 8 MiB bound stages: it streams to disk under the
// 256 MiB bound.
func TestStageSubtitleStreamsALargeScript(t *testing.T) {
	var events strings.Builder
	line := "Dialogue: 0,0:00:00.50,0:00:02.00,Default,,0,0,0,," + strings.Repeat("x", 200) + "\n" //nolint:misspell // ASS event keyword
	for events.Len() < 9<<20 {
		events.WriteString(line)
	}
	mkv, _ := subtitleFixture(t, fixtureScriptHeader+events.String())
	h := nodesourcetest.Relayed(t, mkv)
	dir := t.TempDir()
	if err := stageSubtitle(t.Context(), stagingJob{ffmpeg: ResolveFFmpegPath(""), input: h.Input(), track: 0, codec: "ass"}, dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, stagedSubtitleFile))
	if err != nil || info.Size() < 8<<20 {
		t.Fatalf("staged script: %v, %v", info, err)
	}
}

// Copied video renders no subtitle and stages nothing.
func TestCopiedVideoStagesNoSubtitle(t *testing.T) {
	h := nodesourcetest.Relayed(t, writeSource(t))
	copied := TranscodeOpts{InputPath: h.Input(), SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "ass", TargetCodecVideo: "copy"}
	if stagesRelayedSubtitle(copied) {
		t.Fatal("copied video stages a subtitle")
	}
	copied.TargetCodecVideo = "h264"
	if !stagesRelayedSubtitle(copied) {
		t.Fatal("a relayed text burn-in does not stage its track")
	}
}

// fakeFFmpeg writes a stand-in ffmpeg that records each run's argv, one
// line per run, then waits to be killed.
func fakeFFmpeg(t *testing.T) (bin, runs string) {
	t.Helper()
	dir := t.TempDir()
	bin, runs = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "runs")
	script := "#!/bin/sh\necho \"$*\" >> \"" + runs + "\"\nexec sleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, runs
}

func ffmpegRuns(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func preparingOpts(t *testing.T, f *fakeStaging, h *nodesource.Handle, ffmpeg string) TranscodeOpts {
	opts := f.opts(h, 0)
	opts.SessionID = "preparing"
	opts.OutputDir = t.TempDir()
	opts.FFmpegPath = ffmpeg
	opts.HWAccel = HWAccelNone
	opts.TargetCodecAudio = "aac"
	opts.SegmentDuration = 2
	opts.TotalDuration = 60
	return opts
}

// A start whose subtitle is still staging returns a preparing session at
// once. A restart while it prepares replaces the pending launch rather than
// starting FFmpeg without the subtitle; once the staging completes, FFmpeg
// starts once, with the restart's position and the staged script.
func TestPreparingSessionLaunchesOnceStaged(t *testing.T) {
	f := newFakeStaging(t)
	ffmpeg, runs := fakeFFmpeg(t)
	h := nodesourcetest.Relayed(t, writeSource(t))
	session, err := StartTranscode(t.Context(), preparingOpts(t, f, h, ffmpeg))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if !session.IsRunning() || !session.SegmentProgress(time.Now()).Preparing || len(ffmpegRuns(t, runs)) != 0 {
		t.Fatal("a start whose subtitle stages is not preparing")
	}
	if _, err := session.GetManifest(); !errors.Is(err, ErrManifestNotReady) {
		t.Fatalf("manifest while preparing = %v", err)
	}
	if decision := session.SegmentRecoveryDecision(40, time.Now()); !decision.Wait || decision.RestartOnTimeout || decision.Reason != "transcode_preparing" {
		t.Fatalf("segment decision while preparing = %+v", decision)
	}
	if err := session.Restart(t.Context(), 30, 15); err != nil {
		t.Fatal(err)
	}
	if !session.SegmentProgress(time.Now()).Preparing || len(ffmpegRuns(t, runs)) != 0 {
		t.Fatal("a restart while preparing started FFmpeg without its subtitle")
	}
	close(f.release)
	waitFor(t, func() bool { return len(ffmpegRuns(t, runs)) == 1 && !session.SegmentProgress(time.Now()).Preparing })
	args := ffmpegRuns(t, runs)[0]
	if !strings.Contains(args, "-ss 30") || !strings.Contains(args, stagedSubtitleFile+"':si=0:fontsdir=") {
		t.Fatalf("launched ffmpeg %s", args)
	}
	if !session.IsRunning() || f.runs.Load() != 1 {
		t.Fatalf("running %t after %d stagings", session.IsRunning(), f.runs.Load())
	}
	// A later restart reuses the staging and starts FFmpeg at once.
	if err := session.Restart(t.Context(), 0, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(ffmpegRuns(t, runs)) == 2 })
	if f.runs.Load() != 1 {
		t.Fatalf("%d stagings after a restart", f.runs.Load())
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	f.cache.mu.Lock()
	defer f.cache.mu.Unlock()
	for _, e := range f.cache.entries {
		if e.leases != 0 {
			t.Fatalf("a closed session holds its staging: %d leases", e.leases)
		}
	}
}

// Closing a preparing session cancels its pending launch: FFmpeg never
// starts, and the staging runs on for the next session.
func TestClosingAPreparingSessionCancelsItsLaunch(t *testing.T) {
	f := newFakeStaging(t)
	ffmpeg, runs := fakeFFmpeg(t)
	h := nodesourcetest.Relayed(t, writeSource(t))
	session, err := StartTranscode(t.Context(), preparingOpts(t, f, h, ffmpeg))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Done():
	default:
		t.Fatal("Close returned with the pending launch alive")
	}
	if session.IsRunning() || !errors.Is(session.WaitError(), context.Canceled) {
		t.Fatalf("closed session: running %t, %v", session.IsRunning(), session.WaitError())
	}
	close(f.release)
	lease, err := acquireStagedSubtitle(t.Context(), f.opts(h, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitStaged(t, lease); err != nil || f.runs.Load() != 1 {
		t.Fatalf("the staging after the close = %v after %d runs", err, f.runs.Load())
	}
	lease.release()
	if got := ffmpegRuns(t, runs); len(got) != 0 {
		t.Fatalf("a closed session started ffmpeg: %q", got)
	}
}

// A staging that fails ends its preparing session with the failure, which
// manifest requests answer as a failed transcode.
func TestPreparingSessionEndsWithItsStagingFailure(t *testing.T) {
	f := newFakeStaging(t)
	f.err = errors.New("no such subtitle track")
	ffmpeg, runs := fakeFFmpeg(t)
	h := nodesourcetest.Relayed(t, writeSource(t))
	session, err := StartTranscode(t.Context(), preparingOpts(t, f, h, ffmpeg))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	close(f.release)
	waitFor(t, func() bool { return !session.IsRunning() })
	if !errors.Is(session.WaitError(), ErrSubtitleStaging) {
		t.Fatalf("session ended with %v", session.WaitError())
	}
	if _, err := session.GetManifest(); !errors.Is(err, ErrTranscodeFailed) {
		t.Fatalf("manifest = %v, want ErrTranscodeFailed", err)
	}
	if got := ffmpegRuns(t, runs); len(got) != 0 {
		t.Fatalf("ffmpeg ran without its subtitle: %q", got)
	}
	// A start within the failure's time answers it at once.
	if _, err := StartTranscode(t.Context(), preparingOpts(t, f, h, ffmpeg)); !errors.Is(err, ErrSubtitleStaging) {
		t.Fatalf("start after the failure = %v", err)
	}
}

// Close and a concurrent restart, in either order: once Close returns, no
// generation runs, and a pending launch never starts FFmpeg. A restart that
// comes after Close has begun is refused.
func TestCloseDuringARestartLeavesNothingRunning(t *testing.T) {
	ffmpeg, runs := fakeFFmpeg(t)
	for _, preparing := range []bool{true, false} {
		for i := range 40 {
			f := newFakeStaging(t)
			h := nodesourcetest.Relayed(t, writeSource(t))
			opts := preparingOpts(t, f, h, ffmpeg)
			if !preparing {
				close(f.release)
				lease, err := acquireStagedSubtitle(t.Context(), opts)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := waitStaged(t, lease); err != nil {
					t.Fatal(err)
				}
				lease.release()
			}
			session, err := StartTranscode(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			restarted := make(chan error, 1)
			go func() { restarted <- session.Restart(t.Context(), 10, 5) }()
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			restartErr := <-restarted
			select {
			case <-session.Done():
			default:
				t.Fatalf("preparing %t, iteration %d: Close returned with a generation alive (restart: %v)", preparing, i, restartErr)
			}
			if session.IsRunning() {
				t.Fatalf("preparing %t, iteration %d: a closed session runs (restart: %v)", preparing, i, restartErr)
			}
			if preparing {
				before := len(ffmpegRuns(t, runs))
				close(f.release)
				lease, err := acquireStagedSubtitle(t.Context(), opts)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := waitStaged(t, lease); err != nil {
					t.Fatal(err)
				}
				lease.release()
				if launching := goroutinesNamed("launchWhenStaged"); launching != 0 || len(ffmpegRuns(t, runs)) != before {
					t.Fatalf("iteration %d: a closed session's launch is pending (%d) or started FFmpeg", i, launching)
				}
			}
		}
	}
}

// goroutinesNamed counts the goroutines whose stack runs fn.
func goroutinesNamed(fn string) int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	count := 0
	for _, stack := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(stack, "."+fn+"(") {
			count++
		}
	}
	return count
}

// writeOldGeneration leaves a playable playlist and its segments in dir, as
// an earlier session reusing the output directory would.
func writeOldGeneration(t *testing.T, dir string) {
	t.Helper()
	manifest := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n"
	for i := range 4 {
		name := fmt.Sprintf("seg_%05d.ts", i)
		manifest += "#EXTINF:2.000000,\n" + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old segment"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A preparing generation has started no FFmpeg: a playlist an earlier
// generation left in its reused directory is not its readiness, which under
// hw_accel=auto would count as a hardware success. That holds for a start
// and for a restart that has to wait for the staging.
func TestPreparingGenerationIgnoresAnEarlierPlaylist(t *testing.T) {
	f := newFakeStaging(t)
	ffmpeg, _ := fakeFFmpeg(t)
	h := nodesourcetest.Relayed(t, writeSource(t))
	opts := preparingOpts(t, f, h, ffmpeg)
	writeOldGeneration(t, opts.OutputDir)
	session, err := StartTranscode(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if _, err := session.WaitForGenerationManifestContext(t.Context(), 300*time.Millisecond); !errors.Is(err, ErrManifestNotReady) {
		t.Fatalf("readiness of a preparing start = %v, want a timeout", err)
	}

	// A restart while preparing writes into the same directory.
	if err := session.Restart(t.Context(), 0, 0); err != nil {
		t.Fatal(err)
	}
	writeOldGeneration(t, opts.OutputDir)
	if _, err := session.WaitForGenerationManifestContext(t.Context(), 300*time.Millisecond); !errors.Is(err, ErrManifestNotReady) {
		t.Fatalf("readiness of a preparing restart = %v, want a timeout", err)
	}
}

// A preparing session holds no GPU reservation: its device is chosen at the
// start but counted only once FFmpeg launches, and released with it.
func TestPreparingSessionReservesItsGPUAtLaunch(t *testing.T) {
	f := newFakeStaging(t)
	ffmpeg, runs := fakeFFmpeg(t)
	h := nodesourcetest.Relayed(t, writeSource(t))
	opts := preparingOpts(t, f, h, ffmpeg)
	device := "GPU-preparing-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	opts.HWAccel, opts.HWDevice = transcodeHWNVENC, device
	session, err := StartTranscode(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if !session.SegmentProgress(time.Now()).Preparing {
		t.Fatal("the session is not preparing")
	}
	if n := HWDeviceLoadSnapshot()[device]; n != 0 {
		t.Fatalf("a preparing session holds %d reservations", n)
	}
	if session.Opts().HWDevice != device {
		t.Fatalf("the preparing session's device is %q", session.Opts().HWDevice)
	}
	// A restart while preparing reserves nothing either.
	if err := session.Restart(t.Context(), 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := HWDeviceLoadSnapshot()[device]; n != 0 {
		t.Fatalf("a preparing restart holds %d reservations", n)
	}
	close(f.release)
	waitFor(t, func() bool { return len(ffmpegRuns(t, runs)) == 1 && HWDeviceLoadSnapshot()[device] == 1 })
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if n := HWDeviceLoadSnapshot()[device]; n != 0 {
		t.Fatalf("a closed session holds %d reservations", n)
	}
}

// A readiness-gated start whose staging outlasts its readiness wait fails as
// any start not ready in time does (a TranscodeStartupError of a running
// attempt, which no pipeline takes for a hardware failure). The staging is
// neither canceled nor failed by it, and a retry once it completes is ready.
func TestReadinessTimeoutWhilePreparingKeepsTheStaging(t *testing.T) {
	f := newFakeStaging(t)
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\n" +
		"printf '#EXTM3U\\n#EXT-X-VERSION:3\\n#EXT-X-TARGETDURATION:2\\n#EXT-X-MEDIA-SEQUENCE:0\\n' > stream.tmp\n" +
		"for i in 0 1 2 3; do printf 'segment' > seg_0000$i.ts; printf '#EXTINF:2.000000,\\nseg_0000%s.ts\\n' $i >> stream.tmp; done\n" +
		"mv stream.tmp stream.m3u8\nexec sleep 60\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h := nodesourcetest.Relayed(t, writeSource(t))
	opts := preparingOpts(t, f, h, ffmpeg)
	start := func(timeout time.Duration) (*TranscodeSession, error) {
		return StartReadyTranscode(t.Context(), NewAutoTranscodePipeline(t.Context(), opts), TranscodeStartup{Timeout: timeout})
	}
	_, err := start(200 * time.Millisecond)
	var startupErr *TranscodeStartupError
	if !errors.As(err, &startupErr) || !startupErr.WasRunning {
		t.Fatalf("a start whose staging outlasts its readiness = %v, want a running attempt's TranscodeStartupError", err)
	}
	lease, err := acquireStagedSubtitle(t.Context(), opts)
	if err != nil || lease.e.closed() || f.runs.Load() != 1 {
		t.Fatalf("the staging after the failed start = %v, ended %t after %d runs", err, lease.e.closed(), f.runs.Load())
	}
	close(f.release)
	if _, err := waitStaged(t, lease); err != nil {
		t.Fatalf("the staging = %v", err)
	}
	lease.release()
	session, err := start(10 * time.Second)
	if err != nil {
		t.Fatalf("a retry once the staging completed = %v", err)
	}
	defer func() { _ = session.Close() }()
	if f.runs.Load() != 1 {
		t.Fatalf("%d stagings after the retry", f.runs.Load())
	}
}
