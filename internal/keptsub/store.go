// Package keptsub keeps the embedded subtitle tracks the server reads whole,
// in the assets blob store under subtitles/embedded/, so that reading a
// track costs the source file once: later requests, on any node, convert
// the small kept stream instead. Store serves the subtitle cache in place of
// the source (playback.EmbeddedSubtitleSource) and answers what reading a
// track taught the server (SubtitleTrackFacts); Prefetcher reads the tracks
// a playback is likely to want next, ahead of time.
//
// See docs/architecture/embedded-subtitle-index.md.
package keptsub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// Blobs is the part of the assets store kept tracks use.
type Blobs interface {
	PutStream(ctx context.Context, key string, r io.Reader, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error)
}

const (
	// keyRoot is the assets store prefix kept tracks live under.
	keyRoot = "subtitles/embedded/"
	// wholeReadTimeout bounds reading a whole track through the source's
	// index: tens of seconds on slow network storage. A read that takes
	// longer gives way to the demux, which streams cues as it goes.
	wholeReadTimeout = 3 * time.Minute
	// keepTimeout bounds storing what a read kept.
	keepTimeout = time.Minute
	// claimLease outlasts the read and the keep it covers, so a claim never
	// runs out under a read that is still going.
	claimLease = wholeReadTimeout + keepTimeout + time.Minute
	// wholeReads bounds the whole reads running on a node: each can hold a
	// track's packets in memory, up to keptMaxBytes.
	wholeReads = 4
	// keptMaxBytes bounds the packets one whole read holds. It is above
	// nearly every film's PGS track; a larger track is demuxed instead.
	keptMaxBytes = 128 << 20
	// orphanGrace is when the collector checks a stored object no row may
	// name: one stored just before its row failed to land.
	orphanGrace = 24 * time.Hour
	// foregroundWait is how long a request waits on a whole read before it
	// demuxes the source instead, which streams its first cues at once. It
	// sits well inside the response's write stall window
	// (httpstream.DefaultStallWindow), which runs from the request's start
	// until the first byte. The read goes on, and keeps the track.
	foregroundWait = 90 * time.Second
	// defaultPollInterval is how often a request waiting on another
	// server's read looks for its result.
	defaultPollInterval = time.Second
	// startTimesKept bounds the source start times remembered.
	startTimesKept = 4096
)

var (
	errNotServable      = errors.New("keptsub: track codec isn't read through the index")
	errNoTrackID        = errors.New("keptsub: track has no container track ID")
	errCodecMismatch    = errors.New("keptsub: container track doesn't match the catalog's codec")
	errNoPackets        = errors.New("keptsub: track has no packets")
	errClaimedElsewhere = errors.New("keptsub: another server is reading the track")
	errSourceChanged    = errors.New("keptsub: source changed since the demux began")
)

// windowMaxBytes bounds the packets one window's read holds; a window too
// large for it is demuxed by the subtitle cache instead.
var windowMaxBytes int64 = embeddedsub.WindowMaxBytes

// windowBudget is the MaxBytes of a window ending at to. A window open to
// the end is most of the track, so it gets a whole read's budget.
func windowBudget(to time.Duration) int64 {
	if to == embeddedsub.WholeTo {
		return keptMaxBytes
	}
	return windowMaxBytes
}

// Options configures a Store.
type Options struct {
	// FFmpegPath returns the configured ffmpeg binary; ffprobe is found
	// next to it. Nil means "ffmpeg".
	FFmpegPath func() string
	// MirrorDir returns the node-local directory kept streams are copied
	// to, under the transcode directory. Empty disables the copies, and
	// with them kept tracks, on this node.
	MirrorDir func() string
	// MirrorMaxBytes bounds the local copies; 0 means 1 GiB.
	MirrorMaxBytes int64
}

