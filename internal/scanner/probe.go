package scanner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/mediaprobe"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrPrimaryVideoNotFound reports that FFprobe completed successfully but did
// not describe a playable primary video stream.
var ErrPrimaryVideoNotFound = mediaprobe.ErrPrimaryVideoNotFound

// ffprobeOutput represents the top-level JSON output from ffprobe.
type ffprobeOutput struct {
	Format   ffprobeFormat    `json:"format"`
	Streams  []ffprobeStream  `json:"streams"`
	Chapters []ffprobeChapter `json:"chapters"`
}

type ffprobeScalarString = mediaprobe.ScalarString

// ffprobeFormat represents the "format" section of ffprobe JSON output.
type ffprobeFormat struct {
	Filename       string            `json:"filename"`
	FormatName     string            `json:"format_name"`
	FormatLongName string            `json:"format_long_name"`
	StartTime      string            `json:"start_time"`
	Duration       string            `json:"duration"`
	Size           string            `json:"size"`
	BitRate        string            `json:"bit_rate"`
	Tags           map[string]string `json:"tags"`
}

type ffprobeStream = mediaprobe.Stream

type ffprobeChapter struct {
	ID        int                 `json:"id"`
	Start     ffprobeScalarString `json:"start"`
	End       ffprobeScalarString `json:"end"`
	TimeBase  string              `json:"time_base"`
	StartTime ffprobeScalarString `json:"start_time"`
	EndTime   ffprobeScalarString `json:"end_time"`
	Tags      map[string]string   `json:"tags"`
}

type ffprobeSideData = mediaprobe.SideData
type ffprobeDisp = mediaprobe.Disposition

// ProbeFile runs ffprobe on the given file and returns parsed ProbeData.
// ffprobePath is the path to the ffprobe binary. filePath is the media file to probe.
func ProbeFile(ctx context.Context, ffprobePath string, filePath string) (*ProbeData, error) {
	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-show_chapters",
		filePath,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed for %s: %w", filePath, err)
	}

	var raw ffprobeOutput
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("ffprobe JSON parse failed for %s: %w", filePath, err)
	}

	probe := convertProbeData(&raw)
	if probe.Duration == 0 {
		if frameRate, hasVideo := primaryVideoFrameRate(raw.Streams); hasVideo {
			// A failed or empty packet scan must not discard the codec and
			// track metadata that already parsed successfully: callers persist
			// the partial probe, and the repair layer retries rows whose
			// duration is still unknown.
			if duration, packetErr := probeVideoPacketDuration(ctx, ffprobePath, filePath, frameRate); packetErr == nil && duration > 0 {
				probe.Duration = duration
			}
		}
	}

	return probe, nil
}

// ProbePrimaryVideoTrack runs a bounded metadata-only FFprobe and returns the
// first playable video stream using the scanner's authoritative normalization.
// It does not scan packets or persist any result.
func ProbePrimaryVideoTrack(ctx context.Context, ffprobePath, filePath string) (models.VideoTrack, error) {
	return mediaprobe.ProbePrimaryVideoTrack(ctx, ffprobePath, filePath)
}

// FFprobePathFromFFmpeg derives the sibling ffprobe binary path from a configured ffmpeg path.
func FFprobePathFromFFmpeg(ffmpegPath string) string {
	return mediaprobe.FFprobePathFromFFmpeg(ffmpegPath)
}

