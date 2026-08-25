// Package mediaprobe owns the FFprobe representation and normalization shared
// by catalog scans and execution-time media validation.
package mediaprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrPrimaryVideoNotFound reports that FFprobe completed successfully but did
// not describe a playable primary video stream.
var ErrPrimaryVideoNotFound = errors.New("primary video stream not found")

// ScalarString accepts FFprobe fields emitted as either JSON strings or
// numbers depending on codec and container details.
type ScalarString string

// UnmarshalJSON accepts either a quoted scalar or a numeric FFprobe value.
func (s *ScalarString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = ""
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		*s = ScalarString(str)
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(data, &num); err == nil {
		*s = ScalarString(num.String())
		return nil
	}
	return fmt.Errorf("unsupported ffprobe scalar %s", string(data))
}

// Stream is the FFprobe stream shape shared by full scans and live validation.
type Stream struct {
	Index              int               `json:"index"`
	CodecName          string            `json:"codec_name"`
	CodecLongName      string            `json:"codec_long_name"`
	CodecType          string            `json:"codec_type"`
	Profile            string            `json:"profile"`
	Level              int               `json:"level"`
	Width              int               `json:"width"`
	Height             int               `json:"height"`
	DisplayAspectRatio string            `json:"display_aspect_ratio"`
	FieldOrder         string            `json:"field_order"`
	AvgFrameRate       string            `json:"avg_frame_rate"`
	StartTime          string            `json:"start_time"`
	Duration           string            `json:"duration"`
	BitRate            string            `json:"bit_rate"`
	ColorRange         string            `json:"color_range"`
	ColorTransfer      string            `json:"color_transfer"`
	ColorPrimaries     string            `json:"color_primaries"`
	ColorSpace         string            `json:"color_space"`
	PixFmt             string            `json:"pix_fmt"`
	Refs               int               `json:"refs"`
	BitsPerRawSample   ScalarString      `json:"bits_per_raw_sample"`
	BitsPerSample      ScalarString      `json:"bits_per_sample"`
	Channels           int               `json:"channels"`
	ChannelLayout      string            `json:"channel_layout"`
	SampleRate         string            `json:"sample_rate"`
	Disposition        Disposition       `json:"disposition"`
	Tags               map[string]string `json:"tags"`
	SideDataList       []SideData        `json:"side_data_list"`
}

// Disposition is the subset of FFprobe disposition flags used by scans.
type Disposition struct {
	Default     int `json:"default"`
	Forced      int `json:"forced"`
	AttachedPic int `json:"attached_pic"`
}

// SideData describes FFprobe metadata attached to a stream.
type SideData struct {
	SideDataType        string `json:"side_data_type"`
	DVProfile           int    `json:"dv_profile"`
	DVLevel             int    `json:"dv_level"`
	DVBlPresent         int    `json:"bl_present_flag"`
	DVElPresent         int    `json:"el_present_flag"`
	DVRPUPresent        int    `json:"rpu_present_flag"`
	DVBlPresentLegacy   int    `json:"dv_bl_present"`
	DVElPresentLegacy   int    `json:"dv_el_present"`
	DVRPUPresentLegacy  int    `json:"dv_rpu_present"`
	DVBLCompatID        int    `json:"dv_bl_signal_compatibility_id"`
	DVBLCompatIDPresent bool   `json:"-"`
}

