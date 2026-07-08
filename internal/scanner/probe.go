package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/lang"
)

// ffprobeOutput represents the top-level JSON output from ffprobe.
type ffprobeOutput struct {
	Format   ffprobeFormat    `json:"format"`
	Streams  []ffprobeStream  `json:"streams"`
	Chapters []ffprobeChapter `json:"chapters"`
}

// ffprobeScalarString accepts ffprobe fields that may be emitted as either
// JSON strings or numbers depending on codec/container details.
type ffprobeScalarString string

func (s *ffprobeScalarString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = ""
		return nil
	}

	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		*s = ffprobeScalarString(str)
		return nil
	}

	var num json.Number
	if err := json.Unmarshal(data, &num); err == nil {
		*s = ffprobeScalarString(num.String())
		return nil
	}

	return fmt.Errorf("unsupported ffprobe scalar %s", string(data))
}

// ffprobeFormat represents the "format" section of ffprobe JSON output.
type ffprobeFormat struct {
	Filename       string            `json:"filename"`
	FormatName     string            `json:"format_name"`
	FormatLongName string            `json:"format_long_name"`
	Duration       string            `json:"duration"`
	Size           string            `json:"size"`
	BitRate        string            `json:"bit_rate"`
	Tags           map[string]string `json:"tags"`
}

// ffprobeStream represents a single stream entry in ffprobe JSON output.
type ffprobeStream struct {
	Index              int                 `json:"index"`
	CodecName          string              `json:"codec_name"`
	CodecLongName      string              `json:"codec_long_name"`
	CodecType          string              `json:"codec_type"`
	Profile            string              `json:"profile"`
	Level              int                 `json:"level"`
	Width              int                 `json:"width"`
	Height             int                 `json:"height"`
	DisplayAspectRatio string              `json:"display_aspect_ratio"`
	FieldOrder         string              `json:"field_order"`
	AvgFrameRate       string              `json:"avg_frame_rate"`
	BitRate            string              `json:"bit_rate"`
	ColorTransfer      string              `json:"color_transfer"`
	ColorPrimaries     string              `json:"color_primaries"`
	ColorSpace         string              `json:"color_space"`
	PixFmt             string              `json:"pix_fmt"`
	Refs               int                 `json:"refs"`
	BitsPerRawSample   ffprobeScalarString `json:"bits_per_raw_sample"`
	BitsPerSample      ffprobeScalarString `json:"bits_per_sample"`
	Channels           int                 `json:"channels"`
	ChannelLayout      string              `json:"channel_layout"`
	SampleRate         string              `json:"sample_rate"`
	Disposition        ffprobeDisp         `json:"disposition"`
	Tags               map[string]string   `json:"tags"`
	SideDataList       []ffprobeSideData   `json:"side_data_list"`
}

type ffprobeChapter struct {
	ID        int                 `json:"id"`
	Start     ffprobeScalarString `json:"start"`
	End       ffprobeScalarString `json:"end"`
	TimeBase  string              `json:"time_base"`
	StartTime ffprobeScalarString `json:"start_time"`
	EndTime   ffprobeScalarString `json:"end_time"`
	Tags      map[string]string   `json:"tags"`
}

type ffprobeSideData struct {
	SideDataType string `json:"side_data_type"`
	DVProfile    int    `json:"dv_profile"`
	DVBlPresent  int    `json:"dv_bl_present"`
	DVElPresent  int    `json:"dv_el_present"`
	DVBLCompatID int    `json:"dv_bl_signal_compatibility_id"`
}

// ffprobeDisp represents the disposition flags on a stream.
type ffprobeDisp struct {
	Default int `json:"default"`
	Forced  int `json:"forced"`
}

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

	return convertProbeData(&raw), nil
}

// FFprobePathFromFFmpeg derives the sibling ffprobe binary path from a configured ffmpeg path.
func FFprobePathFromFFmpeg(ffmpegPath string) string {
	if i := strings.LastIndex(ffmpegPath, "ffmpeg"); i >= 0 {
		ffprobePath := ffmpegPath[:i] + "ffprobe" + ffmpegPath[i+len("ffmpeg"):]
		if ffprobePath != "" && ffprobePath != ffmpegPath {
			return ffprobePath
		}
	}
	return "ffprobe"
}