// convertProbeData transforms raw ffprobe JSON output into ProbeData.
func convertProbeData(raw *ffprobeOutput) *ProbeData {
	pd := &ProbeData{
		Container: detectContainer(raw.Format.FormatName),
	}

	if duration, ok := durationFromProbeMetadata(raw); ok {
		pd.Duration = duration
	}

	// Parse bitrate from format (bps to kbps).
	if raw.Format.BitRate != "" {
		if br, err := strconv.Atoi(raw.Format.BitRate); err == nil {
			pd.Bitrate = br / 1000
		}
	}

	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			// Embedded cover art is a "video" stream to ffprobe, but it is a
			// still image, not a playable video track. Recording it as one
			// misreports the file twice: an audio file with a cover picks up a
			// video track and stops satisfying MediaFile.IsAudioOnly, so the
			// planner routes an audiobook through the video path; and when the
			// picture is ordered ahead of the real stream, the flat
			// codec_video/resolution/hdr columns describe the poster instead of
			// the movie.
			if !mediaprobe.IsPrimaryVideoStream(s) {
				continue
			}
			normalized := mediaprobe.NormalizeVideoTrack(s)
			track := VideoTrackInfo{
				Title:               normalized.Title,
				Codec:               normalized.Codec,
				DolbyVision:         normalized.DolbyVision,
				DVProfile:           normalized.DVProfile,
				DVLevel:             normalized.DVLevel,
				DVBLCompatID:        normalized.DVBLCompatID,
				DVConfigPresent:     normalized.DVConfigPresent,
				DVBLCompatIDPresent: normalized.DVBLCompatIDPresent,
				DVBLPresent:         normalized.DVBLPresent,
				DVRPUPresent:        normalized.DVRPUPresent,
				DVELPresent:         normalized.DVELPresent,
				DVEnhancementLayer:  normalized.DVEnhancementLayer,
				HDR10Plus:           normalized.HDR10Plus,
				Profile:             normalized.Profile,
				Level:               normalized.Level,
				Width:               normalized.Width,
				Height:              normalized.Height,
				AspectRatio:         normalized.AspectRatio,
				Interlaced:          normalized.Interlaced,
				FrameRate:           normalized.FrameRate,
				Bitrate:             normalized.Bitrate,
				VideoRange:          normalized.VideoRange,
				VideoRangeType:      normalized.VideoRangeType,
				ColorRange:          normalized.ColorRange,
				ColorPrimaries:      normalized.ColorPrimaries,
				ColorSpace:          normalized.ColorSpace,
				ColorTransfer:       normalized.ColorTransfer,
				BitDepth:            normalized.BitDepth,
				PixelFormat:         normalized.PixelFormat,
				ReferenceFrames:     normalized.ReferenceFrames,
			}
			pd.VideoTracks = append(pd.VideoTracks, track)
			if pd.CodecVideo == "" {
				pd.CodecVideo = s.CodecName
				pd.Resolution = mapResolution(s.Width, s.Height)
				pd.HDR = isHDR(s.ColorTransfer) || normalized.DVProfile > 0 || track.HDR10Plus
			}
		case "audio":
			track := AudioTrackInfo{
				Title:         firstNonEmpty(s.Tags["title"], s.CodecLongName, strings.ToUpper(s.CodecName)),
				EmbeddedTitle: s.Tags["title"],
				Language:      lang.Canonical(s.Tags["language"]),
				Codec:         s.CodecName,
				Profile:       s.Profile,
				Layout:        s.ChannelLayout,
				Channels:      s.Channels,
				Bitrate:       parseNumeric(s.BitRate) / 1000,
				SampleRate:    parseNumeric(s.SampleRate),
				BitDepth:      parseBitDepth(s),
				Default:       s.Disposition.Default == 1,
			}
			pd.AudioTracks = append(pd.AudioTracks, track)
			if pd.CodecAudio == "" {
				pd.CodecAudio = s.CodecName
				pd.AudioChannels = s.Channels
			}
		case "subtitle":
			track := SubtitleTrackInfo{
				Index:           s.Index,
				Codec:           s.CodecName,
				Language:        lang.Canonical(s.Tags["language"]),
				Title:           firstNonEmpty(s.Tags["title"], strings.ToUpper(s.CodecName)),
				EmbeddedTitle:   s.Tags["title"],
				Resolution:      subtitleResolutionLabel(s),
				Forced:          s.Disposition.Forced == 1,
				Default:         s.Disposition.Default == 1,
				HearingImpaired: dispositionFlag(s.Tags, "hearing_impaired"),
			}
			pd.SubtitleTracks = append(pd.SubtitleTracks, track)
		}
	}

	pd.Chapters = normalizeChapters(raw.Chapters, pd.Duration)
	pd.FormatTags = normalizeFormatTags(raw.Format.Tags)

	return pd
}