// Store keeps tracks and reads them back. It is safe for concurrent use.
type Store struct {
	repo    Repo
	blobs   Blobs
	indexes *embeddedsub.Cache
	opts    Options
	// probeStart returns a source's start time; tests replace it.
	probeStart   func(ctx context.Context, path string) (time.Duration, error)
	pollInterval time.Duration
	// foregroundWait is foregroundWait; tests shorten it.
	foregroundWait time.Duration

	// reads holds a slot per whole read running (wholeReads).
	reads chan struct{}

	mu               sync.Mutex
	flights          map[flightKey]*flight
	starts           map[sourceVersion]time.Duration
	mirrors          map[string]*mirrorFetch
	mirrorUsed       map[string]time.Time
	mirrorPins       map[string]int
	mirrorOverBudget map[string]bool
}

// NewStore returns a Store over repo and blobs, or nil without either.
func NewStore(repo Repo, blobs Blobs, opts Options) *Store {
	if repo == nil || blobs == nil {
		return nil
	}
	if opts.MirrorMaxBytes <= 0 {
		opts.MirrorMaxBytes = defaultMirrorMaxBytes
	}
	s := &Store{
		repo:         repo,
		blobs:        blobs,
		indexes:      playback.SubtitleIndexes(),
		opts:         opts,
		pollInterval: defaultPollInterval,
		flights:      map[flightKey]*flight{},
		// A request's wait on a whole read; see foregroundWait.
		foregroundWait:   foregroundWait,
		starts:           map[sourceVersion]time.Duration{},
		mirrors:          map[string]*mirrorFetch{},
		mirrorUsed:       map[string]time.Time{},
		mirrorPins:       map[string]int{},
		mirrorOverBudget: map[string]bool{},
		reads:            make(chan struct{}, wholeReads),
	}
	s.probeStart = func(ctx context.Context, path string) (time.Duration, error) {
		return playback.SubtitleSourceStartTime(ctx, s.ffmpegPath(), path)
	}
	return s
}

var _ playback.EmbeddedSubtitleSource = (*Store)(nil)

func (s *Store) ffmpegPath() string {
	if s.opts.FFmpegPath != nil {
		if p := s.opts.FFmpegPath(); p != "" {
			return p
		}
	}
	return "ffmpeg"
}

// sourceVersion names a version of a source file.
type sourceVersion struct {
	path  string
	size  int64
	mtime int64
}

func versionOf(path string, info os.FileInfo) sourceVersion {
	return sourceVersion{path: path, size: info.Size(), mtime: info.ModTime().UnixNano()}
}

// matches reports whether t was read from this version of the source, for
// the track ref names.
func (t *Track) matches(v sourceVersion, ref playback.EmbeddedSubtitleRef) bool {
	return t != nil && t.SourceSize == v.size && t.SourceModTime == v.mtime &&
		t.ContainerTrackID == ref.ContainerTrackID && codecFamily(t.Codec) == codecFamily(ref.Codec)
}

