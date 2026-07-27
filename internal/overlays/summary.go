package overlays

import (
	"math"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// Summary reuses the API-facing overlay summary shape.
type Summary = models.OverlaySummary

// BuildSummary derives a compact overlay summary from the best available file.
func BuildSummary(files []*models.MediaFile) *Summary {
	best := bestFile(files)
	if best == nil {
		return nil
	}

	summary := &Summary{
		Resolution:    normalizeResolution(best.Resolution),
		HDR:           normalizeHDR(best),
		Audio:         normalizeAudio(best),
		AudioChannels: normalizeAudioChannels(best),
		VideoCodec:    normalizeVideoCodec(best),
		Container:     normalizeContainer(best.Container),
		AspectRatio:   normalizeAspectRatio(best),
		ReleaseType:   normalizeReleaseType(best.FilePath),
		Edition:       normalizeEdition(best.EditionKey),
		MultiAudio:    detectMultiAudio(best),
		MultiSub:      detectMultiSub(best),
	}
	if summary.Resolution == "" &&
		summary.HDR == "" &&
		summary.Audio == "" &&
		summary.AudioChannels == "" &&
		summary.VideoCodec == "" &&
		summary.Container == "" &&
		summary.AspectRatio == "" &&
		summary.ReleaseType == "" &&
		summary.Edition == "" &&
		!summary.MultiAudio &&
		!summary.MultiSub {
		return nil
	}
	return summary
}

func bestFile(files []*models.MediaFile) *models.MediaFile {
	var best *models.MediaFile
	bestRes := -1
	bestRange := -1
	for _, file := range files {
		if file == nil {
			continue
		}
		res := resolutionRank(file.Resolution)
		rng := rangeRank(file)
		if best == nil || res > bestRes || (res == bestRes && rng > bestRange) {
			best = file
			bestRes = res
			bestRange = rng
		}
	}
	return best
}

// rangeRank orders files with equal resolution by the richness of their
// dynamic-range metadata so a Dolby Vision version is not masked by a
// first-scanned file that only carries the bare HDR boolean (e.g. a stale
// pre-DV probe row). Ties keep the earliest file in the slice.
func rangeRank(file *models.MediaFile) int {
	if hasDolbyVision(file.VideoTracks) {
		return 3
	}
	if hdrTypeFromTracks(file.VideoTracks) != "" {
		return 2
	}
	if file.HDR {
		return 1
	}
	return 0
}

// hasDolbyVision reports whether any track carries Dolby Vision metadata.
// Probed rows set DolbyVision and DVProfile together, but seeded/imported
// rows may carry only dv_profile or a DOVI* video_range_type.
func hasDolbyVision(tracks []models.VideoTrack) bool {
	for _, track := range tracks {
		if track.DolbyVision != "" || track.DVProfile > 0 ||
			strings.HasPrefix(track.VideoRangeType, "DOVI") {
			return true
		}
	}
	return false
}

func normalizeResolution(value string) string {
	cleaned := strings.ToLower(strings.TrimSpace(value))
	switch cleaned {
	case "":
		return ""
	case "4k", "uhd":
		return "2160p"
	default:
		return cleaned
	}
}

func resolutionRank(value string) int {
	normalized := normalizeResolution(value)
	if normalized == "" {
		return 0
	}
	if before, ok := strings.CutSuffix(normalized, "p"); ok {
		numeric := before
		if rank, err := strconv.Atoi(numeric); err == nil {
			return rank
		}
	}
	return 0
}

func normalizeHDR(file *models.MediaFile) string {
	hasDV := hasDolbyVision(file.VideoTracks)
	hdrType := hdrTypeFromTracks(file.VideoTracks)

	switch {
	case hasDV && hdrType != "":
		return "DV " + hdrType
	case hasDV:
		return "DV"
	case hdrType != "":
		return hdrType
	case file.HDR:
		return "HDR"
	default:
		return ""
	}
}

// hdrTypeFromTracks distinguishes HDR variants from the scanner-derived
// video_range_type enum (HDR10, HDR10Plus, HLG, DOVIWith*) with color
// transfer as a fallback, so rows without probed color metadata (e.g.
// catalog-seeded imports) still resolve. Keep in sync with trackHdrType in
// web/src/lib/videoRange.ts.
func hdrTypeFromTracks(tracks []models.VideoTrack) string {
	for _, track := range tracks {
		rangeType := strings.TrimSpace(track.VideoRangeType)
		switch {
		case track.HDR10Plus || strings.Contains(rangeType, "HDR10Plus"):
			return "HDR10+"
		case rangeType == "HDR10" || strings.HasSuffix(rangeType, "WithHDR10"):
			return "HDR10"
		case rangeType == "HLG" || strings.HasSuffix(rangeType, "WithHLG"):
			return "HLG"
		}
		ct := strings.ToLower(track.ColorTransfer)
		switch {
		case strings.Contains(ct, "smpte2084"):
			return "HDR10"
		case strings.Contains(ct, "arib-std-b67"):
			return "HLG"
		}
	}
	return ""
}

func normalizeAudio(file *models.MediaFile) string {
	defaultCandidates := make([]string, 0, 3)
	candidates := make([]string, 0, len(file.AudioTracks)*3+1)
	for _, track := range file.AudioTracks {
		if track.Default {
			defaultCandidates = append(defaultCandidates, track.Title, track.EmbeddedTitle, track.Codec)
		}
		candidates = append(candidates, track.Title, track.EmbeddedTitle, track.Codec)
	}
	if len(defaultCandidates) > 0 {
		defaultCandidates = append(defaultCandidates, file.CodecAudio)
	}
	if len(defaultCandidates) > 0 {
		if label := normalizeAudioCandidates(defaultCandidates); label != "" {
			return label
		}
	}
	if label := normalizeAudioCandidates(candidates); label != "" {
		return label
	}

	return normalizeAudioCandidates([]string{file.CodecAudio})
}

func normalizeAudioCandidates(candidates []string) string {
	for _, candidate := range candidates {
		lower := strings.ToLower(strings.TrimSpace(candidate))
		switch {
		case lower == "":
			continue
		case strings.Contains(lower, "atmos"):
			return "Atmos"
		case strings.Contains(lower, "truehd"):
			return "TrueHD"
		case strings.Contains(lower, "dts-hd"), strings.Contains(lower, "dts:x"), strings.Contains(lower, "dtsx"):
			return "DTS-HD"
		case strings.Contains(lower, "dts"):
			return "DTS"
		case strings.Contains(lower, "eac3"), strings.Contains(lower, "e-ac-3"), strings.Contains(lower, "dd+"):
			return "EAC3"
		case strings.Contains(lower, "ac3"), strings.Contains(lower, "ac-3"):
			return "AC3"
		case strings.Contains(lower, "aac"):
			return "AAC"
		case strings.Contains(lower, "flac"):
			return "FLAC"
		}
	}
	return ""
}

// normalizeReleaseType delegates to subtitles.ParseReleaseInfo so the badge
// label uses the same word-boundary-enforced regex as the rest of the system,
// avoiding false positives like "dvd" matching paths containing "goodvideos".
func normalizeReleaseType(path string) string {
	source := subtitles.ParseReleaseInfo(path).Source
	if source == "" {
		return ""
	}
	switch strings.ToLower(source) {
	case "webdl", "web-dl":
		return "WEB-DL"
	case "bluray", "bdrip", "brrip":
		return "BluRay"
	case "dvdrip":
		return "DVD"
	case "hdtv":
		return "HDTV"
	case "webrip":
		return "WEBRip"
	case "remux":
		return "REMUX"
	default:
		return source
	}
}

func normalizeEdition(key string) string {
	return naming.EditionDisplayLabel(key)
}

func normalizeVideoCodec(file *models.MediaFile) string {
	candidate := file.CodecVideo
	for _, t := range file.VideoTracks {
		if t.Codec != "" {
			candidate = t.Codec
			break
		}
	}
	switch strings.ToLower(strings.TrimSpace(candidate)) {
	case "":
		return ""
	case "hevc", "h265", "h.265", "x265":
		return "H.265"
	case "h264", "h.264", "avc", "x264":
		return "H.264"
	case "av1":
		return "AV1"
	case "vp9":
		return "VP9"
	case "mpeg4", "xvid":
		return "MPEG-4"
	case "mpeg2video":
		return "MPEG-2"
	case "vc1":
		return "VC-1"
	default:
		return strings.ToUpper(candidate)
	}
}

func normalizeAudioChannels(file *models.MediaFile) string {
	// Prefer the default audio track when present, else the highest channel
	// count among the remaining tracks, else fall back to the file-level
	// AudioChannels column. The two-pass structure avoids a corner case where
	// a default track with Channels==0 is shadowed by a higher-count
	// non-default track that appears earlier in the slice.
	for _, t := range file.AudioTracks {
		if t.Default && t.Channels > 0 {
			return audioChannelsLabel(t.Channels)
		}
	}
	best := 0
	for _, t := range file.AudioTracks {
		if t.Channels > best {
			best = t.Channels
		}
	}
	if best == 0 {
		best = file.AudioChannels
	}
	return audioChannelsLabel(best)
}

func audioChannelsLabel(best int) string {
	switch {
	case best <= 0:
		return ""
	case best == 1:
		return "Mono"
	case best == 2:
		return "Stereo"
	case best == 6:
		return "5.1"
	case best == 7:
		return "6.1"
	case best == 8:
		return "7.1"
	default:
		return strconv.Itoa(best) + "ch"
	}
}

func normalizeContainer(container string) string {
	c := strings.ToLower(strings.TrimSpace(container))
	if c == "" {
		return ""
	}
	return strings.ToUpper(c)
}

func normalizeAspectRatio(file *models.MediaFile) string {
	for _, t := range file.VideoTracks {
		if ratio := canonicalAspectRatio(t.AspectRatio); ratio != "" {
			return ratio
		}
		if t.Width > 0 && t.Height > 0 {
			return ratioFromDimensions(t.Width, t.Height)
		}
	}
	return ""
}

// canonicalAspectRatio normalizes the probed aspect ratio string. FFprobe
// commonly emits values like "16:9", "239:100", or "2.39:1"; we coerce
// the most common cinema ratios to a stable display form.
func canonicalAspectRatio(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return ""
	}
	num, err1 := strconv.ParseFloat(parts[0], 64)
	den, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || den == 0 {
		return ""
	}
	ratio := num / den
	return formatRatio(ratio)
}