const (
	maxReasonableMediaDurationSeconds = 100_000
	// Corroborated metadata and packet-derived durations have stronger evidence
	// than a lone container timestamp, so they may use the same bounded ceiling
	// as long-form audio. This supports multi-day video without accepting the
	// multi-year timelines seen in malformed containers.
	maxValidatedMediaDurationSeconds = 1_000_000
	// Audio-only files (audiobooks, podcasts) legitimately exceed the video
	// ceiling, but still need a cap so malformed containers cannot persist
	// multi-year durations.
	maxReasonableAudioDurationSeconds = maxValidatedMediaDurationSeconds

	longVideoDurationAbsoluteToleranceSeconds = 1
	longVideoDurationRelativeTolerance        = 0.001
)

// A video duration is implausible when it is either far too short in absolute
// terms, or when it implies a bitrate no real medium reaches. Both are
// signatures of malformed container timestamps (and of the legacy probe that
// divided large durations by one million).
//
// The absolute rule alone cannot catch a feature film that probed as, say, 61
// seconds — well past the floor, yet still wrong by two orders of magnitude.
// Size and duration together pin an implied bitrate, which separates the two
// cases the absolute rule conflates: a genuine short clip has an ordinary
// bitrate, while a 100 GB file claiming 61 seconds implies ~13 Gbps.
//
// The ceiling sits far above any real medium — UHD Blu-ray peaks near
// 150 Mbps and ProRes 4444 XQ at 4K near 500 Mbps — so legitimate content
// cannot trip it. This also makes the rule safer than the absolute floor
// alone, which false-positives on a genuine high-bitrate short.
//
// The shape is shared with the repair triggers in probe_repair.go and
// scanner.go so the probe parser and the repair layers cannot drift apart.
const (
	implausiblyShortVideoMaxSeconds = 10
	implausiblyShortVideoMinBytes   = 100 * 1024 * 1024
	implausibleVideoBitrateBps      = 1_000_000_000
)

func videoDurationImplausible(durationSeconds float64, sizeBytes int64, hasVideo bool) bool {
	if !hasVideo || durationSeconds <= 0 || sizeBytes <= 0 {
		return false
	}
	if durationSeconds <= implausiblyShortVideoMaxSeconds && sizeBytes >= implausiblyShortVideoMinBytes {
		return true
	}
	return impliedBitrateBps(sizeBytes, durationSeconds) > implausibleVideoBitrateBps
}

// impliedBitrateBps is the bitrate a file's size and duration imply. Callers
// use it as a duration-sanity signal, not as a real bitrate estimate: it
// counts container overhead and every stream, which is precisely what makes it
// a conservative upper bound.
func impliedBitrateBps(sizeBytes int64, durationSeconds float64) float64 {
	return float64(sizeBytes) * 8 / durationSeconds
}

func durationFromProbeMetadata(raw *ffprobeOutput) (int, bool) {
	if raw == nil {
		return 0, false
	}

	formatDuration := parseFloat(raw.Format.Duration)
	if !hasVideoStream(raw.Streams) &&
		durationIsPositiveFinite(formatDuration) && formatDuration <= maxReasonableAudioDurationSeconds {
		return truncatedDuration(formatDuration), true
	}
	if durationIsReasonable(formatDuration) && !durationLooksImplausible(raw, formatDuration) {
		return truncatedDuration(formatDuration), true
	}

	for _, stream := range raw.Streams {
		if !mediaprobe.IsPrimaryVideoStream(stream) {
			continue
		}
		streamDuration := parseFloat(stream.Duration)
		if durationIsReasonable(streamDuration) && !durationLooksImplausible(raw, streamDuration) {
			return truncatedDuration(streamDuration), true
		}
		duration := durationAfterStart(streamDuration, parseFloat(stream.StartTime))
		if duration > 0 && !durationLooksImplausible(raw, duration) {
			return truncatedDuration(duration), true
		}
	}

	duration := durationAfterStart(formatDuration, parseFloat(raw.Format.StartTime))
	if duration > 0 && !durationLooksImplausible(raw, duration) {
		return truncatedDuration(duration), true
	}
	if duration, ok := corroboratedLongVideoDuration(raw, formatDuration); ok {
		return truncatedDuration(duration), true
	}
	return 0, false
}

