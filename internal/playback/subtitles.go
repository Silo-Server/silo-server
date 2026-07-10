package playback

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// bitmapSubtitleCodecs lists subtitle codecs that cannot be extracted as text
// and must be burned into the video stream.
var bitmapSubtitleCodecs = map[string]bool{
	"pgs":               true,
	"hdmv_pgs_subtitle": true,
	"dvd_subtitle":      true,
	"dvb_subtitle":      true,
}

// NeedsBurnIn reports whether the given subtitle codec is bitmap-based and
// requires burning into the video stream (cannot be extracted as text).
func NeedsBurnIn(subtitleCodec string) bool {
	return bitmapSubtitleCodecs[strings.ToLower(subtitleCodec)]
}

// pgsSubtitleCodecs lists PGS (Blu-ray bitmap) subtitle codec names. Unlike
// other bitmap codecs, PGS can be extracted losslessly to a .sup elementary
// stream for capable native clients, so burn-in is not the only delivery
// option. The web player burns in all bitmap codecs; DVD/DVB bitmap subs also
// require burn-in for native clients that cannot render them directly.
var pgsSubtitleCodecs = map[string]bool{
	"pgs":               true,
	"hdmv_pgs_subtitle": true,
}

// IsPGS reports whether the given subtitle codec is PGS format.
func IsPGS(codec string) bool {
	return pgsSubtitleCodecs[strings.ToLower(codec)]
}

// assSubtitleCodecs lists subtitle codecs that are ASS/SSA format and support
// rich styling (fonts, colors, positioning, karaoke, typesetting).
var assSubtitleCodecs = map[string]bool{
	"ass": true,
	"ssa": true,
}

// IsASS reports whether the given subtitle codec is ASS/SSA format.
func IsASS(codec string) bool {
	return assSubtitleCodecs[strings.ToLower(codec)]
}

// ExtractSubtitle extracts a subtitle track from a media file using ffmpeg.
// It returns the raw subtitle data, the detected format (e.g., "srt", "ass"),
// and any error encountered.
func ExtractSubtitle(ctx context.Context, filePath string, trackIndex int, ffmpegPath ...string) ([]byte, string, error) {
	// Determine the output format based on what ffmpeg can extract.
	// We default to SRT as a safe text-based format.
	outputFormat := "srt"

	args := []string{
		"-i", filePath,
		"-map", fmt.Sprintf("0:s:%d", trackIndex),
		"-f", outputFormat,
		"pipe:1",
	}

	ffmpeg := "ffmpeg"
	if len(ffmpegPath) > 0 && ffmpegPath[0] != "" {
		ffmpeg = ffmpegPath[0]
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("ffmpeg subtitle extraction failed: %w (stderr: %s)",
			err, truncateStderr(stderr.String()))
	}

	data := stdout.Bytes()
	if len(data) == 0 {
		return nil, "", fmt.Errorf("ffmpeg produced empty subtitle output for track %d", trackIndex)
	}

	return data, outputFormat, nil
}

// ExtractSubtitleWithFormat extracts a subtitle track from a media file in the
// specified output format. Only "srt" and "ass" are allowed as output formats.
func ExtractSubtitleWithFormat(ctx context.Context, filePath string, trackIndex int, outputFormat string, ffmpegPath ...string) ([]byte, error) {
	switch outputFormat {
	case "srt", "ass":
	default:
		return nil, fmt.Errorf("unsupported subtitle extraction format: %q (must be \"srt\" or \"ass\")", outputFormat)
	}

	args := []string{
		"-i", filePath,
		"-map", fmt.Sprintf("0:s:%d", trackIndex),
		"-f", outputFormat,
		"pipe:1",
	}

	ffmpeg := "ffmpeg"
	if len(ffmpegPath) > 0 && ffmpegPath[0] != "" {
		ffmpeg = ffmpegPath[0]
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg subtitle extraction failed: %w (stderr: %s)",
			err, truncateStderr(stderr.String()))
	}

	data := stdout.Bytes()
	if len(data) == 0 {
		return nil, fmt.Errorf("ffmpeg produced empty subtitle output for track %d", trackIndex)
	}

	return data, nil
}

// LoadExternalSubtitleRaw reads an external subtitle file and returns its raw
// contents without any conversion. Used for serving ASS/SSA files directly.
func LoadExternalSubtitleRaw(subtitlePath string) ([]byte, error) {
	data, err := os.ReadFile(subtitlePath)
	if err != nil {
		return nil, fmt.Errorf("read external subtitle: %w", err)
	}
	return data, nil
}

