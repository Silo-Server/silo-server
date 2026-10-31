package playback

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// subtitleCueSpan is one ffmpeg generation's cue file and the segments it has
// demuxed past. A seek restart starts a new span; older spans stay readable so
// a backward seek into output an earlier generation produced keeps its cues.
type subtitleCueSpan struct {
	file  string
	start int
	// end is the last segment the generation finished, or -1 while it is the
	// live generation, whose progress is read from its own playlist.
	end int
	// origin is the cue-clock time, in seconds, at which segment start begins:
	// the first video frame the generation produced, less the mux shift the
	// X-TIMESTAMP-MAP declares. Valid when originKnown.
	origin      float64
	originKnown bool
	// muxShift is how much later the HLS mux put this generation's video than
	// the cues' clock (copyMuxShiftsVideo), in seconds, observed with origin.
	// The X-TIMESTAMP-MAP adds it so each cue shows with its own frame.
	muxShift float64
	// videoStartMisses counts failed reads of the video start the generation
	// records for muxShift (noteSubtitleVideoStartMiss).
	videoStartMisses int
	// originFailedAt is when the last observation of origin failed.
	originFailedAt time.Time
	// startTime is the cue-clock time segment start is taken to begin at while
	// origin is unobserved: the fixed grid's for an encode, the resolved seek
	// anchor for a copy remux.
	startTime float64
	// durations are the durations of segments start..end that a closed
	// generation on FFmpeg's own playlist listed, and final reports that its
	// playlist ended the media. Nil for the live generation, whose playlist is
	// read instead, and for playlists with known durations.
	durations []float64
	final     bool
}

// startSubtitleSpanLocked records the generation that was just spawned.
// Callers hold s.mu.
func (s *TranscodeSession) startSubtitleSpanLocked(opts TranscodeOpts) {
	if !subtitleRenditionActive(opts) || opts.subtitleCueFile == "" {
		return
	}
	s.subtitleSpans = append(s.subtitleSpans, subtitleCueSpan{
		file: opts.subtitleCueFile, start: opts.StartSegmentNumber, end: -1, startTime: subtitleSpanStartTime(opts),
	})
}

// subtitleSpanStartTime is where a generation's first segment is expected to
// start on the cue clock before its first video frame is observed.
func subtitleSpanStartTime(opts TranscodeOpts) float64 {
	if !strings.EqualFold(opts.TargetCodecVideo, "copy") {
		return float64(opts.StartSegmentNumber * renditionSegmentDuration(opts))
	}
	if opts.CopySeekAnchorResolved {
		return opts.StreamOriginSeconds
	}
	return opts.SeekSeconds
}

// closeSubtitleSpan freezes the live span at the last segment its process
// finished. It runs after that process has exited and before a replacement
// can write a playlist, so the playlist still belongs to the closed span.
//
// A generation on FFmpeg's own playlist keeps the durations that playlist
// listed, since the next generation replaces it.
func (s *TranscodeSession) closeSubtitleSpan() {
	data, _ := s.liveGenerationPlaylist()
	_, head, ok := playlistSegmentRange(data)
	followsFFmpeg := followsFFmpegPlaylist(s.Opts(), s.copyPlan)
	s.mu.Lock()
	for i := range s.subtitleSpans {
		span := &s.subtitleSpans[i]
		if span.end >= 0 {
			continue
		}
		if !ok || head < span.start {
			s.subtitleSpans = append(s.subtitleSpans[:i], s.subtitleSpans[i+1:]...)
			break
		}
		span.end = head
		if followsFFmpeg {
			durations, final, own := generationPlaylistTimeline(data, span.start)
			if !own {
				// Its segments' times are unknown, so its cues can't be cut.
				s.subtitleSpans = append(s.subtitleSpans[:i], s.subtitleSpans[i+1:]...)
				break
			}
			span.durations, span.final = durations, final
		}
		break
	}
	s.mu.Unlock()
	s.saveSubtitleSpans()
}

// liveGenerationPlaylist returns the current generation's own playlist. FFmpeg
// adds an entry only after the segment is complete, and a playlist an earlier
// generation left behind is not this process's progress.
func (s *TranscodeSession) liveGenerationPlaylist() ([]byte, bool) {
	s.mu.Lock()
	inherited := s.inheritedManifest
	s.mu.Unlock()
	data, err := readManifestFile(filepath.Join(s.outputDir, "stream.m3u8"), inherited)
	if err != nil {
		return nil, false
	}
	return data, true
}

// liveProducedHead returns the last segment the current generation's own
// playlist lists.
func (s *TranscodeSession) liveProducedHead() (int, bool) {
	data, ok := s.liveGenerationPlaylist()
	if !ok {
		return 0, false
	}
	_, head, ok := playlistSegmentRange(data)
	return head, ok
}