func ratioFromDimensions(width, height int) string {
	if height == 0 {
		return ""
	}
	return formatRatio(float64(width) / float64(height))
}

// formatRatio maps a numeric aspect ratio to its canonical display string.
// Values within 0.02 of a known ratio snap to the named form; everything
// else is rounded to two decimals as "X.YZ:1".
func formatRatio(r float64) string {
	const tol = 0.02
	switch {
	case math.Abs(r-4.0/3.0) <= tol:
		return "4:3"
	case math.Abs(r-16.0/9.0) <= tol:
		return "16:9"
	case math.Abs(r-16.0/10.0) <= tol:
		return "16:10"
	case math.Abs(r-1.85) <= tol:
		return "1.85:1"
	case math.Abs(r-2.0) <= tol:
		return "2:1"
	case math.Abs(r-2.20) <= tol:
		return "2.20:1"
	case math.Abs(r-2.35) <= tol:
		return "2.35:1"
	case math.Abs(r-2.39) <= tol, math.Abs(r-2.40) <= tol:
		return "2.39:1"
	case math.Abs(r-2.76) <= tol:
		return "2.76:1"
	}
	if r <= 0 {
		return ""
	}
	return strconv.FormatFloat(r, 'f', 2, 64) + ":1"
}

// detectMultiAudio returns true when the file has audio tracks in at least
// two distinct languages (ignoring "und"/empty). Commentary tracks tagged
// with the same primary language do not count.
func detectMultiAudio(file *models.MediaFile) bool {
	seen := make(map[string]struct{}, 2)
	for _, t := range file.AudioTracks {
		lang := strings.ToLower(strings.TrimSpace(t.Language))
		if lang == "" || lang == "und" {
			continue
		}
		seen[lang] = struct{}{}
		if len(seen) >= 2 {
			return true
		}
	}
	return false
}

// detectMultiSub returns true when the file has at least one subtitle track,
// embedded or external.
func detectMultiSub(file *models.MediaFile) bool {
	return len(file.SubtitleTracks) > 0 || len(file.ExternalSubtitles) > 0
}