// WholeTrack returns ref's track kept, or read whole through the source's
// index and kept.
func (s *Store) WholeTrack(ctx context.Context, path string, ref playback.EmbeddedSubtitleRef) (*playback.SubtitlePacketInput, error) {
	for attempt := 0; ; attempt++ {
		t, err := s.ensure(ctx, path, ref, false)
		if err != nil {
			return nil, err
		}
		_, release, err := s.mirror(ctx, t)
		if errors.Is(err, blobstore.ErrNotFound) && attempt == 0 {
			// The row outlived its object (a storage move that left the
			// objects behind): read the track again.
			slog.WarnContext(ctx, "kept subtitle track missing from storage; reading it again", "component", "keptsub",
				"file_id", t.MediaFileID, "stream_index", t.StreamIndex, "key", t.BlobKey)
			if err := s.repo.DeleteTrack(ctx, t.MediaFileID, t.StreamIndex, t.BlobKey); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		in := s.streamInput(ctx, t)
		release()
		return in, nil
	}
}

// streamInput feeds FFmpeg the local copy of t, fetching it again
// should it have been evicted since.
func (s *Store) streamInput(ctx context.Context, t *Track) *playback.SubtitlePacketInput {
	return &playback.SubtitlePacketInput{
		StreamFormat:    t.StreamFormat,
		SourceStartTime: t.StartTime,
		ExpectCues:      t.Packets > 0,
		Stream: func() (io.ReadCloser, error) {
			again, release, err := s.mirror(ctx, t)
			if err != nil {
				return nil, err
			}
			defer release()
			return os.Open(again) //nolint:gosec // G304: as above
		},
	}
}

// Window returns the packets of ref's track that show in [from, to) on the
// playback timeline: from the kept track when there is one, else read
// through the source's index. A window open to the end is most of the
// track, so it is read whole and kept first.
func (s *Store) Window(ctx context.Context, path string, ref playback.EmbeddedSubtitleRef, from, to time.Duration) (*playback.SubtitlePacketInput, error) {
	if !servable(ref.Codec) {
		return nil, errNotServable
	}
	info, err := fspath.StatContext(ctx, path)
	if err != nil {
		return nil, err
	}
	v := versionOf(path, info)
	t, err := s.repo.Track(ctx, ref.MediaFileID, ref.StreamIndex)
	switch {
	case err != nil:
		slog.WarnContext(ctx, "kept subtitle track lookup failed", "component", "keptsub", "file_id", ref.MediaFileID, "error", err)
	case t.matches(v, ref):
		in, err := s.keptWindow(ctx, t, from, to)
		if err == nil || ctx.Err() != nil {
			return in, err
		}
		slog.WarnContext(ctx, "subtitle window from the kept track failed; reading the source", "component", "keptsub",
			"file_id", t.MediaFileID, "stream_index", t.StreamIndex, "error", err)
	case to == embeddedsub.WholeTo:
		// A window open to the end reads most of the track: read it whole,
		// keep it, and cut the window from that.
		t, err := s.ensure(ctx, path, ref, false)
		if err != nil {
			return nil, err
		}
		return s.keptWindow(ctx, t, from, to)
	}
	return s.sourceWindow(ctx, path, ref, v, from, to)
}

// sourceWindow reads a window through the source's index.
func (s *Store) sourceWindow(ctx context.Context, path string, ref playback.EmbeddedSubtitleRef, v sourceVersion, from, to time.Duration) (*playback.SubtitlePacketInput, error) {
	number, err := trackNumber(ref)
	if err != nil {
		return nil, err
	}
	idx, track, err := s.sourceTrack(ctx, path, ref, number)
	if err != nil {
		return nil, err
	}
	start, err := s.startTime(ctx, path, v)
	if err != nil {
		return nil, err
	}
	res, err := embeddedsub.ReadPackets(ctx, path, idx, number, shift(from, start), shift(to, start), embeddedsub.ReadOptions{MaxBytes: windowBudget(to)})
	if err != nil {
		return nil, err
	}
	return &playback.SubtitlePacketInput{Index: idx, Track: track, Packets: res.Packets, SourceStartTime: start}, nil
}

// keptWindow cuts a window from the local copy of t.
func (s *Store) keptWindow(ctx context.Context, t *Track, from, to time.Duration) (*playback.SubtitlePacketInput, error) {
	local, release, err := s.mirror(ctx, t)
	if err != nil {
		return nil, err
	}
	defer release()
	idx, err := s.indexes.Index(ctx, local)
	if err != nil {
		return nil, err
	}
	if len(idx.Tracks) != 1 {
		return nil, fmt.Errorf("%w: kept stream has %d tracks", embeddedsub.ErrMalformed, len(idx.Tracks))
	}
	track := &idx.Tracks[0]
	res, err := embeddedsub.ReadPackets(ctx, local, idx, track.Number, shift(from, t.StartTime), shift(to, t.StartTime), embeddedsub.ReadOptions{MaxBytes: windowBudget(to)})
	if err != nil {
		return nil, err
	}
	return &playback.SubtitlePacketInput{Index: idx, Track: track, Packets: res.Packets, SourceStartTime: t.StartTime}, nil
}

// shift moves a playback-timeline bound onto the container's timeline.
func shift(d, start time.Duration) time.Duration {
	if d == embeddedsub.WholeFrom || d == embeddedsub.WholeTo {
		return d
	}
	return d + start
}

// Prefetch reads ref's track whole and keeps it, unless it is kept
// already, at idle I/O priority. It skips a track another server is
// reading.
func (s *Store) Prefetch(ctx context.Context, path string, ref playback.EmbeddedSubtitleRef) error {
	_, err := s.ensure(ctx, path, ref, true)
	return err
}

// ensure returns ref's kept track for the source's current version, reading
// and keeping it when there is none. Concurrent calls for a track share one
// read. A background call reads at idle priority, gives way to another
// server's read, and stops when its callers go, until a foreground call
// joins it.
func (s *Store) ensure(ctx context.Context, path string, ref playback.EmbeddedSubtitleRef, background bool) (*Track, error) {
	if !servable(ref.Codec) {
		return nil, errNotServable
	}
	if s.mirrorDir() == "" {
		// Kept tracks are served from their local copies.
		return nil, errNoMirrorDir
	}
	info, err := fspath.StatContext(ctx, path)
	if err != nil {
		return nil, err
	}
	v := versionOf(path, info)
	t, err := s.repo.Track(ctx, ref.MediaFileID, ref.StreamIndex)
	if err != nil {
		return nil, err
	}
	if t.matches(v, ref) {
		return t, nil
	}
	// A demux keeps a track the index can't address; reading needs the ID.
	number, err := trackNumber(ref)
	if err != nil {
		return nil, err
	}
	key := flightKey{fileID: ref.MediaFileID, stream: ref.StreamIndex, version: v}
	wait := s.foregroundWait
	if background {
		wait = 0
	}
	read := func(ctx context.Context, f *flight) (*Track, error) {
		return s.readWhole(ctx, f, path, ref, number, v)
	}
	t, err = s.shared(ctx, key, background, wait, read)
	if !background && errors.Is(err, errClaimedElsewhere) {
		// A background read gave way to another server just before this
		// request joined it; a read of the request's own waits instead.
		t, err = s.shared(ctx, key, background, wait, read)
	}
	return t, err
}

// readWhole reads a track whole through the source's index and keeps it.
// Waiting on another server's read and the read itself are bounded
// separately, each by wholeReadTimeout.
func (s *Store) readWhole(ctx context.Context, f *flight, path string, ref playback.EmbeddedSubtitleRef, number uint64, v sourceVersion) (*Track, error) {
	// Another caller may have kept it since this one looked.
	if t, err := s.repo.Track(ctx, ref.MediaFileID, ref.StreamIndex); err == nil && t.matches(v, ref) {
		return t, nil
	}
	token := uuid.NewString()
	claimed, err := s.repo.Claim(ctx, ref.MediaFileID, ref.StreamIndex, token, claimLease)
	if err != nil {
		slog.WarnContext(ctx, "subtitle track claim failed; reading without it", "component", "keptsub",
			"file_id", ref.MediaFileID, "stream_index", ref.StreamIndex, "error", err)
	} else if !claimed {
		if !s.isForeground(f) {
			return nil, errClaimedElsewhere
		}
		t, ok, err := s.waitForOther(ctx, ref, v, token)
		if err != nil || t != nil {
			return t, err
		}
		claimed = ok
	}
	if claimed {
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if err := s.repo.Release(releaseCtx, ref.MediaFileID, ref.StreamIndex, token); err != nil {
				slog.WarnContext(ctx, "subtitle track claim release failed", "component", "keptsub", "error", err)
			}
		}()
	}

	readCtx, cancel := context.WithTimeout(ctx, wholeReadTimeout)
	defer cancel()
	t, stats, release, err := s.readAndCopy(readCtx, f, path, ref, number, v)
	if err != nil {
		return nil, err
	}
	defer release()
	// The read is done: keep it even if every caller has gone.
	keepCtx, cancelKeep := context.WithTimeout(context.WithoutCancel(ctx), keepTimeout)
	defer cancelKeep()
	if err := s.keep(keepCtx, t, filepath.Join(s.mirrorDir(), mirrorName(t))); err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "kept embedded subtitle track", "component", "keptsub",
		"file_id", t.MediaFileID, "stream_index", t.StreamIndex, "method", t.Method, "packets", t.Packets,
		"bytes", t.Bytes, "reads", stats.Reads, "read_bytes", stats.Bytes, "elapsed_ms", stats.Wall.Milliseconds(),
		"background", f.prio != nil)
	return t, nil
}

