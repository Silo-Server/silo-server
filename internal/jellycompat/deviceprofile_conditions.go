package jellycompat

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type codecProfileCompatibility struct {
	VideoSupported bool
	AudioSupported bool
}

func (c codecProfileCompatibility) supportsDirectPlay() bool {
	return c.VideoSupported && c.AudioSupported
}

type conditionValue struct {
	text   string
	number int
	hasNum bool
}

type conditionValues map[string]conditionValue

func (c *ProfileCondition) UnmarshalJSON(data []byte) error {
	var raw struct {
		Condition  string `json:"Condition"`
		Property   string `json:"Property"`
		Value      any    `json:"Value"`
		IsRequired *bool  `json:"IsRequired"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	value, err := stringifyConditionValue(raw.Value)
	if err != nil {
		return err
	}
	c.Condition = raw.Condition
	c.Property = raw.Property
	c.Value = value
	// Jellyfin's C# ProfileCondition defaults IsRequired to true in its
	// parameterless constructor, and its deserializer only overwrites
	// properties actually present in the payload. An omitted key therefore
	// means "required", not the Go zero value.
	c.IsRequired = raw.IsRequired == nil || *raw.IsRequired
	return nil
}

func stringifyConditionValue(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10), nil
		}
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshal profile condition value: %w", err)
		}
		return string(data), nil
	}
}

func (p DeviceProfile) codecProfileCompatibility(version catalog.FileVersion, audioStreamIndex *int) codecProfileCompatibility {
	compat := codecProfileCompatibility{VideoSupported: true, AudioSupported: true}
	if len(p.CodecProfiles) == 0 {
		return compat
	}

	values := buildConditionValues(version, audioStreamIndex)
	for _, profile := range p.CodecProfiles {
		target := codecProfileTarget(profile)
		if target == "" || !codecProfileApplies(profile, version, audioStreamIndex) {
			continue
		}
		if !conditionsMatch(profile.ApplyConditions, values) {
			continue
		}
		if conditionsMatch(profile.Conditions, values) {
			continue
		}

		if target == "audio" || conditionsOnlyTargetAudio(profile.Conditions) {
			compat.AudioSupported = false
		} else {
			compat.VideoSupported = false
		}
	}
	return compat
}

func codecProfileTarget(profile CodecProfile) string {
	typ := normalizeConditionToken(profile.Type)
	switch {
	case typ == "videoaudio" || typ == "audio":
		return "audio"
	case typ == "" || typ == "video":
		return "video"
	default:
		return ""
	}
}

func codecProfileApplies(profile CodecProfile, version catalog.FileVersion, audioStreamIndex *int) bool {
	if strings.TrimSpace(profile.Codec) == "" {
		return true
	}
	switch codecProfileTarget(profile) {
	case "audio":
		return matchesCSV(profile.Codec, compatAudioCodec(version, audioStreamIndex))
	case "video":
		return matchesCSV(profile.Codec, version.CodecVideo)
	default:
		return false
	}
}

func conditionsOnlyTargetAudio(conditions []ProfileCondition) bool {
	if len(conditions) == 0 {
		return false
	}
	for _, condition := range conditions {
		if normalizeConditionToken(condition.Property) != "audiochannels" {
			return false
		}
	}
	return true
}

func conditionsMatch(conditions []ProfileCondition, values conditionValues) bool {
	for _, condition := range conditions {
		if !conditionMatches(condition, values) {
			return false
		}
	}
	return true
}

func conditionMatches(condition ProfileCondition, values conditionValues) bool {
	actual, ok := values[normalizeConditionToken(condition.Property)]
	if !ok {
		return !condition.IsRequired
	}

	switch normalizeConditionToken(condition.Condition) {
	case "equals":
		return stringInConditionSet(actual.text, condition.Value)
	case "equalsany", "incollection":
		return stringInConditionSet(actual.text, condition.Value)
	case "notequals":
		return !stringInConditionSet(actual.text, condition.Value)
	case "notequalsany", "notincollection":
		return !stringInConditionSet(actual.text, condition.Value)
	case "lessthanequal", "lessthanorequal", "lowerthanequal", "lowerthanorequal":
		want, ok := firstConditionNumber(condition.Value)
		return ok && actual.hasNum && actual.number <= want
	case "greaterthanequal", "greaterthanorequal":
		want, ok := firstConditionNumber(condition.Value)
		return ok && actual.hasNum && actual.number >= want
	case "lessthan", "lowerthan":
		want, ok := firstConditionNumber(condition.Value)
		return ok && actual.hasNum && actual.number < want
	case "greaterthan":
		want, ok := firstConditionNumber(condition.Value)
		return ok && actual.hasNum && actual.number > want
	default:
		return false
	}
}

func buildConditionValues(version catalog.FileVersion, audioStreamIndex *int) conditionValues {
	video := compatPrimaryVideoTrack(version)
	audio := compatAudioTrack(version, audioStreamIndex)

	values := conditionValues{
		"videorangetype": {text: compatVideoRangeType(video, version.HDR)},
		"isinterlaced":   {text: strconv.FormatBool(video.Interlaced)},
	}

	// Everything below is only inserted when Silo actually knows the value.
	// An absent key falls through to conditionMatches' IsRequired handling,
	// mirroring how Jellyfin's ConditionProcessor treats a null value.
	if strings.TrimSpace(video.Profile) != "" {
		values["videoprofile"] = conditionValue{text: video.Profile}
	}
	if level := intConditionValue(video.Level); level.hasNum {
		values["videolevel"] = level
	}
	if refFrames := intConditionValue(video.ReferenceFrames); refFrames.hasNum {
		values["refframes"] = refFrames
	}
	if width := intConditionValue(video.Width); width.hasNum {
		values["width"] = width
	}
	if height := intConditionValue(video.Height); height.hasNum {
		values["height"] = height
	}
	if bitDepth := intConditionValue(video.BitDepth); bitDepth.hasNum {
		values["videobitdepth"] = bitDepth
	}
	if channels := intConditionValue(audio.Channels); channels.hasNum {
		values["audiochannels"] = channels
	}
	if strings.TrimSpace(video.Codec) != "" {
		values["isavc"] = conditionValue{text: strconv.FormatBool(strings.EqualFold(video.Codec, "h264"))}
	}
	if anamorphic, known := compatIsAnamorphic(video); known {
		values["isanamorphic"] = conditionValue{text: strconv.FormatBool(anamorphic)}
	}
	if frameRate := parseCompatFrameRate(video.FrameRate); frameRate > 0 {
		rounded := int(math.Round(frameRate))
		values["videoframerate"] = conditionValue{text: strconv.Itoa(rounded), number: rounded, hasNum: true}
	}
	videoBitrate := video.Bitrate
	if videoBitrate == 0 && version.Bitrate > 0 {
		videoBitrate = version.Bitrate * 1000
	}
	if videoBitrate > 0 {
		values["videobitrate"] = intConditionValue(videoBitrate)
	}
	if audio.Bitrate > 0 {
		values["audiobitrate"] = intConditionValue(audio.Bitrate)
	}
	if audio.SampleRate > 0 {
		values["audiosamplerate"] = intConditionValue(audio.SampleRate)
	}
	if strings.TrimSpace(audio.Profile) != "" {
		values["audioprofile"] = conditionValue{text: audio.Profile}
	}
	return values
}

// compatIsAnamorphic reports whether a video track's display aspect ratio
// disagrees with its storage aspect ratio, and whether that determination
// could be made at all. Jellyfin treats an unknown value as "no answer"
// rather than false, so callers must respect the second return value.
func compatIsAnamorphic(track models.VideoTrack) (anamorphic, known bool) {
	displayRatio, ok := parseCompatAspectRatio(track.AspectRatio)
	if !ok || track.Width <= 0 || track.Height <= 0 {
		return false, false
	}
	storageRatio := float64(track.Width) / float64(track.Height)
	// The tolerance absorbs rounding in reported DARs, e.g. 1920x816
	// reported as "40:17".
	const tolerance = 0.02
	return math.Abs(displayRatio-storageRatio)/storageRatio > tolerance, true
}

// parseCompatAspectRatio parses an ffprobe display aspect ratio ("16:9").
// A bare decimal ratio is accepted too; anything else is unknown.
func parseCompatAspectRatio(raw string) (float64, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, false
	}
	if numerator, denominator, found := strings.Cut(value, ":"); found {
		width, widthErr := strconv.ParseFloat(strings.TrimSpace(numerator), 64)
		height, heightErr := strconv.ParseFloat(strings.TrimSpace(denominator), 64)
		if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
			return 0, false
		}
		return width / height, true
	}
	ratio, err := strconv.ParseFloat(value, 64)
	if err != nil || ratio <= 0 {
		return 0, false
	}
	return ratio, true
}

func intConditionValue(value int) conditionValue {
	return conditionValue{text: strconv.Itoa(value), number: value, hasNum: value > 0}
}

func compatPrimaryVideoTrack(version catalog.FileVersion) models.VideoTrack {
	if len(version.VideoTracks) > 0 {
		return version.VideoTracks[0]
	}
	return models.VideoTrack{Codec: version.CodecVideo}
}

func compatAudioTrack(version catalog.FileVersion, streamIndex *int) models.AudioTrack {
	if streamIndex != nil {
		audioIndex := *streamIndex - len(version.VideoTracks)
		if audioIndex >= 0 && audioIndex < len(version.AudioTracks) {
			return version.AudioTracks[audioIndex]
		}
	}
	for _, track := range version.AudioTracks {
		if track.Default {
			return track
		}
	}
	if len(version.AudioTracks) > 0 {
		return version.AudioTracks[0]
	}
	return models.AudioTrack{Codec: version.CodecAudio}
}

func compatAudioCodec(version catalog.FileVersion, streamIndex *int) string {
	if track := compatAudioTrack(version, streamIndex); track.Codec != "" {
		return track.Codec
	}
	return version.CodecAudio
}

func compatVideoRange(track models.VideoTrack, versionHDR bool) string {
	rangeType := compatVideoRangeType(track, versionHDR)
	switch rangeType {
	case "SDR":
		return "SDR"
	case "Unknown":
		return "Unknown"
	default:
		return "HDR"
	}
}

func compatVideoRangeType(track models.VideoTrack, versionHDR bool) string {
	if value := strings.TrimSpace(track.VideoRangeType); value != "" {
		return value
	}
	if profile := compatDolbyVisionProfile(track); profile > 0 {
		switch profile {
		case 5:
			return "DOVI"
		case 7:
			if track.HDR10Plus {
				return "DOVIWithELHDR10Plus"
			}
			return "DOVIWithEL"
		case 8:
			if track.HDR10Plus {
				return "DOVIWithHDR10Plus"
			}
			switch track.DVBLCompatID {
			case 1:
				return "DOVIWithHDR10"
			case 2:
				return "DOVIWithSDR"
			case 4:
				return "DOVIWithHLG"
			default:
				if compatIsHLG(track.ColorTransfer) {
					return "DOVIWithHLG"
				}
				if compatIsHDR(track.ColorTransfer) || versionHDR || strings.EqualFold(track.VideoRange, "HDR") {
					return "DOVIWithHDR10"
				}
				return "DOVIWithSDR"
			}
		default:
			return "DOVI"
		}
	}
	if track.HDR10Plus {
		return "HDR10Plus"
	}
	if compatIsHLG(track.ColorTransfer) {
		return "HLG"
	}
	if compatIsHDR(track.ColorTransfer) || versionHDR || strings.EqualFold(track.VideoRange, "HDR") {
		return "HDR10"
	}
	return "SDR"
}

func compatDolbyVisionProfile(track models.VideoTrack) int {
	if track.DVProfile > 0 {
		return track.DVProfile
	}
	raw := strings.ToLower(track.DolbyVision)
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool {
		return r < '0' || r > '9'
	}) {
		if value, err := strconv.Atoi(field); err == nil && value > 0 {
			return value
		}
	}
	return 0
}

func compatIsHDR(colorTransfer string) bool {
	value := strings.ToLower(colorTransfer)
	return strings.Contains(value, "smpte2084") || strings.Contains(value, "arib-std-b67")
}

func compatIsHLG(colorTransfer string) bool {
	return strings.Contains(strings.ToLower(colorTransfer), "arib-std-b67")
}

func stringInConditionSet(actual, rawSet string) bool {
	actual = normalizeConditionToken(actual)
	for _, value := range splitConditionSet(rawSet) {
		if actual == normalizeConditionToken(value) {
			return true
		}
	}
	return false
}

func splitConditionSet(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == '|' || r == ','
	})
}

func firstConditionNumber(raw string) (int, bool) {
	for _, value := range splitConditionSet(raw) {
		number, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return number, true
		}
	}
	return 0, false
}

func normalizeConditionToken(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