// generationPlaylistTimeline reads the playlist a generation that started at
// segment start wrote: the durations of the segments it lists, in order, and
// whether it ended the media. A playlist that does not list start, start+1, ...
// from its first entry was written by another generation.
func generationPlaylistTimeline(data []byte, start int) (durations []float64, final, ok bool) {
	timeline, err := parseManifestTimeline(data)
	if err != nil || len(timeline.entries) == 0 {
		return nil, false, false
	}
	durations = make([]float64, 0, len(timeline.entries))
	for i, entry := range timeline.entries {
		if entry.number != start+i || !(entry.duration > 0) {
			return nil, false, false
		}
		durations = append(durations, entry.duration)
	}
	return durations, bytes.Contains(data, []byte("#EXT-X-ENDLIST")), true
}

// playlistSegmentRange returns the lowest and highest segment numbers a
// playlist lists.
func playlistSegmentRange(data []byte) (first, head int, found bool) {
	first, head = -1, -1
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		segNum, parseErr := ParseSegmentNumber(filepath.Base(string(bytes.TrimSpace(line))))
		if parseErr != nil {
			continue
		}
		if !found || segNum < first {
			first = segNum
		}
		head, found = max(head, segNum), true
	}
	return first, head, found
}

// subtitleSpanRecordName is the record of a session's spans in its output
// directory. Spans otherwise live only in memory, and a session rebuilt from
// its recipe on the same directory (after the server or node that ran it went
// away without removing it) serves the earlier processes' video segments from
// disk: without the record their subtitle segments would come back empty.
const subtitleSpanRecordName = "subtitle_spans.json"

// subtitleSpanRecord is the content of subtitleSpanRecordName. Track and
// SegmentDuration tie it to the recipe whose grid its segment numbers use.
type subtitleSpanRecord struct {
	Track           int                     `json:"track"`
	SegmentDuration int                     `json:"segment_duration"`
	Spans           []subtitleSpanRecordRow `json:"spans"`
}

type subtitleSpanRecordRow struct {
	File  string `json:"file"`
	Start int    `json:"start"`
	// End is -1 for the generation that was live when the record was written.
	End int `json:"end"`
	// InheritedPlaylist identifies the stream.m3u8 an earlier generation left
	// when the live generation started, so a rebuild can tell whether the
	// playlist on disk is the live generation's progress.
	InheritedPlaylist *subtitleSpanRecordPlaylist `json:"inherited_playlist,omitempty"`
	// Origin is the span's observed origin. A rebuild may not be able to
	// observe it again: a later generation that started at or before the
	// span's start makes its segments unreadable as the span's own.
	Origin *float64 `json:"origin,omitempty"`
	// MuxShift is the span's muxShift, observed with Origin.
	MuxShift float64 `json:"mux_shift,omitempty"`
	// StartTime is the span's startTime. Absent in records that predate it,
	// which only encodes wrote, whose start time follows from Start.
	StartTime *float64 `json:"start_time,omitempty"`
	// Durations and Final are a closed span's durations and final, for a
	// copy remux on FFmpeg's own playlist.
	Durations []float64 `json:"durations,omitempty"`
	Final     bool      `json:"final,omitempty"`
}

type subtitleSpanRecordPlaylist struct {
	Size    int64 `json:"size"`
	ModTime int64 `json:"mod_time_ns"`
}

func recordedPlaylist(info os.FileInfo) *subtitleSpanRecordPlaylist {
	if info == nil {
		return nil
	}
	return &subtitleSpanRecordPlaylist{Size: info.Size(), ModTime: info.ModTime().UnixNano()}
}

func (p *subtitleSpanRecordPlaylist) matches(info os.FileInfo) bool {
	return p != nil && info.Size() == p.Size && info.ModTime().UnixNano() == p.ModTime
}

// saveSubtitleSpans writes the session's spans to its output directory. It
// runs whenever a generation starts or closes and when a span's origin is
// observed. The snapshot is taken under
// subtitleRecordMu, so the last write always holds the latest spans. A failed
// write only costs a later rebuild the earlier cues.
func (s *TranscodeSession) saveSubtitleSpans() {
	if s.outputDir == "" {
		return
	}
	s.subtitleRecordMu.Lock()
	defer s.subtitleRecordMu.Unlock()
	s.mu.Lock()
	if s.outputRemoved {
		s.mu.Unlock()
		return
	}
	opts := s.opts
	record := subtitleSpanRecord{Track: opts.SubtitleTrackIndex, SegmentDuration: renditionSegmentDuration(opts)}
	for _, span := range s.subtitleSpans {
		startTime := span.startTime
		row := subtitleSpanRecordRow{File: span.file, Start: span.start, End: span.end, StartTime: &startTime, Durations: span.durations, Final: span.final}
		if span.originKnown {
			row.Origin, row.MuxShift = &span.origin, span.muxShift
		}
		if span.end < 0 {
			row.InheritedPlaylist = recordedPlaylist(s.inheritedManifest)
		}
		record.Spans = append(record.Spans, row)
	}
	s.mu.Unlock()
	if !subtitleRenditionActive(opts) || len(record.Spans) == 0 {
		return
	}
	data, err := json.Marshal(record)
	if err == nil {
		err = writeFileAtomic(s.outputDir, subtitleSpanRecordName, data)
	}
	if err != nil {
		slog.Warn("record subtitle rendition spans", "component", "playback",
			"playback_session_id", opts.SessionID, "error", err)
	}
}