// readAndCopy reads the track's packets, in one of the node's whole-read
// slots, and writes them as a kept stream to the local copies, returning
// the track to record.
func (s *Store) readAndCopy(ctx context.Context, f *flight, path string, ref playback.EmbeddedSubtitleRef, number uint64, v sourceVersion) (*Track, embeddedsub.ReadStats, func(), error) {
	select {
	case s.reads <- struct{}{}:
		defer func() { <-s.reads }()
	case <-ctx.Done():
		return nil, embeddedsub.ReadStats{}, nil, ctx.Err()
	}
	idx, track, err := s.sourceTrack(ctx, path, ref, number)
	if err != nil {
		return nil, embeddedsub.ReadStats{}, nil, err
	}
	start, err := s.startTime(ctx, path, v)
	if err != nil {
		return nil, embeddedsub.ReadStats{}, nil, err
	}
	res, err := embeddedsub.ReadPackets(ctx, path, idx, number, embeddedsub.WholeFrom, embeddedsub.WholeTo,
		embeddedsub.ReadOptions{Background: f.prio, MaxBytes: keptMaxBytes})
	if err != nil {
		return nil, embeddedsub.ReadStats{}, nil, err
	}
	if len(res.Packets) == 0 {
		return nil, res.Stats, nil, errNoPackets
	}
	_, sum, size, release, err := s.writeMirror(s.mirrorDir(), streamExt(idx.StreamFormat()), func(w io.Writer) error {
		return embeddedsub.WriteKept(w, idx, track, res.Packets)
	})
	if err != nil {
		return nil, res.Stats, nil, err
	}
	completeness := CompletenessUnknown
	if track.Completeness == embeddedsub.CompletenessComplete {
		completeness = CompletenessComplete
	}
	return &Track{
		MediaFileID:      ref.MediaFileID,
		StreamIndex:      ref.StreamIndex,
		ContainerTrackID: ref.ContainerTrackID,
		Codec:            ref.Codec,
		SourceSize:       idx.Size,
		SourceModTime:    idx.ModTime.UnixNano(),
		StreamFormat:     idx.StreamFormat(),
		StartTime:        start,
		SHA256:           sum,
		Bytes:            size,
		Packets:          len(res.Packets),
		Completeness:     completeness,
		Method:           MethodCues,
		HasOverrideTags:  overrideTags(ref.Codec, res.Packets),
	}, res.Stats, release, nil
}