// convertProbeData transforms raw ffprobe JSON output into ProbeData.
func convertProbeData(raw *ffprobeOutput) *ProbeData {
	pd := &ProbeData{
		Container: detectContainer(raw.Format.FormatName),
	}

	// Parse duration from format (ffprobe reports seconds).
	// Some containers (notably MKV) may produce duration in microseconds;
	// detect and normalise to seconds when the value is unreasonably large.
	if raw.Format.Duration != "" {
		if dur, err := strconv.ParseFloat(raw.Format.Duration, 64); err == nil {
			const maxReasonableSec = 100_000 // ~27.8 hours
			if dur > maxReasonableSec {
				dur = dur / 1_000_000 // microseconds → seconds
			}
			pd.Duration = int(dur)
		}
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
			dvProfile := dolbyVisionProfileNumber(s.SideDataList)
			track := VideoTrackInfo{
				Title:           firstNonEmpty(s.Tags["title"], s.CodecLongName, strings.ToUpper(s.CodecName)),
				Codec:           s.CodecName,
				DolbyVision:     dolbyVisionProfile(s.SideDataList),
				DVProfile:       dvProfile,
				DVBLCompatID:    dolbyVisionBLCompatID(s.SideDataList),
				DVELPresent:     dolbyVisionELPresent(s.SideDataList),
				HDR10Plus:       hasHDR10Plus(s.SideDataList),
				Profile:         s.Profile,
				Level:           s.Level,
				Width:           s.Width,
				Height:          s.Height,
				AspectRatio:     s.DisplayAspectRatio,
				Interlaced:      isInterlaced(s.FieldOrder),
				FrameRate:       normalizeFrameRate(s.AvgFrameRate),
				Bitrate:         parseNumeric(s.BitRate) / 1000,
				VideoRange:      videoRangeLabel(s),
				VideoRangeType:  videoRangeType(s),
				ColorPrimaries:  s.ColorPrimaries,
				ColorSpace:      s.ColorSpace,
				ColorTransfer:   s.ColorTransfer,
				BitDepth:        parseBitDepth(s),
				PixelFormat:     s.PixFmt,
				ReferenceFrames: s.Refs,
			}
			pd.VideoTracks = append(pd.VideoTracks, track)
			if pd.CodecVideo == "" {
				pd.CodecVideo = s.CodecName
				pd.Resolution = mapResolution(s.Width, s.Height)
				pd.HDR = isHDR(s.ColorTransfer) || dvProfile > 0 || track.HDR10Plus
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

func normalizeFrameRate(raw string) string {
	if raw == "" || raw == "0/0" {
		return ""
	}
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		return raw
	}
	num, err1 := strconv.ParseFloat(parts[0], 64)
	den, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || den == 0 {
		return raw
	}
	return strconv.FormatFloat(num/den, 'f', 3, 64)
}

func isInterlaced(fieldOrder string) bool {
	switch strings.ToLower(fieldOrder) {
	case "tt", "bb", "tb", "bt":
		return true
	default:
		return false
	}
}

func videoRangeLabel(s ffprobeStream) string {
	if dv := dolbyVisionProfile(s.SideDataList); dv != "" {
		return "DolbyVision"
	}
	if isHDR(s.ColorTransfer) {
		return "HDR"
	}
	return ""
}

func dolbyVisionProfile(sideData []ffprobeSideData) string {
	if profile := dolbyVisionProfileNumber(sideData); profile > 0 {
		return fmt.Sprintf("Profile %d", profile)
	}
	return ""
}

func dolbyVisionProfileNumber(sideData []ffprobeSideData) int {
	for _, data := range sideData {
		if strings.EqualFold(data.SideDataType, "DOVI configuration record") && data.DVProfile > 0 {
			return data.DVProfile
		}
	}
	return 0
}

func dolbyVisionBLCompatID(sideData []ffprobeSideData) int {
	for _, data := range sideData {
		if strings.EqualFold(data.SideDataType, "DOVI configuration record") && data.DVBLCompatID > 0 {
			return data.DVBLCompatID
		}
	}
	return 0
}

func dolbyVisionELPresent(sideData []ffprobeSideData) bool {
	for _, data := range sideData {
		if strings.EqualFold(data.SideDataType, "DOVI configuration record") {
			return data.DVElPresent > 0
		}
	}
	return false
}

func hasHDR10Plus(sideData []ffprobeSideData) bool {
	for _, data := range sideData {
		typ := strings.ToLower(data.SideDataType)
		if strings.Contains(typ, "hdr10+") || strings.Contains(typ, "smpte2094-40") {
			return true
		}
	}
	return false
}

func videoRangeType(s ffprobeStream) string {
	profile := dolbyVisionProfileNumber(s.SideDataList)
	hdr10Plus := hasHDR10Plus(s.SideDataList)
	if profile > 0 {
		switch profile {
		case 5:
			return "DOVI"
		case 7:
			if hdr10Plus {
				return "DOVIWithELHDR10Plus"
			}
			return "DOVIWithEL"
		case 8:
			if hdr10Plus {
				return "DOVIWithHDR10Plus"
			}
			switch dolbyVisionBLCompatID(s.SideDataList) {
			case 1:
				return "DOVIWithHDR10"
			case 2:
				return "DOVIWithSDR"
			case 4:
				return "DOVIWithHLG"
			default:
				if isHLG(s.ColorTransfer) {
					return "DOVIWithHLG"
				}
				if isHDR(s.ColorTransfer) {
					return "DOVIWithHDR10"
				}
				return "DOVIWithSDR"
			}
		default:
			return "DOVI"
		}
	}
	if hdr10Plus {
		return "HDR10Plus"
	}
	if isHLG(s.ColorTransfer) {
		return "HLG"
	}
	if isHDR(s.ColorTransfer) {
		return "HDR10"
	}
	return "SDR"
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

func isHLG(colorTransfer string) bool {
	return strings.Contains(strings.ToLower(colorTransfer), "arib-std-b67")
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