// ParseSubtitleTrackParam parses a subtitle track URL parameter that may
// contain an optional format extension (e.g. "5" or "5.ass" or "5.vtt").
// Returns the track index and the requested format (empty string if no
// extension was provided).
func ParseSubtitleTrackParam(param string) (int, string, error) {
	format := ""
	indexStr := param

	if dotIdx := strings.LastIndex(param, "."); dotIdx >= 0 {
		format = strings.ToLower(param[dotIdx+1:])
		indexStr = param[:dotIdx]
	}

	index, err := strconv.Atoi(indexStr)
	if err != nil || index < 0 {
		return 0, "", fmt.Errorf("invalid subtitle track parameter: %q", param)
	}

	return index, format, nil
}

// ServeSubtitle writes subtitle data to the HTTP response with the appropriate
// content type for the given format.
func ServeSubtitle(w http.ResponseWriter, data []byte, format string) {
	switch strings.ToLower(format) {
	case "ass", "ssa":
		w.Header().Set("Content-Type", "text/x-ssa; charset=utf-8")
	case "srt", "subrip":
		w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// LoadExternalSubtitleAsVTT reads a sidecar subtitle file and converts it to
// WebVTT for player consumption.
func LoadExternalSubtitleAsVTT(ctx context.Context, subtitlePath, format string, ffmpegPath ...string) ([]byte, error) {
	switch strings.ToLower(format) {
	case "srt", "vtt", "webvtt":
		data, err := os.ReadFile(subtitlePath)
		if err != nil {
			return nil, fmt.Errorf("read external subtitle: %w", err)
		}
		return ConvertToVTT(data, format)
	default:
		args := []string{
			"-i", subtitlePath,
			"-f", "webvtt",
			"pipe:1",
		}

		ffmpeg := "ffmpeg"
		if len(ffmpegPath) > 0 && ffmpegPath[0] != "" {
			ffmpeg = ffmpegPath[0]
		}
		cmd := exec.CommandContext(ctx, ffmpeg, args...)

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("ffmpeg external subtitle conversion failed: %w (stderr: %s)",
				err, truncateStderr(stderr.String()))
		}

		data := stdout.Bytes()
		if len(data) == 0 {
			return nil, fmt.Errorf("ffmpeg produced empty subtitle output for %s", subtitlePath)
		}

		return data, nil
	}
}

// ConvertToVTT converts subtitle data from a given format to WebVTT.
// Currently supports conversion from SRT format.
func ConvertToVTT(input []byte, fromFormat string) ([]byte, error) {
	switch strings.ToLower(fromFormat) {
	case "srt":
		return srtToVTT(input), nil
	case "vtt", "webvtt":
		// Already VTT, return as-is.
		return input, nil
	default:
		return nil, fmt.Errorf("unsupported subtitle format for VTT conversion: %s", fromFormat)
	}
}

// srtToVTT converts SRT subtitle content to WebVTT format.
// SRT uses commas for millisecond separators; VTT uses periods.
// VTT requires a "WEBVTT" header.
func srtToVTT(input []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("WEBVTT\n\n")

	lines := strings.SplitSeq(string(input), "\n")
	for line := range lines {
		// SRT timestamps use comma: 00:01:23,456 --> 00:01:25,789
		// VTT timestamps use period: 00:01:23.456 --> 00:01:25.789
		if isSRTTimeLine(line) {
			line = strings.ReplaceAll(line, ",", ".")
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}

	return buf.Bytes()
}

// isSRTTimeLine checks if a line looks like an SRT timestamp line.
// Format: 00:01:23,456 --> 00:01:25,789
func isSRTTimeLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "-->") {
		return false
	}
	parts := strings.Split(trimmed, "-->")
	if len(parts) != 2 {
		return false
	}
	// Check that the left side looks like a timestamp with a comma.
	left := strings.TrimSpace(parts[0])
	return strings.Contains(left, ",") && strings.Contains(left, ":")
}

// truncateStderr limits stderr output to a reasonable length for error messages.
func truncateStderr(s string) string {
	const maxLen = 500
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// FormatSubtitleTrackArg formats a subtitle track index for ffmpeg mapping.
func FormatSubtitleTrackArg(trackIndex int) string {
	return "0:s:" + strconv.Itoa(trackIndex)
}