// waitForOther waits while another server reads the track, for the track
// it keeps, or for the claim to come free, which it then takes (ok). It
// gives up after wholeReadTimeout, returning neither.
func (s *Store) waitForOther(ctx context.Context, ref playback.EmbeddedSubtitleRef, v sourceVersion, token string) (t *Track, ok bool, err error) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	deadline := time.Now().Add(wholeReadTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-ticker.C:
		}
		if t, err := s.repo.Track(ctx, ref.MediaFileID, ref.StreamIndex); err == nil && t.matches(v, ref) {
			return t, false, nil
		}
		if claimed, err := s.repo.Claim(ctx, ref.MediaFileID, ref.StreamIndex, token, claimLease); err == nil && claimed {
			return nil, true, nil
		}
	}
	return nil, false, nil
}

// sourceTrack returns the source's index and ref's track in it.
func (s *Store) sourceTrack(ctx context.Context, path string, ref playback.EmbeddedSubtitleRef, number uint64) (*embeddedsub.Index, *embeddedsub.Track, error) {
	idx, err := s.indexes.Index(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	track, ok := idx.Track(number)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %d", embeddedsub.ErrTrackNotFound, number)
	}
	if codecFamily(track.FFmpegCodec()) != codecFamily(ref.Codec) {
		return nil, nil, fmt.Errorf("%w: %s is %s", errCodecMismatch, ref.Codec, track.CodecID)
	}
	return idx, track, nil
}

