package playback

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/lang"
)

// On-demand subtitle renditions list every embedded text track of the source
// as a WebVTT SUBTITLES rendition of its own, in one group, instead of the one
// selected track the stream's FFmpeg demuxes (subtitle_rendition.go). Each
// segment is read when it is requested: the track's packets for the segment's
// window come from the source's own index (embeddedsub.ReadPackets) and go
// through the FFmpeg WebVTT conversion the pipeline rendition uses, so a
// segment ahead of the stream's FFmpeg carries its cues at once, and choosing
// another track is the player's own media selection on the same stream.
//
// The timing is the pipeline rendition's: the stream's generations still
// record spans, and a segment's window comes from the span's origin and the
// video playlist's segment durations (subtitleSegmentWindow). Only where the
// cues come from changes. They are put on the clock the stream's FFmpeg would
// have written them on: the container's own timestamps for an encode
// (-copyts), less the source's start time for a copy remux (-start_at_zero).
//
// A segment's URI names its track by container track number (Matroska
// TrackNumber, MP4 track ID), sub_<track>_<segment>.vtt, so a session rebuilt
// without the track list can still serve the segments a player already holds.

// SubtitleRenditionTrack is one embedded text track an HLS stream publishes as
// an on-demand WebVTT rendition.
type SubtitleRenditionTrack struct {
	// StreamIndex is the absolute probed stream index.
	StreamIndex int `json:"stream_index"`
	// TrackNumber is the Matroska TrackNumber or MP4 track ID, the track's
	// container_track_id, which the source's index reads it by.
	TrackNumber uint64 `json:"track_number"`
	// Codec is the track's probed codec; a kept copy of the track is found
	// by it (EmbeddedSubtitleRef).
	Codec string `json:"codec,omitempty"`
	// Name is the rendition's NAME: the inventory row's label, made unique in
	// the group (SubtitleRenditionNames).
	Name     string `json:"name"`
	Language string `json:"language,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
	// Default marks the plan's selected track, the rendition marked
	// DEFAULT=YES; none is when subtitles are off or come from a sidecar.
	Default bool `json:"default,omitempty"`
}

// SubtitleRenditionSource is the source file version a plan chose its
// on-demand renditions from: its size and modification time. Segments are
// read from that version only, so a file replaced while it plays answers
// empty segments rather than cues from another file.
type SubtitleRenditionSource struct {
	Size            int64 `json:"size"`
	ModTimeUnixNano int64 `json:"mtime_ns"`
}

// IsZero reports a version that wasn't recorded.
func (v SubtitleRenditionSource) IsZero() bool { return v == SubtitleRenditionSource{} }

// matches reports whether idx describes version v; an unrecorded version
// takes any.
func (v SubtitleRenditionSource) matches(idx *embeddedsub.Index) bool {
	return v.IsZero() || idx.Size == v.Size && idx.ModTime.UnixNano() == v.ModTimeUnixNano
}

// SubtitleRenditionTrackQueryParam names the on-demand track, by container
// track number, whose playlist silo_rendition=subtitles selects.
const SubtitleRenditionTrackQueryParam = "silo_track"

const (
	// renditionCueCacheBytes bounds the converted windows kept for on-demand
	// segments. A window of text cues is a few hundred bytes.
	renditionCueCacheBytes = 16 << 20
	// renditionConversions bounds the FFmpeg conversions on-demand segments
	// run at once in this process.
	renditionConversions = 8
	// renditionLoads bounds the windows being read at once, queued for a
	// conversion slot or holding one. A miss past it answers 404 for the
	// player to retry.
	renditionLoads = 256
	// renditionStartTimes bounds the source start times kept for copy remux
	// renditions.
	renditionStartTimes = 1024
)

var (
	renditionCues   = newRenditionCueCache(renditionCueCacheBytes, renditionLoads)
	renditionSlots  = make(chan struct{}, renditionConversions)
	renditionStarts = newRenditionStartCache(renditionStartTimes)
	// renditionKept holds the kept tracks (SetRenditionKeptTracks).
	renditionKept atomic.Pointer[keptRenditionSource]
)

// keptRenditionSource wraps the EmbeddedSubtitleSource on-demand renditions
// read kept tracks through.
type keptRenditionSource struct{ src EmbeddedSubtitleSource }

// SetRenditionKeptTracks makes on-demand renditions read a segment's window
// from the track's kept copy (keptsub) when there is one, before reading the
// source; it reads the source otherwise. A server without kept tracks, such
// as a transcode node, doesn't set it.
func SetRenditionKeptTracks(src EmbeddedSubtitleSource) {
	if src == nil {
		renditionKept.Store(nil)
		return
	}
	renditionKept.Store(&keptRenditionSource{src: src})
}

// keptRenditionWindowSource returns the kept tracks to read track number's
// windows through, or nil: none are set, or the recipe doesn't name the
// catalog track a kept copy is found by.
func keptRenditionWindowSource(opts TranscodeOpts, number uint64) (EmbeddedSubtitleSource, EmbeddedSubtitleRef) {
	kept := renditionKept.Load()
	rendition, ok := renditionTrack(opts, number)
	if kept == nil || !ok || opts.MediaFileID <= 0 || rendition.StreamIndex < 0 || rendition.Codec == "" {
		return nil, EmbeddedSubtitleRef{}
	}
	return kept.src, EmbeddedSubtitleRef{
		MediaFileID: opts.MediaFileID, StreamIndex: rendition.StreamIndex,
		ContainerTrackID: strconv.FormatUint(number, 10), Codec: rendition.Codec,
	}
}

// SubtitleRenditionNames turns inventory labels into the NAMEs of one
// rendition group, which HLS requires to differ: characters a quoted-string
// can't hold are replaced, an empty label falls back to fallback (the row's
// language) or "Subtitles", and a repeated name gets " (2)", " (3)"...
func SubtitleRenditionNames(labels, fallbacks []string) []string {
	names := make([]string, len(labels))
	seen := make(map[string]bool, len(labels))
	for i, label := range labels {
		name := hlsQuotedStringValue(label)
		if name == "" && i < len(fallbacks) {
			name = hlsQuotedStringValue(fallbacks[i])
		}
		if name == "" {
			name = SubtitleRenditionName
		}
		unique := name
		for n := 2; seen[strings.ToLower(unique)]; n++ {
			unique = fmt.Sprintf("%s (%d)", name, n)
		}
		seen[strings.ToLower(unique)] = true
		names[i] = unique
	}
	return names
}

// hlsQuotedStringValue makes s safe inside an HLS quoted-string, which can't
// hold a double quote, CR or LF: quotes become apostrophes and control
// characters are dropped.
func hlsQuotedStringValue(s string) string {
	s = strings.ReplaceAll(s, `"`, "'")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// renditionTrack returns the listed on-demand track with the given number.
func renditionTrack(opts TranscodeOpts, number uint64) (SubtitleRenditionTrack, bool) {
	for _, track := range opts.SubtitleRenditionTracks {
		if track.TrackNumber == number {
			return track, true
		}
	}
	return SubtitleRenditionTrack{}, false
}

// subtitleTrackSegmentPrefix names the segments of on-demand track number.
func subtitleTrackSegmentPrefix(number uint64) string {
	return subtitleSegmentPrefix + strconv.FormatUint(number, 10) + "_"
}

// parseSubtitleTrackSegment parses an on-demand rendition segment name,
// sub_<track>_<segment>.vtt.
func parseSubtitleTrackSegment(name string) (number uint64, segNum int, ok bool) {
	rest, ok := strings.CutPrefix(name, subtitleSegmentPrefix)
	if !ok {
		return 0, 0, false
	}
	rest, ok = strings.CutSuffix(rest, subtitleSegmentExt)
	if !ok {
		return 0, 0, false
	}
	track, segment, ok := strings.Cut(rest, "_")
	if !ok || !allDigits(track) || !allDigits(segment) {
		return 0, 0, false
	}
	number, err := strconv.ParseUint(track, 10, 64)
	if err != nil || number == 0 {
		return 0, 0, false
	}
	segNum, err = strconv.Atoi(segment)
	if err != nil {
		return 0, 0, false
	}
	return number, segNum, true
}

func allDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// parseSubtitleRenditionTrack reads an on-demand track number from a
// SubtitleRenditionTrackQueryParam value.
func parseSubtitleRenditionTrack(value string) (uint64, bool) {
	if !allDigits(value) {
		return 0, false
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return number, err == nil && number > 0
}

// subtitleRenditionsPlaylist is the multivariant playlist of a stream with
// on-demand renditions: one EXT-X-MEDIA per track, in the recipe's order,
// DEFAULT=YES only on the selected one.
func subtitleRenditionsPlaylist(opts TranscodeOpts, uri func(rendition string) string) []byte {
	var buf bytes.Buffer
	for _, track := range opts.SubtitleRenditionTracks {
		def := "NO"
		if track.Default {
			def = "YES"
		}
		forced := "NO"
		if track.Forced {
			forced = "YES"
		}
		name := hlsQuotedStringValue(track.Name)
		if name == "" {
			// A session rebuilt from a stream token knows its tracks only
			// by number.
			name = "Track " + strconv.FormatUint(track.TrackNumber, 10)
		}
		fmt.Fprintf(&buf, "#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=%q,NAME=\"%s\"", SubtitleRenditionGroupID, name)
		if language := hlsLanguage(track.Language); language != "" {
			fmt.Fprintf(&buf, ",LANGUAGE=\"%s\"", language)
		}
		fmt.Fprintf(&buf, ",DEFAULT=%s,AUTOSELECT=YES,FORCED=%s,URI=%q\n", def, forced,
			uri(subtitleRenditionSubtitles+"&"+SubtitleRenditionTrackQueryParam+"="+strconv.FormatUint(track.TrackNumber, 10)))
	}
	return buf.Bytes()
}

// hlsLanguage is a rendition's LANGUAGE: the track's language as a canonical
// BCP 47 tag ("eng" becomes "en"), as HLS asks, or "" when it is unknown or
// isn't one.
func hlsLanguage(language string) string {
	tag := lang.CanonicalTag(language)
	if strings.EqualFold(tag, "und") {
		return ""
	}
	return tag
}

// SubtitleRenditionTrackCount is how many on-demand renditions the session
// serves: 0 without them, or when its recipe can't carry a rendition at all.
func (s *TranscodeSession) SubtitleRenditionTrackCount() int {
	opts := s.Opts()
	if !subtitleRenditionActive(opts) {
		return 0
	}
	return len(opts.SubtitleRenditionTracks)
}

// subtitleTrackSegmentVTT returns segment segNum of on-demand track number.
// Its cues are read whenever it is asked for, however far ahead of the
// stream's FFmpeg. Only its window waits on the stream: a segment before
// every generation waits, as the pipeline rendition's does, for the restart
// its video request starts, and the first segments of a generation for the
// origin the generation's first video segment shows. A segment the read
// can't produce in time answers ErrSegmentNotFound, which the player
// retries, never an empty document it would keep.
func (s *TranscodeSession) subtitleTrackSegmentVTT(ctx context.Context, number uint64, segNum int) ([]byte, error) {
	opts := s.Opts()
	if !subtitleRenditionActive(opts) || !subtitleRenditionsOnDemand(opts) {
		return nil, ErrSegmentNotFound
	}
	if _, ok := renditionTrack(opts, number); !ok {
		return nil, ErrSegmentNotFound
	}
	// An encode's playlist is the synthetic grid; a segment past it has no
	// window to read. A copy's segments are those its spans place.
	if segNum < 0 || !strings.EqualFold(opts.TargetCodecVideo, "copy") && segNum >= syntheticSegmentCount(opts.TotalDuration, opts.SegmentDuration) {
		return nil, ErrSegmentNotFound
	}
	started := time.Now()
	deadline := started.Add(segmentWaitTimeout(opts.SegmentDuration))
	originDeadline := started.Add(subtitleSegmentProductionWait)
	poll := subtitleCuePollInterval
	for {
		span, found, originPending, waitable, restarting := s.onDemandSegmentSpan(segNum)
		if restarting || s.copyPlan != nil {
			// As long as its video segment would wait: a restart, or a
			// planned copy, which runs on to every planned segment.
			deadline = originDeadline
		}
		expired := !time.Now().Before(deadline)
		switch {
		case found:
			window := s.subtitleSegmentWindow(ctx, span.file, segNum)
			if window.observed || !originPending || !time.Now().Before(originDeadline) {
				return onDemandSegmentDocument(ctx, opts, number, segNum, span, window)
			}
		case waitable && s.copyPlan != nil && expired:
			// Like its video segment, a planned copy's segment no generation
			// reached answers 404, which the player retries.
			return nil, ErrSegmentNotFound
		case !waitable || expired:
			return subtitleSegmentDocument(opts, segNum, subtitleCueWindow{}, nil), nil
		}
		timer := time.NewTimer(poll)
		poll = min(2*poll, maxSubtitleCuePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// onDemandSegmentSpan finds the span whose timeline segment segNum is on: the
// newest that started at or before it and is live or got past it, the
// generation whose video segment the player gets. originPending reports that
// the span's origin is not observed yet but its process may still produce the
// segment that shows it, waitable that a running or restarting process may
// still start a span that covers segNum, and restarting that a restart is
// under way. A copy's segment is on a span only once its duration is known.
func (s *TranscodeSession) onDemandSegmentSpan(segNum int) (span subtitleCueSpan, found, originPending, waitable, restarting bool) {
	s.mu.Lock()
	spans := slices.Clone(s.subtitleSpans)
	running, restarting := s.running, s.restarting != nil
	s.mu.Unlock()
	waitable = running || restarting
	for i := len(spans) - 1; i >= 0; i-- {
		candidate := spans[i]
		if segNum < candidate.start || candidate.end >= 0 && segNum > candidate.end {
			continue
		}
		if _, known := s.subtitleSegmentPlacementKnown(candidate, segNum); !known {
			return subtitleCueSpan{}, false, false, waitable, restarting
		}
		originPending = !candidate.originKnown && candidate.end < 0 && waitable
		return candidate, true, originPending, waitable, restarting
	}
	return subtitleCueSpan{}, false, false, waitable, restarting
}

// onDemandSegmentDocument reads track number's cues for segment segNum's
// window and cuts the segment from them as the pipeline rendition cuts its
// cue file. A track the file can't give answers an empty segment, as the
// pipeline's does for a track it can't demux, so the player plays on; a read
// that failed or ran out of time answers ErrSegmentNotFound, for the player
// to retry.
func onDemandSegmentDocument(ctx context.Context, opts TranscodeOpts, number uint64, segNum int, span subtitleCueSpan, window subtitleCueWindow) ([]byte, error) {
	cues, err := readOnDemandCues(ctx, opts, number, segNum, span, window)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case renditionReadPermanent(err):
		slog.WarnContext(ctx, "on-demand subtitle rendition track unreadable; segment answered empty", "component", "playback",
			"playback_session_id", opts.SessionID, "track_number", number, "segment", segNum, "error", err)
		cues = nil
	default:
		slog.WarnContext(ctx, "on-demand subtitle rendition segment not read in time", "component", "playback",
			"playback_session_id", opts.SessionID, "track_number", number, "segment", segNum, "error", err)
		return nil, ErrSegmentNotFound
	}
	return subtitleSegmentDocument(opts, segNum, window, cues), nil
}

// renditionReadPermanent reports whether a failed read says something about
// the file or the track, which retrying won't change, rather than about load
// or time.
func renditionReadPermanent(err error) bool {
	for _, target := range []error{
		embeddedsub.ErrNotSupportedContainer, embeddedsub.ErrMalformed, embeddedsub.ErrNoCues,
		embeddedsub.ErrIncompleteCues, embeddedsub.ErrUnsupported, embeddedsub.ErrTooLarge,
		embeddedsub.ErrTrackNotFound, embeddedsub.ErrFileChanged,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// subtitleWindowBounds places segment segNum's window on the cue clock in
// 90 kHz ticks, as subtitleSegmentDocument cuts it; firstCue90k, on the cue
// clock, chooses an observed MPEG-TS origin's epoch (subtitleWindowEpoch). An
// open window ends at math.MaxInt64.
func subtitleWindowBounds(opts TranscodeOpts, segNum int, window subtitleCueWindow, firstCue90k int64) (start90k, end90k int64) {
	if window.duration <= 0 {
		segDur := renditionSegmentDuration(opts)
		window.duration = float64(segDur)
		window.open = segNum >= syntheticSegmentCount(opts.TotalDuration, segDur)-1
	}
	origin90k := int64(math.Round(window.origin * 90_000))
	if window.observed && HLSOutputContainer(opts) == OutputContainerMPEGTS {
		origin90k = subtitleWindowEpoch(origin90k, firstCue90k)
	}
	start90k = origin90k + int64(math.Round(window.offset*90_000))
	end90k = start90k + int64(math.Round(window.duration*90_000))
	if window.open {
		end90k = math.MaxInt64
	}
	return start90k, end90k
}

// readOnDemandCues returns the cues of track number that show in segment
// segNum's window as WebVTT, converted by FFmpeg from the packets of the
// track's kept copy (SetRenditionKeptTracks), or else the ones the source's
// index points at; both give the same bytes. Only the file version the plan
// read is read (SubtitleRenditionSource). Windows are kept per file version,
// track and clock, so every session on the file shares them.
func readOnDemandCues(ctx context.Context, opts TranscodeOpts, number uint64, segNum int, span subtitleCueSpan, window subtitleCueWindow) ([]byte, error) {
	idx, err := subtitleIndexes.Index(ctx, opts.InputPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: source is gone: %w", embeddedsub.ErrFileChanged, err)
	}
	if err != nil {
		return nil, err
	}
	if !opts.SubtitleRenditionSource.matches(idx) {
		return nil, fmt.Errorf("%w: not the version the plan read", embeddedsub.ErrFileChanged)
	}
	track, ok := idx.Track(number)
	if !ok {
		return nil, fmt.Errorf("%w: %d", embeddedsub.ErrTrackNotFound, number)
	}
	// A copy remux's cues are on the source's clock less its start time
	// (-start_at_zero), an encode's on the source's own (-copyts).
	copyClock := copyMuxShiftsVideo(opts)
	tsEpoch := window.observed && HLSOutputContainer(opts) == OutputContainerMPEGTS
	kept, ref := keptRenditionWindowSource(opts, number)
	var startTime time.Duration
	if copyClock || tsEpoch || kept != nil {
		if startTime, err = renditionStarts.get(ctx, opts.FFmpegPath, opts.InputPath, idx); err != nil {
			return nil, err
		}
	}
	// The pipeline picks an observed MPEG-TS origin's epoch against its first
	// cue. Before the cues are read, where the window was expected to start on
	// the cue clock stands in for it; the two agree to far less than the hour
	// subtitleWindowEpoch allows.
	expected := span.startTime + window.offset
	var clockStart time.Duration
	if copyClock {
		clockStart = startTime
	} else {
		expected += startTime.Seconds()
	}
	from90k, to90k := subtitleWindowBounds(opts, segNum, window, int64(math.Round(expected*90_000)))
	from := ticks90kDuration(from90k) + clockStart
	to := embeddedsub.WholeTo
	if to90k != math.MaxInt64 {
		to = ticks90kDuration(to90k) + clockStart
	}
	key := renditionCueKey{path: opts.InputPath, size: idx.Size, mtime: idx.ModTime.UnixNano(), track: number, startTime: clockStart, from: from, to: to}
	return renditionCues.get(ctx, key, func(ctx context.Context) ([]byte, error) {
		// The slot bounds the window's reads as well as its conversion.
		select {
		case renditionSlots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-renditionSlots }()
		// A window open to the end would have the kept tracks read the
		// whole track first; it is read from the source.
		if kept != nil && to != embeddedsub.WholeTo {
			in, err := kept.Window(ctx, opts.InputPath, ref, from-startTime, to-startTime)
			if err == nil {
				return convertKeptRenditionWindow(ctx, opts, in, clockStart)
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			slog.InfoContext(ctx, "on-demand subtitle rendition window not read from the kept track; reading the source", "component", "playback",
				"playback_session_id", opts.SessionID, "track_number", number, "error", err)
		}
		res, err := embeddedsub.ReadPackets(ctx, opts.InputPath, idx, number, from, to, embeddedsub.ReadOptions{})
		var damaged *embeddedsub.DamagedError
		if errors.As(err, &damaged) {
			// A window keeps what it could read; the rest of the file's
			// cues are still worth showing.
			slog.WarnContext(ctx, "on-demand subtitle rendition read damaged cues", "component", "playback",
				"playback_session_id", opts.SessionID, "track_number", number, "error", err)
			err = nil
		}
		if err != nil {
			return nil, err
		}
		if len(res.Packets) == 0 {
			return nil, nil
		}
		return ExtractSubtitlePacketsWebVTT(ctx, &SubtitlePacketInput{Index: idx, Track: track, Packets: res.Packets, SourceStartTime: clockStart}, opts.FFmpegPath)
	})
}

// convertKeptRenditionWindow converts a window read from a kept track. Kept
// windows are asked for on the playback timeline, which starts at the
// source's start time, but their packets keep the source's timestamps, so
// they convert on the cue clock as the source's do: clockStart is the start
// time the cue clock leaves out.
func convertKeptRenditionWindow(ctx context.Context, opts TranscodeOpts, in *SubtitlePacketInput, clockStart time.Duration) ([]byte, error) {
	if in.Stream == nil && len(in.Packets) == 0 {
		return nil, nil
	}
	in.SourceStartTime = clockStart
	return ExtractSubtitlePacketsWebVTT(ctx, in, opts.FFmpegPath)
}

// ticks90kDuration converts 90 kHz ticks to a Duration without overflowing
// on the way, saturating outside the Duration range.
func ticks90kDuration(ticks int64) time.Duration {
	whole, part := ticks/90_000, ticks%90_000
	const limit = math.MaxInt64 / int64(time.Second)
	switch {
	case whole >= limit:
		return time.Duration(math.MaxInt64)
	case whole <= -limit:
		return time.Duration(math.MinInt64)
	}
	return time.Duration(whole)*time.Second + time.Duration(part)*time.Second/90_000
}

// renditionCueKey names one converted window: the file version, the track,
// the clock it is on (the start time subtracted) and the window on the
// container's timeline.
type renditionCueKey struct {
	path        string
	size, mtime int64
	track       uint64
	startTime   time.Duration
	from, to    time.Duration
}

// renditionCueCache keeps converted windows up to a byte budget, evicting the
// least recently used. Concurrent misses for one window share one read, which
// stops when the last of them gives up, and at most maxLoads reads run at
// once. Errors are not kept.
type renditionCueCache struct {
	maxBytes int64
	maxLoads int

	mu      sync.Mutex
	used    int64
	lru     *list.List // of renditionCueKey, most recent first
	entries map[renditionCueKey]*renditionCueEntry
	loading map[renditionCueKey]*renditionCueLoad
}

type renditionCueEntry struct {
	cues []byte
	elem *list.Element
}

type renditionCueLoad struct {
	done chan struct{}
	// cancel stops the read; waiters counts the callers waiting on it.
	// Both are guarded by the cache's mu.
	cancel  context.CancelFunc
	waiters int
	cues    []byte
	err     error
}

// errRenditionLoadsBusy reports a miss while maxLoads reads are running.
var errRenditionLoadsBusy = errors.New("too many on-demand subtitle reads")

func newRenditionCueCache(maxBytes int64, maxLoads int) *renditionCueCache {
	return &renditionCueCache{
		maxBytes: maxBytes,
		maxLoads: maxLoads,
		lru:      list.New(),
		entries:  map[renditionCueKey]*renditionCueEntry{},
		loading:  map[renditionCueKey]*renditionCueLoad{},
	}
}

// get returns the window under key, reading it with read on a miss. The
// read runs on its own, within subtitleSegmentProductionWait, so a caller
// that gives up doesn't fail the others; it stops once every caller has.
func (c *renditionCueCache) get(ctx context.Context, key renditionCueKey, read func(context.Context) ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.lru.MoveToFront(e.elem)
		c.mu.Unlock()
		return e.cues, nil
	}
	load, loading := c.loading[key]
	if !loading {
		if len(c.loading) >= c.maxLoads {
			c.mu.Unlock()
			return nil, errRenditionLoadsBusy
		}
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subtitleSegmentProductionWait)
		load = &renditionCueLoad{done: make(chan struct{}), cancel: cancel}
		c.loading[key] = load
		go func() {
			defer cancel()
			c.load(readCtx, key, load, read)
		}()
	}
	load.waiters++
	c.mu.Unlock()
	select {
	case <-load.done:
		return load.cues, load.err
	case <-ctx.Done():
		c.leave(key, load)
		return nil, ctx.Err()
	}
}

// leave drops a caller that gave up on load, stopping the read when nobody
// else waits on it. The next request for the window reads it again.
func (c *renditionCueCache) leave(key renditionCueKey, load *renditionCueLoad) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if load.waiters--; load.waiters == 0 && c.loading[key] == load {
		delete(c.loading, key)
		load.cancel()
	}
}

func (c *renditionCueCache) load(ctx context.Context, key renditionCueKey, load *renditionCueLoad, read func(context.Context) ([]byte, error)) {
	defer func() {
		if r := recover(); r != nil {
			load.cues, load.err = nil, fmt.Errorf("on-demand subtitle read panicked: %v", r)
		}
		c.mu.Lock()
		if c.loading[key] == load {
			delete(c.loading, key)
			if load.err == nil {
				c.add(key, load.cues)
			}
		}
		c.mu.Unlock()
		close(load.done)
	}()
	load.cues, load.err = read(ctx)
}

// add stores cues under key and evicts down to the budget. c.mu is held.
func (c *renditionCueCache) add(key renditionCueKey, cues []byte) {
	size := int64(len(cues)) + int64(len(key.path)) + 128
	if size > c.maxBytes {
		return
	}
	c.entries[key] = &renditionCueEntry{cues: cues, elem: c.lru.PushFront(key)}
	c.used += size
	for c.used > c.maxBytes {
		oldest, _ := c.lru.Remove(c.lru.Back()).(renditionCueKey)
		c.used -= int64(len(c.entries[oldest].cues)) + int64(len(oldest.path)) + 128
		delete(c.entries, oldest)
	}
}

// renditionStartCache keeps sources' start times (SubtitleSourceStartTime)
// by file version, up to a count; it forgets them all when full. Concurrent
// misses for one version share one ffprobe run. Errors are not kept.
type renditionStartCache struct {
	max int

	mu      sync.Mutex
	times   map[renditionStartKey]time.Duration
	loading map[renditionStartKey]*renditionStartLoad
}

type renditionStartKey struct {
	path        string
	size, mtime int64
}

type renditionStartLoad struct {
	done  chan struct{}
	start time.Duration
	err   error
}

func newRenditionStartCache(maxEntries int) *renditionStartCache {
	return &renditionStartCache{max: maxEntries, times: map[renditionStartKey]time.Duration{}, loading: map[renditionStartKey]*renditionStartLoad{}}
}

// get returns the start time of the file version idx describes, running
// ffprobe on a miss. The probe runs on its own, so a caller that gives up
// doesn't fail the others.
func (c *renditionStartCache) get(ctx context.Context, ffmpegPath, path string, idx *embeddedsub.Index) (time.Duration, error) {
	key := renditionStartKey{path: path, size: idx.Size, mtime: idx.ModTime.UnixNano()}
	c.mu.Lock()
	if start, ok := c.times[key]; ok {
		c.mu.Unlock()
		return start, nil
	}
	load, loading := c.loading[key]
	if !loading {
		load = &renditionStartLoad{done: make(chan struct{})}
		c.loading[key] = load
	}
	c.mu.Unlock()
	if !loading {
		go func() {
			probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subtitleOriginProbeTimeout)
			defer cancel()
			start, err := SubtitleSourceStartTime(probeCtx, ffmpegPath, path)
			c.mu.Lock()
			delete(c.loading, key)
			if err == nil {
				if len(c.times) >= c.max {
					clear(c.times)
				}
				c.times[key] = start
			}
			c.mu.Unlock()
			load.start, load.err = start, err
			close(load.done)
		}()
	}
	select {
	case <-load.done:
		return load.start, load.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// CueIndexedSubtitles is what a source's index says about on-demand
// renditions: which subtitle tracks, by container track number, they can
// read, and the file version that holds for.
type CueIndexedSubtitles struct {
	Tracks map[uint64]bool
	Source SubtitleRenditionSource
}

// CueIndexedSubtitleTracks reports which of the source's subtitle tracks an
// on-demand rendition can read: their packets are all found through the
// file's index (embeddedsub.Completeness complete or unknown) in a form the
// reader decodes. It reads the index through the process's cache
// (SubtitleIndexes), so a stream that starts next finds it there.
func CueIndexedSubtitleTracks(ctx context.Context, path string) (CueIndexedSubtitles, error) {
	idx, err := subtitleIndexes.Index(ctx, path)
	if err != nil {
		return CueIndexedSubtitles{}, err
	}
	readable := make(map[uint64]bool, len(idx.Tracks))
	for _, track := range idx.Tracks {
		if track.Unsupported != "" {
			continue
		}
		switch track.Completeness {
		case embeddedsub.CompletenessComplete, embeddedsub.CompletenessUnknown:
			readable[track.Number] = true
		}
	}
	return CueIndexedSubtitles{Tracks: readable, Source: SubtitleRenditionSource{Size: idx.Size, ModTimeUnixNano: idx.ModTime.UnixNano()}}, nil
}
