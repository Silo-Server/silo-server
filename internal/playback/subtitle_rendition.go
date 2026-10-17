package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// An HLS subtitle rendition delivers the selected embedded text track inside
// the transcode's own presentation instead of as a whole-file sidecar.
//
// The transcode's ffmpeg process writes the track as one continuous WebVTT
// file per generation (a second output beside the HLS mux, cues on the same
// -copyts source clock as the video). The server cuts that file into segments
// on the synthetic playlist's fixed grid: subtitle segment N carries every
// cue overlapping the source time its video segment covers, and its
// X-TIMESTAMP-MAP ties cue time zero to the video segments' timestamp origin.
// That source time is O + (N-K)*d for a generation that starts at segment K,
// where O is the generation's first video frame: the source clock need not
// start at zero, and an input seek counts from the container's start rather
// than the video's. O is read from the generation's own first segment.
// Demuxing the subtitle as part of the transcode means a cue is on disk once
// the video covering it is, so a subtitle segment is never slower than the
// video segment it accompanies.
//
// The rendition is served through the existing master.m3u8 and segment/
// routes, so every relay (API, proxy token and grant routes, node) carries it
// unchanged: master.m3u8 becomes a multivariant playlist whose variant and
// subtitle playlists are master.m3u8 again, selected by SubtitleRenditionQueryParam.

// SubtitleRenditionQueryParam selects a playlist of a session that publishes
// a subtitle rendition. Absent, master.m3u8 answers the multivariant playlist.
const SubtitleRenditionQueryParam = "silo_rendition"

const (
	subtitleRenditionVideo     = "video"
	subtitleRenditionSubtitles = "subtitles"
	// SubtitleRenditionGroupID is the EXT-X-MEDIA GROUP-ID the variant names
	// in its SUBTITLES attribute.
	SubtitleRenditionGroupID = "subs"
	// SubtitleRenditionName is the rendition's NAME. The plan's inventory
	// already describes the track; the playlist only has to be valid.
	SubtitleRenditionName   = "Subtitles"
	subtitleSegmentPrefix   = "sub_"
	subtitleSegmentExt      = ".vtt"
	subtitleCuePollInterval = 100 * time.Millisecond
	// subtitleOriginProbeTimeout bounds one observation of a generation's
	// origin; it reads a single local segment.
	subtitleOriginProbeTimeout = 5 * time.Second
	// subtitleOriginRetryAfter spaces out observations of a generation's
	// origin after one fails, so a missing ffprobe or unreadable segment costs
	// one subprocess per interval rather than one per subtitle request.
	subtitleOriginRetryAfter = 5 * time.Second
	// subtitleSegmentProductionWait bounds the wait for a segment the live
	// generation is about to produce, matching what its video segment gets
	// after a seek restart. An empty answer is final for a VOD player, so a
	// cue produced a moment later would never be shown.
	subtitleSegmentProductionWait = 30 * time.Second
	// The multivariant playlist needs a BANDWIDTH even for an uncapped
	// encode; a single variant only uses it as a startup hint.
	subtitleRenditionDefaultVideoKbps = 8000
	subtitleRenditionDefaultAudioKbps = 192
)

// SubtitleRenditionCodecSupported reports whether ffmpeg can write a source
// subtitle codec as WebVTT text: every text codec except CEA-608 captions,
// which travel inside the video stream rather than as a subtitle stream.
func SubtitleRenditionCodecSupported(codec string) bool {
	return isTextSubtitleV3(codec) && !strings.Contains(normalizeCodecV3(codec), "608")
}

// SubtitleRenditionExecutable reports whether a transcode with these facts can
// carry a subtitle rendition: an encoded (not copied) video whose playlist is
// the synthetic fixed-grid VOD playlist the subtitle segments align to.
func SubtitleRenditionExecutable(targetVideoCodec string, totalDuration float64, segmentDuration int) bool {
	if segmentDuration <= 0 {
		segmentDuration = defaultSegmentDuration
	}
	return !strings.EqualFold(targetVideoCodec, "copy") && totalDuration > 0 &&
		CanGenerateSyntheticManifest(totalDuration, segmentDuration)
}