// corroboratedLongVideoDuration accepts an extended-range video duration only
// when the container and the primary video stream independently report nearly the
// same value. A small absolute/relative tolerance covers container rounding
// and stream-boundary differences without trusting a lone malformed timeline.
func corroboratedLongVideoDuration(raw *ffprobeOutput, formatDuration float64) (float64, bool) {
	if raw == nil {
		return 0, false
	}

	for _, stream := range raw.Streams {
		if !mediaprobe.IsPrimaryVideoStream(stream) {
			continue
		}
		streamDuration := parseFloat(stream.Duration)
		formatStart := parseFloat(raw.Format.StartTime)
		streamStart := parseFloat(stream.StartTime)

		// Some MPEG-TS/HLS timelines report duration as an absolute end
		// timestamp. Use normalized spans to reconcile raw end timestamps that
		// disagree, or when matching timestamps have a dominant start offset that
		// strongly indicates the absolute-end shape. Ordinary non-zero starts remain
		// part of an already corroborated raw duration.
		normalizedFormatDuration := durationAfterStartWithinValidatedLimit(
			formatDuration,
			formatStart,
		)
		normalizedStreamDuration := durationAfterStartWithinValidatedLimit(
			streamDuration,
			streamStart,
		)
		rawDurationsAgree := longVideoDurationsAgree(formatDuration, streamDuration)
		normalizedDurationsAgree := longVideoDurationsAgree(normalizedFormatDuration, normalizedStreamDuration)
		matchingAbsoluteEnds := rawDurationsAgree &&
			durationHasDominantStartOffset(formatDuration, formatStart) &&
			durationHasDominantStartOffset(streamDuration, streamStart)
		if normalizedDurationsAgree && (!rawDurationsAgree || matchingAbsoluteEnds) &&
			!durationLooksImplausible(raw, normalizedFormatDuration) {
			return normalizedFormatDuration, true
		}
		if matchingAbsoluteEnds {
			// Dominant starts identify the raw values as absolute end
			// timestamps. If their normalized spans do not corroborate, reject
			// the metadata for packet repair instead of persisting an inflated
			// raw end timestamp.
			return 0, false
		}

		if rawDurationsAgree &&
			!durationLooksImplausible(raw, formatDuration) {
			return formatDuration, true
		}
		return 0, false
	}

	return 0, false
}

func longVideoDurationsAgree(first, second float64) bool {
	if first <= maxReasonableMediaDurationSeconds ||
		!durationIsWithinValidatedLimit(first) ||
		!durationIsWithinValidatedLimit(second) {
		return false
	}
	tolerance := max(
		longVideoDurationAbsoluteToleranceSeconds,
		max(first, second)*longVideoDurationRelativeTolerance,
	)
	return math.Abs(first-second) <= tolerance
}

// durationHasDominantStartOffset identifies the conservative absolute-end
// shape where the start timestamp occupies at least half of the reported end.
// Smaller starts are common media offsets and cannot disambiguate a duration
// field from an absolute end timestamp.
func durationHasDominantStartOffset(end, start float64) bool {
	return start > 0 && end > start && start >= end-start
}

func durationLooksImplausible(raw *ffprobeOutput, duration float64) bool {
	if raw == nil {
		return false
	}
	size := int64(parseFloat(raw.Format.Size))
	return videoDurationImplausible(duration, size, hasVideoStream(raw.Streams))
}

func durationAfterStart(end, start float64) float64 {
	if start <= 0 || end <= start {
		return 0
	}
	duration := end - start
	if !durationIsReasonable(duration) {
		return 0
	}
	return duration
}

func durationAfterStartWithinValidatedLimit(end, start float64) float64 {
	if start <= 0 || end <= start {
		return 0
	}
	duration := end - start
	if !durationIsWithinValidatedLimit(duration) {
		return 0
	}
	return duration
}

func durationIsReasonable(duration float64) bool {
	return durationIsPositiveFinite(duration) && duration <= maxReasonableMediaDurationSeconds
}

func durationIsWithinValidatedLimit(duration float64) bool {
	return durationIsPositiveFinite(duration) && duration <= maxValidatedMediaDurationSeconds
}