// startTime returns the start time FFmpeg gives a source version.
func (s *Store) startTime(ctx context.Context, path string, v sourceVersion) (time.Duration, error) {
	s.mu.Lock()
	start, ok := s.starts[v]
	s.mu.Unlock()
	if ok {
		return start, nil
	}
	start, err := s.probeStart(ctx, path)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	if len(s.starts) >= startTimesKept {
		clear(s.starts)
	}
	s.starts[v] = start
	s.mu.Unlock()
	return start, nil
}

// DemuxCopy says how a demux of the whole source copies ref's track to
// keep it: the muxer for its codec, and the source's start time.
func (s *Store) DemuxCopy(ctx context.Context, path string, ref playback.EmbeddedSubtitleRef) (playback.DemuxCopy, error) {
	format := demuxCopyFormat(ref.Codec)
	if format == "" {
		return playback.DemuxCopy{}, errNotServable
	}
	if s.mirrorDir() == "" {
		return playback.DemuxCopy{}, errNoMirrorDir
	}
	info, err := fspath.StatContext(ctx, path)
	if err != nil {
		return playback.DemuxCopy{}, err
	}
	start, err := s.startTime(ctx, path, versionOf(path, info))
	if err != nil {
		return playback.DemuxCopy{}, err
	}
	return playback.DemuxCopy{Format: format, StartTime: start}, nil
}

// KeepDemuxed keeps the copy of ref's track a demux of path wrote, once it
// indexes as one track with packets, moving the copy's file to the local
// copies. Only a text track's packets are read, for its override-tag fact;
// a PGS track's are counted from the index.
func (s *Store) KeepDemuxed(ctx context.Context, path string, ref playback.EmbeddedSubtitleRef, demuxCopy playback.DemuxCopy, srcSize int64, srcModTime time.Time) error {
	copyFile, err := os.Open(demuxCopy.Path)
	if err != nil {
		return err
	}
	defer func() { _ = copyFile.Close() }()
	info, err := copyFile.Stat()
	if err != nil {
		return err
	}
	if info.Size() >= playback.MaxDemuxCopyBytes {
		return embeddedsub.ErrTooLarge
	}
	idx, err := embeddedsub.ReadIndex(ctx, demuxCopy.Path)
	if err != nil {
		return err
	}
	if len(idx.Tracks) != 1 {
		return fmt.Errorf("%w: demuxed copy has %d tracks", embeddedsub.ErrMalformed, len(idx.Tracks))
	}
	track := &idx.Tracks[0]
	packets := len(track.Cues)
	if idx.Container == embeddedsub.ContainerMP4 {
		packets = int(track.FrameCount)
	}
	var tags *bool
	if overrideTagsApply(ref.Codec) {
		res, err := embeddedsub.ReadPackets(ctx, demuxCopy.Path, idx, track.Number, embeddedsub.WholeFrom, embeddedsub.WholeTo, embeddedsub.ReadOptions{MaxBytes: keptMaxBytes})
		if err != nil {
			return err
		}
		packets, tags = len(res.Packets), overrideTags(ref.Codec, res.Packets)
	}
	if packets <= 0 {
		return errNoPackets
	}
	if info, err := fspath.StatContext(ctx, path); err != nil || info.Size() != srcSize || !info.ModTime().Equal(srcModTime) {
		return errSourceChanged
	}
	local, sum, release, err := s.adoptMirror(s.mirrorDir(), streamExt(demuxCopy.Format), copyFile, info.Size())
	if err != nil {
		return err
	}
	defer release()
	t := &Track{
		MediaFileID:      ref.MediaFileID,
		StreamIndex:      ref.StreamIndex,
		ContainerTrackID: ref.ContainerTrackID,
		Codec:            ref.Codec,
		SourceSize:       srcSize,
		SourceModTime:    srcModTime.UnixNano(),
		StreamFormat:     demuxCopy.Format,
		StartTime:        demuxCopy.StartTime,
		SHA256:           sum,
		Bytes:            info.Size(),
		Packets:          packets,
		// FFmpeg demuxed the whole file.
		Completeness:    CompletenessComplete,
		Method:          MethodFFmpeg,
		HasOverrideTags: tags,
	}
	if err := s.keep(ctx, t, local); err != nil {
		return err
	}
	slog.InfoContext(ctx, "kept embedded subtitle track", "component", "keptsub",
		"file_id", t.MediaFileID, "stream_index", t.StreamIndex, "method", t.Method, "packets", t.Packets, "bytes", t.Bytes)
	return nil
}

