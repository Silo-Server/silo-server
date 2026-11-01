package keptsub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestWholeTrackKeepsWhatItReads(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, blobs := newTestStore(t, ffmpeg)
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)
	want := demux(t, ffmpeg, path, 0, 0, 0)

	in, err := s.WholeTrack(t.Context(), path, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got := convert(t, ffmpeg, in); got != want {
		t.Fatalf("kept track converts to %q, want %q", got, want)
	}
	kept, _ := repo.Track(t.Context(), file.ID, 1)
	if kept == nil || kept.Method != MethodCues || kept.Packets != 3 || kept.HasOverrideTags == nil || !*kept.HasOverrideTags ||
		kept.StreamFormat != "matroska" || !strings.HasPrefix(kept.BlobKey, "subtitles/embedded/41/1-") || blobs.objects[kept.BlobKey] == nil {
		t.Fatalf("kept row = %+v", kept)
	}

	// Again: the kept track, from the local copy, without a read or a fetch.
	if _, err := s.WholeTrack(t.Context(), path, ref); err != nil {
		t.Fatal(err)
	}
	if puts, claims := repo.counts(); puts != 1 || claims != 1 || blobs.gets != 0 {
		t.Fatalf("second request: %d keeps, %d reads, %d fetches", puts, claims, blobs.gets)
	}

	// Another node: no local copy, so one fetch, checked against the hash.
	if err := os.RemoveAll(s.mirrorDir()); err != nil {
		t.Fatal(err)
	}
	in, err = s.WholeTrack(t.Context(), path, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got := convert(t, ffmpeg, in); got != want || blobs.gets != 1 {
		t.Fatalf("after a fetch (%d): %q", blobs.gets, got)
	}
	original := blobs.objects[kept.BlobKey]
	for _, tc := range []struct {
		object []byte
		want   string
	}{
		{bytes.Repeat([]byte("x"), len(original)), "SHA-256"},
		{original[:len(original)-1], "fewer than"},
	} {
		blobs.objects[kept.BlobKey] = tc.object
		if err := os.RemoveAll(s.mirrorDir()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.WholeTrack(t.Context(), path, ref); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("a tampered object was used: %v", err)
		}
	}
}

func TestKeptTrackFollowsTheSourceVersion(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, blobs := newTestStore(t, ffmpeg)
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)
	if _, err := s.WholeTrack(t.Context(), path, ref); err != nil {
		t.Fatal(err)
	}
	first, _ := repo.Track(t.Context(), file.ID, 1)

	// The file changes: the kept track is stale, so a window reads the
	// source and the next whole-track request reads it again.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Window(t.Context(), path, ref, 0, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if puts, _ := repo.counts(); puts != 1 {
		t.Fatal("a window kept the track")
	}
	if _, err := s.WholeTrack(t.Context(), path, ref); err != nil {
		t.Fatal(err)
	}
	second, _ := repo.Track(t.Context(), file.ID, 1)
	if second.SourceModTime != later.UnixNano() || second.SourceModTime == first.SourceModTime {
		t.Fatalf("kept row after the change = %+v", second)
	}

	// A row whose object is gone is read again.
	delete(blobs.objects, second.BlobKey)
	if err := os.RemoveAll(s.mirrorDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WholeTrack(t.Context(), path, ref); err != nil {
		t.Fatal(err)
	}
	if puts, _ := repo.counts(); puts != 3 || blobs.objects[second.BlobKey] == nil {
		t.Fatalf("missing object: %d keeps", puts)
	}

	// A track the catalog describes differently isn't this one.
	other := ref
	other.ContainerTrackID = "9"
	if _, err := s.WholeTrack(t.Context(), path, other); !errors.Is(err, embeddedsub.ErrTrackNotFound) {
		t.Fatalf("another track id: %v", err)
	}
}