func durationIsPositiveFinite(duration float64) bool {
	return duration > 0 && !math.IsNaN(duration) && !math.IsInf(duration, 0)
}

func roundedDuration(duration float64) int {
	return max(1, int(math.Round(duration)))
}

func truncatedDuration(duration float64) int {
	return max(1, int(duration))
}

func hasVideoStream(streams []ffprobeStream) bool {
	return slices.ContainsFunc(streams, mediaprobe.IsPrimaryVideoStream)
}

func primaryVideoFrameRate(streams []ffprobeStream) (string, bool) {
	for _, stream := range streams {
		if mediaprobe.IsPrimaryVideoStream(stream) {
			return stream.AvgFrameRate, true
		}
	}
	return "", false
}

// probeVideoPacketDuration derives a duration for files whose duration
// metadata is unusable by scanning video packet timestamps. It intentionally
// demuxes the whole file: the timestamps being repaired are the same ones
// ffprobe would need for reliable interval seeking, so sampling cannot be
// trusted here. The repair layer keeps this one-shot per file.
func probeVideoPacketDuration(
	ctx context.Context,
	ffprobePath string,
	filePath string,
	frameRate string,
) (int, error) {
	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "packet=pts_time",
		"-of", "csv=p=0",
		filePath,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("opening ffprobe packet output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting ffprobe packet scan: %w", err)
	}

	duration := estimateVideoPacketDuration(stdout, frameRate)
	if err := cmd.Wait(); err != nil {
		return 0, fmt.Errorf("ffprobe packet scan failed for %s: %w", filePath, err)
	}
	return duration, nil
}

func estimateVideoPacketDuration(reader io.Reader, frameRate string) int {
	scanner := bufio.NewScanner(reader)
	packetCount := 0
	minTimestamp := math.Inf(1)
	maxTimestamp := math.Inf(-1)

	for scanner.Scan() {
		value := strings.TrimSpace(scanner.Text())
		if value == "" {
			continue
		}
		packetCount++
		timestamp, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		minTimestamp = min(minTimestamp, timestamp)
		maxTimestamp = max(maxTimestamp, timestamp)
	}

	packetSpan := 0.0
	if !math.IsInf(minTimestamp, 1) && !math.IsInf(maxTimestamp, -1) {
		span := maxTimestamp - minTimestamp
		if durationIsWithinValidatedLimit(span) {
			packetSpan = span
		}
	}

	frameDuration := 0.0
	if fps := parseFrameRate(frameRate); fps > 0 && packetCount > 0 {
		frameDuration = float64(packetCount) / fps
	}

	best := packetSpan
	if packetSpan > maxReasonableMediaDurationSeconds &&
		durationIsReasonable(frameDuration) &&
		!longVideoDurationsAgree(packetSpan, frameDuration) {
		// A long PTS span is strong evidence only when a sane frame-count
		// estimate contradicts it. Malformed frame rates can produce finite but
		// unusable estimates and must not veto an otherwise valid packet span.
		best = 0
	}
	if durationIsReasonable(frameDuration) && frameDuration > best {
		best = frameDuration
	}
	if best <= 0 {
		return 0
	}
	return roundedDuration(best)
}

// parseFrameRate parses ffprobe's rational frame-rate shape ("30000/1001")
// or a plain float, returning 0 when unparsable. normalizeFrameRate formats
// the same parse for persistence; keep the parsing logic here only.
func parseFrameRate(raw string) float64 {
	raw = strings.TrimSpace(raw)
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		fps, _ := strconv.ParseFloat(raw, 64)
		return fps
	}
	numerator, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0
	}
	denominator, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || denominator == 0 {
		return 0
	}
	return numerator / denominator
}

func parseNumeric(raw string) int {
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return v
}

func parseFloat(raw string) float64 {
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return value
}