// keep stores the local copy at local as t's kept stream and records t.
// The key is queued for the collector before the object is stored, so an
// object whose row never lands is deleted after orphanGrace, while one a row
// names is live and stays. The object goes before the row, so a row never
// names a missing object.
func (s *Store) keep(ctx context.Context, t *Track, local string) error {
	t.BlobKey = blobKey(t)
	if err := s.repo.QueueCollection(ctx, t.BlobKey, orphanGrace); err != nil {
		return err
	}
	f, err := os.Open(local) //nolint:gosec // G304: a copy under the mirror directory, named by its hash
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := s.blobs.PutStream(ctx, t.BlobKey, f, "application/octet-stream"); err != nil {
		return fmt.Errorf("store kept subtitle track: %w", err)
	}
	return s.repo.PutTrack(ctx, *t)
}

// blobKey is subtitles/embedded/<file id>/<stream index>-<sha256>.<ext>.
func blobKey(t *Track) string {
	return keyRoot + strconv.Itoa(t.MediaFileID) + "/" + strconv.Itoa(t.StreamIndex) + "-" + t.SHA256 + streamExt(t.StreamFormat)
}

func streamExt(format string) string {
	if format == string(embeddedsub.ContainerMP4) {
		return ".mp4"
	}
	return ".mks"
}

// trackNumber parses the container track ID the index addresses a track by.
func trackNumber(ref playback.EmbeddedSubtitleRef) (uint64, error) {
	n, err := strconv.ParseUint(ref.ContainerTrackID, 10, 64)
	if err != nil || n == 0 {
		return 0, errNoTrackID
	}
	return n, nil
}

// Codec families, as playback.SubtitleCodecFamilyV3 names them, that the
// index reads: the text codecs and PGS, the bitmap codec served without
// burn-in.
const (
	codecSubRip  = "subrip"
	codecASS     = "ass"
	codecWebVTT  = "webvtt"
	codecMovText = "mov_text"
	codecPGS     = "hdmv_pgs_subtitle"
)

// codecFamily is playback's family of a codec name.
func codecFamily(codec string) string { return playback.SubtitleCodecFamilyV3(codec) }

// servable reports whether tracks of codec are read through the index.
func servable(codec string) bool {
	switch codecFamily(codec) {
	case codecSubRip, codecASS, codecWebVTT, codecMovText, codecPGS:
		return true
	}
	return false
}

// demuxCopyFormat is the muxer a demux copies a track of codec into.
func demuxCopyFormat(codec string) string {
	switch {
	case !servable(codec):
		return ""
	case codecFamily(codec) == codecMovText:
		return string(embeddedsub.ContainerMP4)
	}
	return string(embeddedsub.ContainerMatroska)
}

// overrideTags says whether a plain-text track's packets carry ASS
// override tags; nil for ASS, whose events are built of them, and PGS.
func overrideTags(codec string, packets []embeddedsub.Packet) *bool {
	if overrideTagsApply(codec) {
		has := embeddedsub.HasOverrideTags(packets)
		return &has
	}
	return nil
}