func TestConcurrentRequestsShareOneRead(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, _ := newTestStore(t, ffmpeg)
	ref := *playback.EmbeddedSubtitleRefFor(file, 1)
	// Hold every request at its first lookup until all have arrived.
	const requests = 8
	var arrived sync.WaitGroup
	arrived.Add(requests)
	var once sync.Once
	release := make(chan struct{})
	repo.trackHook = func(context.Context) error {
		once.Do(func() {
			go func() {
				arrived.Wait()
				close(release)
			}()
		})
		select {
		case <-release:
		default:
			arrived.Done()
			<-release
		}
		return nil
	}
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			if _, err := s.WholeTrack(t.Context(), path, ref); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if puts, claims := repo.counts(); puts != 1 || claims != 1 {
		t.Fatalf("%d requests made %d reads and %d keeps", requests, claims, puts)
	}
}

func TestReadsWaitForAnotherServer(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)

	// The other server's read is the same read: this store's own result,
	// recorded under the other's claim.
	other, _, otherBlobs := newTestStore(t, ffmpeg)
	if _, err := other.WholeTrack(t.Context(), path, ref); err != nil {
		t.Fatal(err)
	}
	keptElsewhere, _ := other.repo.Track(t.Context(), file.ID, 1)

	s, repo, blobs := newTestStore(t, ffmpeg)
	blobs.objects = otherBlobs.objects
	repo.claims[trackKey{file.ID, 1}] = memClaim{token: "other", expires: time.Now().Add(time.Hour)}

	// A prefetch gives way.
	if err := s.Prefetch(t.Context(), path, ref); !errors.Is(err, errClaimedElsewhere) {
		t.Fatalf("prefetch under another claim: %v", err)
	}
	// A request waits for the other server's track: it lands once the
	// request has polled for it.
	go func() {
		waitFor(t, "the request to wait", func() bool {
			_, claims := repo.counts()
			return claims >= 3
		})
		_ = repo.PutTrack(context.Background(), *keptElsewhere)
	}()
	in, err := s.WholeTrack(t.Context(), path, ref)
	if err != nil {
		t.Fatal(err)
	}
	if puts, _ := repo.counts(); puts != 1 || convert(t, ffmpeg, in) != demux(t, ffmpeg, path, 0, 0, 0) {
		t.Fatalf("waited request kept %d tracks itself", puts-1)
	}
	// A claim that runs out is taken over.
	repo.claims[trackKey{file.ID, 2}] = memClaim{token: "dead", expires: time.Now().Add(30 * time.Millisecond)}
	if _, err := s.WholeTrack(t.Context(), path, *playback.EmbeddedSubtitleRefFor(file, 1)); err != nil {
		t.Fatal(err)
	}
}