// writeFileAtomic replaces dir/name through a temporary file and a rename, so
// a crash never leaves a partial file. Both names resolve inside an os.Root of
// dir, so neither can leave it.
func writeFileAtomic(dir, name string, data []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tmpName := "." + name + "." + rand.Text() + ".tmp"
	tmp, err := root.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(tmpName, name)
	}
	if err != nil {
		_ = root.Remove(tmpName)
	}
	return err
}

// loadSubtitleSpans reads the spans an earlier session recorded in the output
// directory this recipe reuses, and the first generation number whose cue file
// name none of them used. It runs before the new process starts, while the
// playlist on disk is still the earlier process's. A span is kept only while
// it can serve its cues: its cue file exists and, for the generation that was
// live when the earlier session went away, its own playlist on disk shows how
// far it got. On FFmpeg's own playlist a span also needs its segments'
// durations. plan is the session's keyframe plan, if any.
func loadSubtitleSpans(opts TranscodeOpts, plan *copySegmentPlan) ([]subtitleCueSpan, uint64) {
	root, err := os.OpenRoot(opts.OutputDir)
	if err != nil {
		return nil, 0
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile(subtitleSpanRecordName)
	if err != nil {
		return nil, 0
	}
	var record subtitleSpanRecord
	if json.Unmarshal(data, &record) != nil {
		return nil, 0
	}
	var spans []subtitleCueSpan
	next := uint64(0)
	followsFFmpeg := followsFFmpegPlaylist(opts, plan)
	for i, row := range record.Spans {
		generation, ok := subtitleCueFileGeneration(row.File)
		if !ok {
			continue
		}
		next = max(next, generation+1)
		if record.Track != opts.SubtitleTrackIndex || record.SegmentDuration != renditionSegmentDuration(opts) || row.Start < 0 {
			continue
		}
		if _, err := root.Stat(row.File); err != nil {
			continue
		}
		span := subtitleCueSpan{file: row.File, start: row.Start, end: row.End, durations: row.Durations, final: row.Final}
		if row.StartTime != nil {
			span.startTime = *row.StartTime
		} else {
			span.startTime = float64(row.Start * renditionSegmentDuration(opts))
		}
		if span.end < 0 {
			if i != len(record.Spans)-1 || !recordedLiveSpanProgress(root, row, followsFFmpeg, &span) {
				continue
			}
		}
		if followsFFmpeg && len(span.durations) != span.end-span.start+1 {
			// Its segments' times are unknown, so its cues can't be cut.
			continue
		}
		if span.end >= row.Start {
			if row.Origin != nil {
				span.origin, span.originKnown, span.muxShift = *row.Origin, true, row.MuxShift
			}
			spans = append(spans, span)
		}
	}
	return spans, next
}

// recordedLiveSpanProgress fills in how far a generation that never closed
// got, from its own playlist on disk, and reports whether that playlist is
// its own. A generation's own playlist starts at its first segment; one that
// starts elsewhere was written by another generation, for example after a
// failed record write left this row stale.
func recordedLiveSpanProgress(root *os.Root, row subtitleSpanRecordRow, followsFFmpeg bool, span *subtitleCueSpan) bool {
	data := recordedLivePlaylist(root, row)
	first, head, ok := playlistSegmentRange(data)
	if !ok || first != row.Start {
		return false
	}
	span.end = head
	if followsFFmpeg {
		durations, final, own := generationPlaylistTimeline(data, row.Start)
		if !own {
			return false
		}
		span.durations, span.final = durations, final
	}
	return true
}

// recordedLivePlaylist returns the playlist on disk when it may be the one a
// generation that never closed wrote, or nil when it is the playlist that
// generation inherited.
func recordedLivePlaylist(root *os.Root, row subtitleSpanRecordRow) []byte {
	f, err := root.Open("stream.m3u8")
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || row.InheritedPlaylist.matches(info) {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	return data
}
