package playback

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
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
	// Name is the rendition's NAME: the inventory row's label, made unique in
	// the group (SubtitleRenditionNames).
	Name     string `json:"name"`
	Language string `json:"language,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
	// Default marks the plan's selected track, the rendition marked
	// DEFAULT=YES; none is when subtitles are off or come from a sidecar.
	Default bool `json:"default,omitempty"`
}

// SubtitleRenditionTrackQueryParam names the on-demand track, by container
// track number, whose playlist silo_rendition=subtitles selects.
const SubtitleRenditionTrackQueryParam = "silo_track"

const (
	// renditionIndexCacheBytes bounds the indexes kept for on-demand
	// renditions and the planner's check for them. An index is a few
	// kilobytes to a few hundred.
	renditionIndexCacheBytes = 64 << 20
	// renditionCueCacheBytes bounds the converted windows kept for on-demand
	// segments. A window of text cues is a few hundred bytes.
	renditionCueCacheBytes = 16 << 20
	// renditionConversions bounds the FFmpeg conversions on-demand segments
	// run at once in this process.
	renditionConversions = 8
	// renditionStartTimes bounds the source start times kept for copy remux
	// renditions.
	renditionStartTimes = 1024
)

var (
	// renditionIndexes keeps source indexes for on-demand renditions, shared
	// by every session in the process and by the planner's check.
	renditionIndexes = embeddedsub.NewCache(renditionIndexCacheBytes)
	renditionCues    = newRenditionCueCache(renditionCueCacheBytes)
	renditionSlots   = make(chan struct{}, renditionConversions)
	renditionStarts  = newRenditionStartCache(renditionStartTimes)
)

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

// hlsLanguage is a rendition's LANGUAGE, or "" when the track's language is
// unknown.
func hlsLanguage(language string) string {
	language = hlsQuotedStringValue(language)
	if strings.EqualFold(language, "und") || strings.ContainsAny(language, " ,") {
		return ""
	}
	return language
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
// origin the generation's first video segment shows.
func (s *TranscodeSession) subtitleTrackSegmentVTT(ctx context.Context, number uint64, segNum int) ([]byte, error) {
	opts := s.Opts()
	if !subtitleRenditionsOnDemand(opts) {
		return nil, ErrSegmentNotFound
	}
	if _, ok := renditionTrack(opts, number); !ok {
		return nil, ErrSegmentNotFound
	}
	started := time.Now()
	deadline := started.Add(segmentWaitTimeout(opts.SegmentDuration))
	originDeadline := started.Add(subtitleSegmentProductionWait)
	poll := subtitleCuePollInterval
	for {
		span, found, originPending, waitable, restarting := s.onDemandSegmentSpan(segNum)
		if restarting {
			deadline = originDeadline
		}
		if found {
			window := s.subtitleSegmentWindow(ctx, span.file, segNum)
			if window.observed || !originPending || !time.Now().Before(originDeadline) {
				return onDemandSegmentDocument(ctx, opts, number, segNum, span, window)
			}
		} else if !waitable || !time.Now().Before(deadline) {
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
// cue file. A track that can't be read answers an empty segment: the player
// keeps playing, as it does past a segment the pipeline never produced.
func onDemandSegmentDocument(ctx context.Context, opts TranscodeOpts, number uint64, segNum int, span subtitleCueSpan, window subtitleCueWindow) ([]byte, error) {
	// An observed MPEG-TS origin's epoch is chosen against where the window
	// was expected to start, since its cues aren't read yet: the two agree to
	// far less than the hour subtitleWindowEpoch allows.
	expected90k := int64(math.Round((span.startTime + window.offset) * 90_000))
	from90k, to90k := subtitleWindowBounds(opts, segNum, window, expected90k)
	cues, err := readRenditionCues(ctx, opts, number, from90k, to90k)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		slog.WarnContext(ctx, "on-demand subtitle rendition segment unreadable", "component", "playback",
			"playback_session_id", opts.SessionID, "track_number", number, "segment", segNum, "error", err)
		cues = nil
	}
	return subtitleSegmentDocument(opts, segNum, window, cues), nil
}

// subtitleWindowBounds places segment segNum's window on the cue clock in
// 90 kHz ticks, as subtitleSegmentDocument cuts it; firstCue90k chooses an
// observed MPEG-TS origin's epoch (subtitleWindowEpoch). An open window ends
// at math.MaxInt64.
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

// readRenditionCues returns track number's cues that show in [from90k,
// to90k) on the cue clock as WebVTT, converted by FFmpeg from the packets the
// source's index points at. Windows are kept per file version, track and
// clock, so every session on the file shares them.
func readRenditionCues(ctx context.Context, opts TranscodeOpts, number uint64, from90k, to90k int64) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		cues, err := readRenditionCuesOnce(ctx, opts, number, from90k, to90k)
		if errors.Is(err, embeddedsub.ErrFileChanged) && attempt == 0 {
			// The cache keys indexes by size and modification time, so the
			// next lookup reads the file again.
			continue
		}
		return cues, err
	}
}

func readRenditionCuesOnce(ctx context.Context, opts TranscodeOpts, number uint64, from90k, to90k int64) ([]byte, error) {
	idx, err := renditionIndexes.Index(ctx, opts.InputPath)
	if err != nil {
		return nil, err
	}
	track, ok := idx.Track(number)
	if !ok {
		return nil, fmt.Errorf("%w: %d", embeddedsub.ErrTrackNotFound, number)
	}
	// A copy remux's cues are on the source's clock less its start time
	// (-start_at_zero); an encode's on the source's own (-copyts).
	var startTime time.Duration
	if copyMuxShiftsVideo(opts) {
		startTime, err = renditionStarts.get(ctx, opts.FFmpegPath, opts.InputPath, idx)
		if err != nil {
			return nil, err
		}
	}
	from := ticks90kDuration(from90k) + startTime
	to := embeddedsub.WholeTo
	if to90k != math.MaxInt64 {
		to = ticks90kDuration(to90k) + startTime
	}
	key := renditionCueKey{path: opts.InputPath, size: idx.Size, mtime: idx.ModTime.UnixNano(), track: number, startTime: startTime, from: from, to: to}
	return renditionCues.get(ctx, key, func(ctx context.Context) ([]byte, error) {
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
		select {
		case renditionSlots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-renditionSlots }()
		return ExtractSubtitlePacketsWebVTT(ctx, &SubtitlePacketInput{Index: idx, Track: track, Packets: res.Packets, SourceStartTime: startTime}, opts.FFmpegPath)
	})
}

func ticks90kDuration(ticks int64) time.Duration {
	return time.Duration(ticks) * time.Second / 90_000
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
// least recently used. Concurrent misses for one window share one read.
// Errors are not kept.
type renditionCueCache struct {
	maxBytes int64

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
	cues []byte
	err  error
}

func newRenditionCueCache(maxBytes int64) *renditionCueCache {
	return &renditionCueCache{
		maxBytes: maxBytes,
		lru:      list.New(),
		entries:  map[renditionCueKey]*renditionCueEntry{},
		loading:  map[renditionCueKey]*renditionCueLoad{},
	}
}

// get returns the window under key, reading it with read on a miss. The
// read runs on its own, so a caller that gives up doesn't fail the others.
func (c *renditionCueCache) get(ctx context.Context, key renditionCueKey, read func(context.Context) ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.lru.MoveToFront(e.elem)
		c.mu.Unlock()
		return e.cues, nil
	}
	load, loading := c.loading[key]
	if !loading {
		load = &renditionCueLoad{done: make(chan struct{})}
		c.loading[key] = load
	}
	c.mu.Unlock()
	if !loading {
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subtitleSegmentProductionWait)
		go func() {
			defer cancel()
			c.load(readCtx, key, load, read)
		}()
	}
	select {
	case <-load.done:
		return load.cues, load.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *renditionCueCache) load(ctx context.Context, key renditionCueKey, load *renditionCueLoad, read func(context.Context) ([]byte, error)) {
	defer func() {
		if r := recover(); r != nil {
			load.cues, load.err = nil, fmt.Errorf("on-demand subtitle read panicked: %v", r)
		}
		c.mu.Lock()
		delete(c.loading, key)
		if load.err == nil {
			c.add(key, load.cues)
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
// by file version, up to a count; it forgets them all when full.
type renditionStartCache struct {
	max int

	mu    sync.Mutex
	times map[renditionStartKey]time.Duration
}

type renditionStartKey struct {
	path        string
	size, mtime int64
}

func newRenditionStartCache(maxEntries int) *renditionStartCache {
	return &renditionStartCache{max: maxEntries, times: map[renditionStartKey]time.Duration{}}
}

// get returns the start time of the file version idx describes, running
// ffprobe on a miss.
func (c *renditionStartCache) get(ctx context.Context, ffmpegPath, path string, idx *embeddedsub.Index) (time.Duration, error) {
	key := renditionStartKey{path: path, size: idx.Size, mtime: idx.ModTime.UnixNano()}
	c.mu.Lock()
	start, ok := c.times[key]
	c.mu.Unlock()
	if ok {
		return start, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, subtitleOriginProbeTimeout)
	defer cancel()
	start, err := SubtitleSourceStartTime(probeCtx, ffmpegPath, path)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	if len(c.times) >= c.max {
		clear(c.times)
	}
	c.times[key] = start
	c.mu.Unlock()
	return start, nil
}

// CueIndexedSubtitleTracks reports which of the source's subtitle tracks, by
// container track number, an on-demand rendition can read: their packets
// are all found through the file's index (embeddedsub.Completeness complete
// or unknown) in a form the reader decodes. It reads the index through the
// cache the renditions use, so a stream that starts next finds it there.
func CueIndexedSubtitleTracks(ctx context.Context, path string) (map[uint64]bool, error) {
	idx, err := renditionIndexes.Index(ctx, path)
	if err != nil {
		return nil, err
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
	return readable, nil
}