// subtitleRenditionActive is the single gate shared by the ffmpeg output, the
// playlists, and segment serving, so they cannot disagree about a recipe.
func subtitleRenditionActive(opts TranscodeOpts) bool {
	return opts.SubtitleRendition && opts.SubtitleTrackIndex >= 0 && !opts.SubtitleBurnIn &&
		SubtitleRenditionCodecSupported(opts.SubtitleCodec) &&
		SubtitleRenditionExecutable(opts.TargetCodecVideo, opts.TotalDuration, opts.SegmentDuration)
}

func subtitleCueFileName(generation uint64) string {
	return fmt.Sprintf("subtitles_%d.vtt", generation)
}

// appendSubtitleRenditionOutputArgs adds the WebVTT cue output after the HLS
// output. -flush_packets writes every cue as soon as it is demuxed, which is
// what lets a subtitle segment be served once the video around it exists.
// -y lets it replace a cue file of the same name: a reconstructed session
// reuses its predecessor's output directory and numbers generations from 0
// again, and without -y ffmpeg would wait at its overwrite prompt on the open
// stdin pipe instead of transcoding.
func appendSubtitleRenditionOutputArgs(args []string, opts TranscodeOpts) []string {
	if !subtitleRenditionActive(opts) {
		return args
	}
	cueFile := opts.subtitleCueFile
	if cueFile == "" {
		cueFile = subtitleCueFileName(0)
	}
	return append(args,
		"-y",
		"-map", fmt.Sprintf("0:s:%d", opts.SubtitleTrackIndex),
		"-c:s", "webvtt",
		"-flush_packets", "1",
		"-f", "webvtt",
		filepath.Join(opts.OutputDir, cueFile),
	)
}

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
	// originFailedAt is when the last observation of origin failed.
	originFailedAt time.Time
}

// startSubtitleSpanLocked records the generation that was just spawned.
// Callers hold s.mu.
func (s *TranscodeSession) startSubtitleSpanLocked(opts TranscodeOpts) {
	if !subtitleRenditionActive(opts) || opts.subtitleCueFile == "" {
		return
	}
	s.subtitleSpans = append(s.subtitleSpans, subtitleCueSpan{file: opts.subtitleCueFile, start: opts.StartSegmentNumber, end: -1})
}

// closeSubtitleSpan freezes the live span at the last segment its process
// finished. It runs after that process has exited and before a replacement
// can write a playlist, so the playlist still belongs to the closed span.
func (s *TranscodeSession) closeSubtitleSpan() {
	head, ok := s.liveProducedHead()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.subtitleSpans {
		if s.subtitleSpans[i].end >= 0 {
			continue
		}
		if !ok || head < s.subtitleSpans[i].start {
			s.subtitleSpans = append(s.subtitleSpans[:i], s.subtitleSpans[i+1:]...)
			return
		}
		s.subtitleSpans[i].end = head
		return
	}
}

// liveProducedHead returns the last segment the current generation's own
// playlist lists. FFmpeg adds an entry only after the segment is complete, and
// a playlist an earlier generation left behind is not this process's progress.
func (s *TranscodeSession) liveProducedHead() (int, bool) {
	s.mu.Lock()
	inherited := s.inheritedManifest
	s.mu.Unlock()
	data, info, err := readManifestFile(filepath.Join(s.outputDir, "stream.m3u8"))
	if err != nil || inherited != nil && sameManifestFile(info, inherited) {
		return 0, false
	}
	head, found := -1, false
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		segNum, parseErr := ParseSegmentNumber(filepath.Base(string(bytes.TrimSpace(line))))
		if parseErr == nil && segNum > head {
			head, found = segNum, true
		}
	}
	return head, found
}

// liveSubtitleFileLocked names the live generation's cue file, or "" between
// a restart closing one span and starting the next. Callers hold s.mu.
func (s *TranscodeSession) liveSubtitleFileLocked() string {
	if n := len(s.subtitleSpans); n > 0 && s.subtitleSpans[n-1].end < 0 {
		return s.subtitleSpans[n-1].file
	}
	return ""
}