func normalizeChapters(raw []ffprobeChapter, durationSeconds int) []ChapterInfo {
	if len(raw) == 0 {
		return []ChapterInfo{}
	}

	limit := float64(durationSeconds)
	type chapterRange struct {
		title string
		start float64
		end   float64
	}

	ranges := make([]chapterRange, 0, len(raw))
	for _, chapter := range raw {
		start := parseFloat(string(chapter.StartTime))
		end := parseFloat(string(chapter.EndTime))
		if end <= 0 {
			end = parseFloat(string(chapter.End))
		}
		if start <= 0 {
			start = parseFloat(string(chapter.Start))
		}
		if limit > 0 {
			if start < 0 {
				start = 0
			}
			if end > limit {
				end = limit
			}
		}
		if end <= start {
			continue
		}

		title := strings.TrimSpace(firstNonEmpty(
			chapter.Tags["title"],
			chapter.Tags["TITLE"],
		))
		ranges = append(ranges, chapterRange{
			title: title,
			start: start,
			end:   end,
		})
	}

	if len(ranges) == 0 {
		return []ChapterInfo{}
	}

	slices.SortStableFunc(ranges, func(a, b chapterRange) int {
		switch {
		case a.start < b.start:
			return -1
		case a.start > b.start:
			return 1
		case a.end < b.end:
			return -1
		case a.end > b.end:
			return 1
		default:
			return 0
		}
	})

	chapters := make([]ChapterInfo, 0, len(ranges))
	for i, chapter := range ranges {
		end := chapter.end
		if i+1 < len(ranges) && ranges[i+1].start < end {
			end = ranges[i+1].start
		}
		if end <= chapter.start {
			continue
		}

		title := chapter.title
		if title == "" {
			title = fmt.Sprintf("Chapter %02d", len(chapters)+1)
		}
		chapters = append(chapters, ChapterInfo{
			Index:        len(chapters),
			Title:        title,
			StartSeconds: chapter.start,
			EndSeconds:   end,
			Source:       "embedded",
		})
	}

	return chapters
}

func parseBitDepth(s ffprobeStream) int {
	if v := parseNumeric(string(s.BitsPerRawSample)); v > 0 {
		return v
	}
	return parseNumeric(string(s.BitsPerSample))
}

func subtitleResolutionLabel(s ffprobeStream) string {
	if s.Width <= 0 || s.Height <= 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", s.Width, s.Height)
}

func dispositionFlag(tags map[string]string, key string) bool {
	if tags == nil {
		return false
	}
	value := strings.TrimSpace(strings.ToLower(tags[key]))
	return value == "1" || value == "true" || value == "yes"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// mapResolution converts video dimensions to a standard resolution string.
// Uses upper-bound bucketing (similar to Jellyfin) checking both width and
// height, which correctly handles ultra-wide and non-standard aspect ratios.
func mapResolution(width, height int) string {
	switch {
	case width <= 0 && height <= 0:
		return ""
	case width <= 854 && height <= 480:
		return "480p"
	case width <= 1280 && height <= 962:
		return "720p"
	case width <= 2560 && height <= 1440:
		return "1080p"
	case width <= 4096 && height <= 3072:
		return "2160p"
	case width <= 8192 && height <= 6144:
		return "4320p"
	default:
		return "2160p"
	}
}

// isHDR checks whether the color transfer characteristic indicates HDR content.
func isHDR(colorTransfer string) bool {
	ct := strings.ToLower(colorTransfer)
	return strings.Contains(ct, "smpte2084") || strings.Contains(ct, "arib-std-b67")
}

// normalizeFormatTags lowercases tag keys so callers can look up
// "title", "artist", "album" without worrying about ffprobe's mixed-case
// output. Trims whitespace from values.
func normalizeFormatTags(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

// detectContainer maps ffprobe format names to common container names.
func detectContainer(formatName string) string {
	// ffprobe format_name can contain multiple names separated by commas
	// e.g. "mov,mp4,m4a,3gp,3g2,mj2"
	parts := strings.Split(formatName, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		case "matroska", "webm":
			return "mkv"
		case "mov", "mp4", "m4a":
			return "mp4"
		case "avi":
			return "avi"
		case "mpegts":
			return "ts"
		case "flv":
			return "flv"
		case "ogg":
			return "ogg"
		case "wmv", "asf":
			return "wmv"
		}
	}
	// Fallback: return first part
	if len(parts) > 0 && parts[0] != "" {
		return strings.TrimSpace(parts[0])
	}
	return formatName
}