// Windows never run FFmpeg on the source: they come from the kept track,
// else from the index. A window past a sparse track's last cue is empty
// instead of a read to the end of the file (#1406).
func TestWindows(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, _ := newTestStore(t, ffmpeg)
	forced := *playback.EmbeddedSubtitleRefFor(file, 1)
	// A window's read holds at most windowMaxBytes; past it the read
	// refuses, and the subtitle cache demuxes instead.
	overBudget := func(where string) {
		t.Helper()
		windowMaxBytes = 1
		defer func() { windowMaxBytes = embeddedsub.WindowMaxBytes }()
		if _, err := s.Window(t.Context(), path, forced, 5*time.Second, 8*time.Second); !errors.Is(err, embeddedsub.ErrTooLarge) {
			t.Fatalf("window over its budget %s: err = %v", where, err)
		}
	}
	overBudget("from the index")
	fromIndex, err := s.Window(t.Context(), path, forced, 5*time.Second, 8*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if fromIndex.Index == nil || len(fromIndex.Packets) != 1 || filepath.Clean(fromIndex.Index.Tracks[0].CodecID) != "S_TEXT/UTF8" {
		t.Fatalf("window from the index = %+v", fromIndex)
	}
	if _, err := s.WholeTrack(t.Context(), path, forced); err != nil {
		t.Fatal(err)
	}
	fromKept, err := s.Window(t.Context(), path, forced, 5*time.Second, 8*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromKept.Packets) != 1 || fromKept.Index.Size == fromIndex.Index.Size {
		t.Fatalf("window from the kept track = %d packets from a %d byte file", len(fromKept.Packets), fromKept.Index.Size)
	}
	overBudget("from the kept track")
	if got, want := convert(t, ffmpeg, fromKept), convert(t, ffmpeg, fromIndex); got != want || !strings.Contains(got, "Noch einer") {
		t.Fatalf("kept window %q, index window %q", got, want)
	}
	empty, err := s.Window(t.Context(), path, forced, 8*time.Second, 11*time.Second)
	if err != nil || len(empty.Packets) != 0 || convert(t, ffmpeg, empty) != "WEBVTT\n" {
		t.Fatalf("empty window: %+v, %v", empty, err)
	}
	if puts, _ := repo.counts(); puts != 1 {
		t.Fatalf("windows kept %d tracks", puts-1)
	}
	// A window open to the end reads the track whole and keeps it.
	english := *playback.EmbeddedSubtitleRefFor(file, 0)
	open, err := s.Window(t.Context(), path, english, 2500*time.Millisecond, embeddedsub.WholeTo)
	if err != nil || len(open.Packets) != 2 {
		t.Fatalf("open window: %+v, %v", open, err)
	}
	if kept, _ := repo.Track(t.Context(), file.ID, 1); kept == nil {
		t.Fatal("an open window didn't keep the track")
	}
}

// A file without cues is demuxed once; the copy that demux kept serves
// whole tracks and windows after that.
func TestDemuxedCopyServesFilesWithoutCues(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, false)
	s, repo, _ := newTestStore(t, ffmpeg)
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)
	if _, err := s.WholeTrack(t.Context(), path, ref); !errors.Is(err, embeddedsub.ErrNoCues) {
		t.Fatalf("whole track without cues: %v", err)
	}
	if _, err := s.Window(t.Context(), path, ref, 0, 5*time.Second); !errors.Is(err, embeddedsub.ErrNoCues) {
		t.Fatalf("window without cues: %v", err)
	}
	demuxCopy, err := s.DemuxCopy(t.Context(), path, ref)
	if err != nil || demuxCopy.Format != "matroska" {
		t.Fatalf("demux copy = %+v, %v", demuxCopy, err)
	}
	demuxCopy.Path = filepath.Join(t.TempDir(), "kept.part-1")
	if err := os.WriteFile(demuxCopy.Path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	var served strings.Builder
	if err := playback.StreamExtractSubtitle(t.Context(), playback.StreamExtractOpts{
		InputPath: path, SourceCodec: "subrip", FFmpegPath: ffmpeg, Writer: &served,
		KeepPath: demuxCopy.Path, KeepFormat: demuxCopy.Format, KeepOffset: demuxCopy.StartTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.KeepDemuxed(t.Context(), path, ref, demuxCopy, info.Size(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(demuxCopy.Path); !os.IsNotExist(err) {
		t.Fatalf("the copy was copied, not moved, to the local copies: %v", err)
	}
	kept, _ := repo.Track(t.Context(), file.ID, 1)
	if kept == nil || kept.Method != MethodFFmpeg || kept.Completeness != CompletenessComplete || kept.HasOverrideTags == nil || !*kept.HasOverrideTags {
		t.Fatalf("kept row = %+v", kept)
	}
	in, err := s.WholeTrack(t.Context(), path, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got := convert(t, ffmpeg, in); got != served.String() {
		t.Fatalf("kept copy converts to %q, the demux served %q", got, served.String())
	}
	window, err := s.Window(t.Context(), path, ref, 2500*time.Millisecond, 5*time.Second)
	if err != nil || len(window.Packets) != 1 {
		t.Fatalf("window from the kept copy: %+v, %v", window, err)
	}
	// A track the scanner recorded no ID for is kept by its demux too.
	noID := ref
	noID.ContainerTrackID = ""
	demuxCopy.Path = filepath.Join(t.TempDir(), "kept.part-2")
	if err := playback.StreamExtractSubtitle(t.Context(), playback.StreamExtractOpts{
		InputPath: path, SourceCodec: "subrip", FFmpegPath: ffmpeg, Writer: &strings.Builder{},
		KeepPath: demuxCopy.Path, KeepFormat: demuxCopy.Format, KeepOffset: demuxCopy.StartTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.KeepDemuxed(t.Context(), path, noID, demuxCopy, info.Size(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WholeTrack(t.Context(), path, noID); err != nil {
		t.Fatalf("kept track without an ID: %v", err)
	}
	// A copy made while the source changed is never kept.
	demuxCopy.Path = filepath.Join(t.TempDir(), "kept.part-3")
	if err := playback.StreamExtractSubtitle(t.Context(), playback.StreamExtractOpts{
		InputPath: path, SourceCodec: "subrip", FFmpegPath: ffmpeg, Writer: &strings.Builder{},
		KeepPath: demuxCopy.Path, KeepFormat: demuxCopy.Format, KeepOffset: demuxCopy.StartTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.KeepDemuxed(t.Context(), path, ref, demuxCopy, info.Size()+1, info.ModTime()); !errors.Is(err, errSourceChanged) {
		t.Fatalf("changed source: %v", err)
	}
}

func TestKeepDemuxedRejectsOversizedCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.mkv")
	if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.mks")
	f, err := os.Create(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := embeddedsub.WriteKept(f, &embeddedsub.Index{Container: embeddedsub.ContainerMatroska, TimestampScale: 1_000_000},
		&embeddedsub.Track{Number: 1, CodecID: "S_HDMV/PGS", Enabled: true},
		[]embeddedsub.Packet{{PTS: time.Second, Duration: time.Second, Data: []byte("packet")}}); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Truncate(playback.MaxDemuxCopyBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	s, repo, blobs := newTestStore(t, "unused-ffmpeg")
	err = s.KeepDemuxed(t.Context(), path, playback.EmbeddedSubtitleRef{MediaFileID: 41, StreamIndex: 1, Codec: "hdmv_pgs_subtitle"},
		playback.DemuxCopy{Path: copyPath, Format: "matroska"}, info.Size(), info.ModTime())
	if !errors.Is(err, embeddedsub.ErrTooLarge) {
		t.Fatalf("oversized copy: %v, want ErrTooLarge", err)
	}
	if puts, _ := repo.counts(); puts != 0 || len(blobs.objects) != 0 {
		t.Fatal("oversized copy was persisted")
	}
	entries, err := os.ReadDir(s.mirrorDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("oversized copy left a mirror")
	}
}

func TestSubtitleTrackFacts(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, _ := newTestStore(t, ffmpeg)
	facts := s.SubtitleTrackFacts(t.Context(), file)
	if repo.fileTrackCalls != 0 {
		t.Fatal("facts were looked up before planning asked")
	}
	if got := facts.OverrideTags(file, file.SubtitleTracks[0]); got != playback.SubtitleFactUnknownV3 {
		t.Fatalf("unread track: %v", got)
	}
	for ordinal := range file.SubtitleTracks {
		if _, err := s.WholeTrack(t.Context(), path, *playback.EmbeddedSubtitleRefFor(file, ordinal)); err != nil {
			t.Fatal(err)
		}
	}
	// Planning looks once per plan; a new plan sees the new facts.
	repo.fileTrackCalls = 0
	facts = s.SubtitleTrackFacts(t.Context(), file)
	for ordinal, want := range []playback.SubtitleFactV3{playback.SubtitleFactYesV3, playback.SubtitleFactNoV3} {
		if got := facts.OverrideTags(file, file.SubtitleTracks[ordinal]); got != want {
			t.Errorf("track %d: %v, want %v", ordinal, got, want)
		}
	}
	if repo.fileTrackCalls != 1 {
		t.Fatalf("%d lookups for one plan", repo.fileTrackCalls)
	}
	changed := *file
	changed.FileSize++
	if got := s.SubtitleTrackFacts(t.Context(), &changed).OverrideTags(&changed, changed.SubtitleTracks[0]); got != playback.SubtitleFactUnknownV3 {
		t.Fatalf("facts from another version of the file: %v", got)
	}
	changed = *file
	changed.FileModifiedAt = nil
	if got := s.SubtitleTrackFacts(t.Context(), &changed).OverrideTags(&changed, changed.SubtitleTracks[0]); got != playback.SubtitleFactUnknownV3 {
		t.Fatalf("facts without catalog mtime: %v", got)
	}
	changed = *file
	later := file.FileModifiedAt.Add(time.Second)
	changed.FileModifiedAt = &later
	if got := s.SubtitleTrackFacts(t.Context(), &changed).OverrideTags(&changed, changed.SubtitleTracks[0]); got != playback.SubtitleFactUnknownV3 {
		t.Fatalf("facts from an older same-size file: %v", got)
	}
}

// A request stops waiting on a slow read and demuxes instead; the read goes
// on and keeps the track for the next request.
func TestSlowReadsHandOverToTheDemux(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, _ := newTestStore(t, ffmpeg)
	s.foregroundWait = 20 * time.Millisecond
	// The read stalls, once started, until released.
	release := make(chan struct{})
	stallStartedRead(s, release)
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)
	if _, err := s.WholeTrack(t.Context(), path, ref); !errors.Is(err, errStillReading) {
		t.Fatalf("slow read: %v", err)
	}
	close(release)
	waitFor(t, "the read to keep the track", func() bool {
		tr, _ := repo.Track(t.Context(), file.ID, 1)
		return tr != nil
	})
}

// A storage budget must not prevent a freshly read track from reaching
// durable storage before its local mirror becomes eligible for eviction.
func TestOversizedMirrorStillKeepsTrack(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, _, _ := newTestStore(t, ffmpeg)
	s.opts.MirrorMaxBytes = 1
	in, err := s.WholeTrack(t.Context(), path, *playback.EmbeddedSubtitleRefFor(file, 0))
	if err != nil {
		t.Fatalf("newly read track could not reach storage: %v", err)
	}
	stream, err := in.Stream(t.Context())
	if err != nil {
		t.Fatalf("oversized stored track could not be consumed: %v", err)
	}
	defer func() { _ = stream.Close() }()
	data, err := io.ReadAll(stream)
	if err != nil || len(data) == 0 {
		t.Fatalf("read stored track: %d bytes, %v", len(data), err)
	}
}

type delayedQueueRepo struct {
	Repo
	first   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (r *delayedQueueRepo) QueueCollection(ctx context.Context, key string, after time.Duration) error {
	if r.first.CompareAndSwap(false, true) {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.Repo.QueueCollection(ctx, key, after)
}

// A second whole read must not evict the first read's mirror while the
// first read is still waiting to persist it.
func TestPendingKeepSurvivesConcurrentMirrorWrite(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, _ := newTestStore(t, ffmpeg)
	s.opts.MirrorMaxBytes = 300 // each fixture track fits; both together do not
	delayed := &delayedQueueRepo{Repo: repo, entered: make(chan struct{}), release: make(chan struct{})}
	s.repo = delayed
	first := make(chan error, 1)
	go func() {
		_, err := s.WholeTrack(t.Context(), path, *playback.EmbeddedSubtitleRefFor(file, 0))
		first <- err
	}()
	select {
	case <-delayed.entered:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	_, secondErr := s.WholeTrack(t.Context(), path, *playback.EmbeddedSubtitleRefFor(file, 1))
	close(delayed.release)
	if secondErr != nil {
		t.Fatal(secondErr)
	}
	if err := <-first; err != nil {
		t.Fatalf("another mirror write broke an active keep: %v", err)
	}
}