// subtitleSegmentSource finds the newest generation whose cue file (a name in
// the output directory) already holds every cue of segment segNum. waitable
// reports that a running or restarting process may still produce it, and
// imminent that it is about to: a restart is under way, or the live generation
// started at or before segNum and its output is within reach of it, the window
// in which a video request waits rather than restarts.
func (s *TranscodeSession) subtitleSegmentSource(segNum int) (file string, ready, waitable, imminent bool) {
	s.mu.Lock()
	sampledLive := s.liveSubtitleFileLocked()
	s.mu.Unlock()
	liveHead, liveOK := s.liveProducedHead()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.liveSubtitleFileLocked() != sampledLive {
		// A restart replaced the live generation while its playlist was
		// read: that head was the previous process's progress, not this one's.
		liveOK = false
	}
	running := s.running
	exited := !running && s.restarting == nil
	waitable = running || s.restarting != nil
	imminent = s.restarting != nil
	for i := len(s.subtitleSpans) - 1; i >= 0; i-- {
		span := s.subtitleSpans[i]
		if segNum < span.start {
			continue
		}
		if span.end < 0 && running {
			head := span.start - 1
			if liveOK {
				head = max(head, liveHead)
			}
			imminent = imminent || segNum <= head+maxSequentialMissingSegments+1
		}
		covered := span.end >= 0 && segNum <= span.end
		if span.end < 0 && liveOK {
			// A running process has demuxed past a segment once the next one
			// is complete. When it has exited, every listed segment is final.
			covered = segNum < liveHead || exited && segNum <= liveHead
		}
		if covered {
			return span.file, true, waitable, imminent
		}
	}
	return "", false, waitable, imminent
}