// UnmarshalJSON preserves whether FFprobe emitted a Dolby Vision compatibility
// identifier so an explicit zero is not confused with an omitted field.
func (d *SideData) UnmarshalJSON(data []byte) error {
	type alias SideData
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*d = SideData(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, d.DVBLCompatIDPresent = fields["dv_bl_signal_compatibility_id"]
	return nil
}

// ProbePrimaryVideoTrack runs a metadata-only FFprobe and returns the first
// playable video stream using the same normalization as a catalog scan.
func ProbePrimaryVideoTrack(ctx context.Context, ffprobePath, filePath string) (models.VideoTrack, error) {
	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-select_streams", "v",
		filePath,
	)
	output, err := cmd.Output()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return models.VideoTrack{}, contextErr
		}
		return models.VideoTrack{}, fmt.Errorf("ffprobe primary video failed for %s: %w", filePath, err)
	}
	var raw struct {
		Streams []Stream `json:"streams"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return models.VideoTrack{}, fmt.Errorf("ffprobe primary video JSON parse failed for %s: %w", filePath, err)
	}
	for _, stream := range raw.Streams {
		if IsPrimaryVideoStream(stream) {
			return NormalizeVideoTrack(stream), nil
		}
	}
	return models.VideoTrack{}, ErrPrimaryVideoNotFound
}

// FFprobePathFromFFmpeg derives a sibling FFprobe executable from FFmpeg's
// configured path, including platform-specific executable suffixes.
func FFprobePathFromFFmpeg(ffmpegPath string) string {
	ffmpegPath = strings.TrimSpace(ffmpegPath)
	if ffmpegPath == "" {
		return "ffprobe"
	}
	base := filepath.Base(ffmpegPath)
	if i := strings.LastIndex(strings.ToLower(base), "ffmpeg"); i >= 0 {
		return filepath.Join(filepath.Dir(ffmpegPath), base[:i]+"ffprobe"+base[i+len("ffmpeg"):])
	}
	return "ffprobe"
}

// IsPrimaryVideoStream excludes attached cover art from playable video facts.
func IsPrimaryVideoStream(stream Stream) bool {
	return stream.CodecType == "video" && stream.Disposition.AttachedPic == 0
}

// NormalizeVideoTrack converts FFprobe's raw video facts into the catalog's
// authoritative normalized representation.
func NormalizeVideoTrack(stream Stream) models.VideoTrack {
	dvConfig, dvConfigPresent := dolbyVisionConfigRecord(stream.SideDataList)
	dvProfile := positive(dvConfig.DVProfile)
	dvLevel := positive(dvConfig.DVLevel)
	dvELPresent := dvConfig.DVElPresent > 0 || dvConfig.DVElPresentLegacy > 0
	colorRange := firstNonEmpty(stream.ColorRange, "unknown")
	hdr10Plus := hasHDR10Plus(stream.SideDataList)
	return models.VideoTrack{
		Title:               firstNonEmpty(stream.Tags["title"], stream.CodecLongName, strings.ToUpper(stream.CodecName)),
		Codec:               stream.CodecName,
		DolbyVision:         dolbyVisionProfile(dvProfile),
		DVProfile:           dvProfile,
		DVLevel:             dvLevel,
		DVBLCompatID:        dvConfig.DVBLCompatID,
		DVConfigPresent:     dvConfigPresent,
		DVBLCompatIDPresent: dvConfigPresent && (dvConfig.DVBLCompatIDPresent || dvConfig.DVBLCompatID != 0),
		DVBLPresent:         dvConfig.DVBlPresent > 0 || dvConfig.DVBlPresentLegacy > 0,
		DVRPUPresent:        dvConfig.DVRPUPresent > 0 || dvConfig.DVRPUPresentLegacy > 0,
		DVELPresent:         dvELPresent,
		DVEnhancementLayer:  enhancementLayer(dvELPresent),
		HDR10Plus:           hdr10Plus,
		Profile:             stream.Profile,
		Level:               stream.Level,
		Width:               stream.Width,
		Height:              stream.Height,
		AspectRatio:         stream.DisplayAspectRatio,
		Interlaced:          isInterlaced(stream.FieldOrder),
		FrameRate:           normalizeFrameRate(stream.AvgFrameRate),
		Bitrate:             parseNumeric(stream.BitRate) / 1000,
		VideoRange:          videoRangeLabel(stream, dvProfile),
		VideoRangeType:      videoRangeType(stream, dvProfile, dvConfig.DVBLCompatID, hdr10Plus),
		ColorRange:          colorRange,
		ColorPrimaries:      stream.ColorPrimaries,
		ColorSpace:          stream.ColorSpace,
		ColorTransfer:       stream.ColorTransfer,
		BitDepth:            models.NormalizeVideoBitDepth(parseBitDepth(stream), stream.PixFmt, stream.Profile),
		PixelFormat:         stream.PixFmt,
		ReferenceFrames:     stream.Refs,
	}
}

func dolbyVisionConfigRecord(sideData []SideData) (SideData, bool) {
	for _, data := range sideData {
		if strings.EqualFold(data.SideDataType, "DOVI configuration record") {
			return data, true
		}
	}
	return SideData{}, false
}

func positive(value int) int {
	if value > 0 {
		return value
	}
	return 0
}

func dolbyVisionProfile(profile int) string {
	if profile > 0 {
		return fmt.Sprintf("Profile %d", profile)
	}
	return ""
}

func enhancementLayer(present bool) string {
	if present {
		return "unknown"
	}
	return "none"
}

func hasHDR10Plus(sideData []SideData) bool {
	for _, data := range sideData {
		typ := strings.ToLower(data.SideDataType)
		if strings.Contains(typ, "hdr10+") || strings.Contains(typ, "smpte2094-40") {
			return true
		}
	}
	return false
}

func videoRangeLabel(stream Stream, dvProfile int) string {
	if dvProfile > 0 {
		return "DolbyVision"
	}
	if isHDR(stream.ColorTransfer) {
		return "HDR"
	}
	return ""
}

func videoRangeType(stream Stream, dvProfile, compatID int, hdr10Plus bool) string {
	if dvProfile > 0 {
		switch dvProfile {
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
			switch compatID {
			case 1:
				return "DOVIWithHDR10"
			case 2:
				return "DOVIWithSDR"
			case 4:
				return "DOVIWithHLG"
			default:
				if isHLG(stream.ColorTransfer) {
					return "DOVIWithHLG"
				}
				if isHDR(stream.ColorTransfer) {
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
	if isHLG(stream.ColorTransfer) {
		return "HLG"
	}
	if isHDR(stream.ColorTransfer) {
		return "HDR10"
	}
	return "SDR"
}

func parseBitDepth(stream Stream) int {
	if value := parseNumeric(string(stream.BitsPerRawSample)); value > 0 {
		return value
	}
	return parseNumeric(string(stream.BitsPerSample))
}

func parseNumeric(raw string) int {
	value, _ := strconv.Atoi(raw)
	return value
}

func normalizeFrameRate(raw string) string {
	if raw == "" || raw == "0/0" {
		return ""
	}
	if !strings.Contains(raw, "/") {
		return raw
	}
	parts := strings.SplitN(strings.TrimSpace(raw), "/", 2)
	if len(parts) != 2 {
		return raw
	}
	numerator, errNumerator := strconv.ParseFloat(parts[0], 64)
	denominator, errDenominator := strconv.ParseFloat(parts[1], 64)
	if errNumerator != nil || errDenominator != nil || denominator == 0 {
		return raw
	}
	fps := numerator / denominator
	if fps == 0 {
		return raw
	}
	return strconv.FormatFloat(fps, 'f', 3, 64)
}

func isInterlaced(fieldOrder string) bool {
	switch strings.ToLower(fieldOrder) {
	case "tt", "bb", "tb", "bt":
		return true
	default:
		return false
	}
}

func isHDR(colorTransfer string) bool {
	value := strings.ToLower(colorTransfer)
	return strings.Contains(value, "smpte2084") || strings.Contains(value, "arib-std-b67")
}

func isHLG(colorTransfer string) bool {
	return strings.Contains(strings.ToLower(colorTransfer), "arib-std-b67")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