// SubtitleSegmentVTT returns subtitle segment segNum. A segment whose cues are
// not on disk yet is waited for while ffmpeg produces it, then answered as a
// valid empty WebVTT document: a missing rendition segment must never hold up
// player readiness the way a whole-file sidecar extraction did. The wait is
// brief unless the live generation is about to reach the segment, when it is
// as long as the matching video segment's.
func (s *TranscodeSession) SubtitleSegmentVTT(ctx context.Context, segNum int) ([]byte, error) {
	opts := s.Opts()
	started := time.Now()
	deadline := started.Add(segmentWaitTimeout(opts.SegmentDuration))
	for {
		file, ready, waitable, imminent := s.subtitleSegmentSource(segNum)
		if imminent {
			deadline = started.Add(subtitleSegmentProductionWait)
		}
		if ready {
			data, err := s.readOutputFile(file)
			if err == nil {
				window := s.subtitleSegmentWindow(ctx, file, segNum, renditionSegmentDuration(opts))
				return subtitleSegmentDocument(opts, segNum, window, data), nil
			}
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("read subtitle cues: %w", err)
			}
		}
		if !waitable || !time.Now().Before(deadline) {
			return subtitleSegmentDocument(opts, segNum, subtitleCueWindow{}, nil), nil
		}
		timer := time.NewTimer(subtitleCuePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// subtitleCueWindow places a subtitle segment's window on the cue clock, in
// seconds: it starts offset after origin, where the generation's first video
// segment starts. An observed origin comes from MPEG-TS or fMP4 video
// timestamps; without one, origin is zero and the window is the fixed grid,
// which is right for the common source whose clock starts at zero.
type subtitleCueWindow struct {
	origin, offset float64
	observed       bool
}

// subtitleSegmentWindow returns the window of segment segNum of the
// generation that wrote cue file.
func (s *TranscodeSession) subtitleSegmentWindow(ctx context.Context, file string, segNum int, segDur int) subtitleCueWindow {
	start, origin, ok := s.subtitleSpanOrigin(ctx, file, segNum)
	if !ok {
		return subtitleCueWindow{offset: float64(segNum * segDur)}
	}
	return subtitleCueWindow{origin: origin, offset: float64((segNum - start) * segDur), observed: true}
}

// subtitleSpanOrigin returns the start segment and observed origin of the span
// that wrote cue file. The origin is read once from the generation's first
// video segment, or from segNum when retention already pruned the first.
// A segment is read only while it is still the one that generation wrote: no
// restart is under way, and no later generation has rewritten it.
// Observations run one at a time per session, outside s.mu, and each span
// keeps its result, so concurrent requests share one ffprobe run.
func (s *TranscodeSession) subtitleSpanOrigin(ctx context.Context, file string, segNum int) (int, float64, bool) {
	// lookup returns the span that wrote file, the session's options, and the
	// segments of that span that may be read now; -1 marks one that may not.
	lookup := func() (span subtitleCueSpan, opts TranscodeOpts, first, fallback int) {
		s.mu.Lock()
		defer s.mu.Unlock()
		i := slices.IndexFunc(s.subtitleSpans, func(candidate subtitleCueSpan) bool { return candidate.file == file })
		if i < 0 {
			return subtitleCueSpan{}, TranscodeOpts{}, -1, -1
		}
		span, opts, first, fallback = s.subtitleSpans[i], s.opts, -1, -1
		if span.originKnown || s.restarting != nil ||
			!span.originFailedAt.IsZero() && time.Since(span.originFailedAt) < subtitleOriginRetryAfter {
			return span, opts, -1, -1
		}
		if !s.subtitleSegmentRewrittenLocked(i, span.start) {
			first = span.start
		}
		if segNum > span.start && !s.subtitleSegmentRewrittenLocked(i, segNum) {
			fallback = segNum
		}
		return span, opts, first, fallback
	}
	span, _, first, fallback := lookup()
	if first < 0 && fallback < 0 {
		return span.start, span.origin, span.originKnown
	}
	s.subtitleOriginMu.Lock()
	defer s.subtitleOriginMu.Unlock()
	span, opts, first, fallback := lookup()
	if first < 0 && fallback < 0 {
		return span.start, span.origin, span.originKnown
	}

	// The observation outlives the request that started it: concurrent
	// requests wait on it, and its result is kept for the generation.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subtitleOriginProbeTimeout)
	observed, err := first, os.ErrNotExist
	var pts float64
	if first >= 0 {
		pts, err = probeSegmentVideoPTS(probeCtx, opts, s.outputDir, first)
	}
	if errors.Is(err, os.ErrNotExist) && fallback >= 0 {
		observed = fallback
		pts, err = probeSegmentVideoPTS(probeCtx, opts, s.outputDir, fallback)
	}
	cancel()
	origin := pts - float64(subtitleSegmentPTSOffset90k(opts))/90_000 -
		float64((observed-span.start)*renditionSegmentDuration(opts))

	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.subtitleSpans, func(candidate subtitleCueSpan) bool {
		return candidate.file == file && candidate.start == span.start
	})
	if i < 0 || s.restarting != nil || s.subtitleSegmentRewrittenLocked(i, observed) {
		// A restart began while the segment was read; it may not have been
		// this generation's any more.
		return span.start, 0, false
	}
	if err != nil {
		s.subtitleSpans[i].originFailedAt = time.Now()
		return span.start, 0, false
	}
	s.subtitleSpans[i].origin, s.subtitleSpans[i].originKnown = origin, true
	return span.start, origin, true
}

// subtitleSegmentRewrittenLocked reports whether a generation after span i
// has replaced segment: a restart deletes its predecessors' output from its
// own start onward. Callers hold s.mu.
func (s *TranscodeSession) subtitleSegmentRewrittenLocked(i, segment int) bool {
	for _, later := range s.subtitleSpans[i+1:] {
		if later.start <= segment {
			return true
		}
	}
	return false
}

// readOutputFile reads a file the session's FFmpeg wrote, confined to its
// output directory.
func (s *TranscodeSession) readOutputFile(name string) ([]byte, error) {
	root, err := os.OpenRoot(s.outputDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(name)
}

// ServeSubtitleSegment writes subtitle segment segNum.
func (s *TranscodeSession) ServeSubtitleSegment(w http.ResponseWriter, r *http.Request, segNum int) {
	body, err := s.SubtitleSegmentVTT(r.Context(), segNum)
	if err != nil && r.Context().Err() != nil {
		return
	}
	if err != nil {
		http.Error(w, "subtitle segment unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// subtitleSegmentPTSOffset90k is the shift the video segments add to the
// source clock (see HLSMPEGTSTimestampOffset90k); fMP4 keeps the source clock.
func subtitleSegmentPTSOffset90k(opts TranscodeOpts) int64 {
	if HLSOutputContainer(opts) == OutputContainerMPEGTS {
		return HLSMPEGTSTimestampOffset90k
	}
	return 0
}

// renditionSegmentDuration is the session's segment duration in seconds.
func renditionSegmentDuration(opts TranscodeOpts) int {
	if opts.SegmentDuration <= 0 {
		return defaultSegmentDuration
	}
	return opts.SegmentDuration
}

const (
	// subtitleCueClockWrap90k is the MPEG-TS timestamp range.
	subtitleCueClockWrap90k = int64(1) << 33
	// subtitleEpochCueLead90k is how long before a generation's origin its
	// first cue may start: a cue already showing at a seek point is written
	// with its own start time.
	subtitleEpochCueLead90k = int64(60 * 60 * 90_000)
)

// subtitleWindowEpoch moves a generation's origin observed from MPEG-TS video,
// whose timestamps wrap at 2^33 ticks (about 26.5 h), onto the cue clock,
// which does not wrap: a source whose clock passes the rollover (a broadcast
// capture) keeps counting in its cue file while its video segments start
// over. A generation starts no later than its first cue, give or take a cue
// already showing, so the origin takes the latest epoch that keeps it there,
// however long the first cue takes to arrive. The epoch is the origin's, so
// every window of the generation keeps it.
func subtitleWindowEpoch(origin90k, firstCue90k int64) int64 {
	if ahead := firstCue90k + subtitleEpochCueLead90k - origin90k; ahead >= subtitleCueClockWrap90k {
		origin90k += ahead / subtitleCueClockWrap90k * subtitleCueClockWrap90k
	}
	return origin90k
}

// subtitleRenditionCue is one complete cue of a generation's cue file.
type subtitleRenditionCue struct {
	from, to int64 // milliseconds
	text     string
}

// parseSubtitleRenditionCues reads the complete cues of a cue file, skipping
// the one ffmpeg may still be writing.
func parseSubtitleRenditionCues(cues []byte) []subtitleRenditionCue {
	text := strings.ReplaceAll(string(cues), "\r\n", "\n")
	if !strings.HasSuffix(text, "\n") {
		// The final cue is still being written.
		if cut := strings.LastIndex(text, "\n\n"); cut >= 0 {
			text = text[:cut]
		} else {
			text = ""
		}
	}
	var parsed []subtitleRenditionCue
	for block := range strings.SplitSeq(text, "\n\n") {
		block = strings.Trim(block, "\n")
		lines := strings.Split(block, "\n")
		for i, line := range lines {
			left, right, found := strings.Cut(line, " --> ")
			if !found {
				continue
			}
			fields := strings.Fields(right)
			if len(fields) == 0 || i == len(lines)-1 {
				break
			}
			from, err := subtitleCueTimestampMS(left)
			if err != nil {
				break
			}
			to, err := subtitleCueTimestampMS(fields[0])
			if err != nil {
				break
			}
			parsed = append(parsed, subtitleRenditionCue{from: from, to: to, text: strings.Join(lines[i:], "\n")})
			break
		}
	}
	return parsed
}

// subtitleSegmentDocument cuts one segment out of a generation's cue file:
// every cue overlapping the window's d seconds. Cues keep their absolute
// source times and settings; a cue that spans segments is repeated in each,
// as HLS expects, and players de-duplicate it.
func subtitleSegmentDocument(opts TranscodeOpts, segNum int, window subtitleCueWindow, cues []byte) []byte {
	segDur := renditionSegmentDuration(opts)
	parsed := parseSubtitleRenditionCues(cues)
	origin90k := int64(math.Round(window.origin * 90_000))
	if window.observed && HLSOutputContainer(opts) == OutputContainerMPEGTS && len(parsed) > 0 {
		origin90k = subtitleWindowEpoch(origin90k, parsed[0].from*90)
	}
	start90k := origin90k + int64(math.Round(window.offset*90_000))
	end90k := start90k + int64(segDur)*90_000
	if segNum >= syntheticSegmentCount(opts.TotalDuration, segDur)-1 {
		end90k = math.MaxInt64
	}

	var out bytes.Buffer
	out.WriteString("WEBVTT\n")
	fmt.Fprintf(&out, "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:%d\n", subtitleSegmentPTSOffset90k(opts)%(1<<33))
	for _, cue := range parsed {
		if cue.to*90 <= start90k || cue.from*90 >= end90k {
			continue
		}
		out.WriteByte('\n')
		out.WriteString(cue.text)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// subtitleCueTimestampMS parses a WebVTT [HH:]MM:SS.mmm timestamp.
func subtitleCueTimestampMS(value string) (int64, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("invalid cue timestamp %q", value)
	}
	ms := int64(0)
	for i, part := range parts {
		if i == len(parts)-1 {
			seconds, err := strconv.ParseFloat(part, 64)
			if err != nil || seconds < 0 {
				return 0, fmt.Errorf("invalid cue timestamp %q", value)
			}
			return ms*60_000 + int64(math.Round(seconds*1000)), nil
		}
		unit, err := strconv.ParseInt(part, 10, 64)
		if err != nil || unit < 0 {
			return 0, fmt.Errorf("invalid cue timestamp %q", value)
		}
		ms = ms*60 + unit
	}
	return ms, nil
}

// ParseSubtitleSegmentNumber reports the segment number of a subtitle
// rendition segment name (sub_00012.vtt).
func ParseSubtitleSegmentNumber(name string) (int, bool) {
	name = filepath.Base(name)
	if !strings.HasPrefix(name, subtitleSegmentPrefix) || !strings.HasSuffix(name, subtitleSegmentExt) {
		return 0, false
	}
	digits := strings.TrimSuffix(strings.TrimPrefix(name, subtitleSegmentPrefix), subtitleSegmentExt)
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, false
	}
	segNum, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	return segNum, true
}

// HasSubtitleRendition reports whether this session publishes a subtitle
// rendition, so master.m3u8 is a multivariant playlist.
func (s *TranscodeSession) HasSubtitleRendition() bool {
	return s.copyPlan == nil && subtitleRenditionActive(s.Opts())
}

// buildSubtitleRenditionPlaylist answers master.m3u8 for a session with a
// subtitle rendition: the multivariant playlist, or the variant or subtitle
// media playlist it points at.
func (s *TranscodeSession) buildSubtitleRenditionPlaylist(segPrefix, rawQuery string) []byte {
	rendition, query := splitSubtitleRenditionQuery(rawQuery)
	switch rendition {
	case subtitleRenditionVideo:
		return s.GenerateFullManifest(segPrefix, query)
	case subtitleRenditionSubtitles:
		return s.generateSyntheticPlaylist(segPrefix, query, subtitleSegmentPrefix, subtitleSegmentExt, false)
	default:
		return subtitleRenditionMultivariantPlaylist(s.Opts(), query)
	}
}

// subtitleRenditionMultivariantPlaylist lists the transcode as one variant
// whose SUBTITLES group holds the selected track. Both URIs are master.m3u8
// relative to itself, so the playlist works behind every relay path.
func subtitleRenditionMultivariantPlaylist(opts TranscodeOpts, query string) []byte {
	uri := func(rendition string) string {
		value := "master.m3u8?" + SubtitleRenditionQueryParam + "=" + rendition
		if query != "" {
			value += "&" + query
		}
		return value
	}
	videoKbps := opts.TargetBitrateKbps
	if videoKbps <= 0 {
		videoKbps = subtitleRenditionDefaultVideoKbps
	}
	audioKbps := opts.TargetAudioBitrateKbps
	if audioKbps <= 0 {
		audioKbps = subtitleRenditionDefaultAudioKbps
	}

	var buf bytes.Buffer
	buf.WriteString("#EXTM3U\n")
	buf.WriteString("#EXT-X-VERSION:3\n")
	buf.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	fmt.Fprintf(&buf, "#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=%q,NAME=%q,DEFAULT=YES,AUTOSELECT=YES,FORCED=NO,URI=%q\n",
		SubtitleRenditionGroupID, SubtitleRenditionName, uri(subtitleRenditionSubtitles))
	fmt.Fprintf(&buf, "#EXT-X-STREAM-INF:BANDWIDTH=%d,SUBTITLES=%q\n", (videoKbps+audioKbps)*1000, SubtitleRenditionGroupID)
	buf.WriteString(uri(subtitleRenditionVideo) + "\n")
	return buf.Bytes()
}

// splitSubtitleRenditionQuery removes SubtitleRenditionQueryParam from a raw
// query, keeping every other parameter byte-for-byte.
func splitSubtitleRenditionQuery(rawQuery string) (rendition, rest string) {
	if rawQuery == "" {
		return "", ""
	}
	kept := make([]string, 0, strings.Count(rawQuery, "&")+1)
	for part := range strings.SplitSeq(rawQuery, "&") {
		if key, value, _ := strings.Cut(part, "="); key == SubtitleRenditionQueryParam {
			rendition = value
			continue
		}
		if part != "" {
			kept = append(kept, part)
		}
	}
	return rendition, strings.Join(kept, "&")
}
