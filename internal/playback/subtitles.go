package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	subtitleCodecPGS       = "pgs"
	subtitleCodecPGSShort  = "pgssub"
	subtitleCodecPGSFFmpeg = "hdmv_pgs_subtitle"
	subtitleCodecDVDShort  = "dvdsub"
	subtitleCodecVOBShort  = "vobsub"
	subtitleCodecDVDFFmpeg = "dvd_subtitle"
	subtitleCodecDVBShort  = "dvbsub"
	subtitleCodecDVBFFmpeg = "dvb_subtitle"
)

// bitmapSubtitleCodecs lists subtitle codecs that cannot be extracted as text
// and must be burned into the video stream.
var bitmapSubtitleCodecs = map[string]bool{
	subtitleCodecPGS:       true,
	subtitleCodecPGSFFmpeg: true,
	subtitleCodecDVDFFmpeg: true,
	subtitleCodecDVBFFmpeg: true,
}

// NeedsBurnIn reports whether the given subtitle codec is bitmap-based and
// requires burning into the video stream (cannot be extracted as text).
func NeedsBurnIn(subtitleCodec string) bool {
	return bitmapSubtitleCodecs[normalizeCodecV3(subtitleCodec)]
}

// pgsSubtitleCodecs lists PGS (Blu-ray bitmap) subtitle codec names. Unlike
// other bitmap codecs, PGS can be extracted losslessly to a .sup elementary
// stream for capable native clients, so burn-in is not the only delivery
// option. The web player burns in all bitmap codecs; DVD/DVB bitmap subs also
// require burn-in for native clients that cannot render them directly.
var pgsSubtitleCodecs = map[string]bool{
	subtitleCodecPGS:       true,
	subtitleCodecPGSFFmpeg: true,
}

// IsPGS reports whether the given subtitle codec is PGS format.
func IsPGS(codec string) bool {
	return pgsSubtitleCodecs[normalizeCodecV3(codec)]
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

// IsSubRip reports whether the given subtitle codec is SubRip (SRT).
func IsSubRip(codec string) bool {
	switch normalizeCodecV3(codec) {
	case subtitleFormatSRT, subtitleCodecSubRip:
		return true
	default:
		return false
	}
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

// ConvertToVTTWithFFmpeg converts subtitle formats that require a real parser
// (notably ASS/SSA) from in-memory data. It keeps downloaded subtitle
// conversion on the same executable path as external sidecar conversion.
func ConvertToVTTWithFFmpeg(ctx context.Context, input []byte, fromFormat, ffmpegPath string) ([]byte, error) {
	if converted, err := ConvertToVTT(input, fromFormat); err == nil {
		return converted, nil
	}
	inputFormat := strings.ToLower(strings.TrimSpace(fromFormat))
	if inputFormat == "ssa" {
		inputFormat = "ass"
	}
	if inputFormat == "" {
		return nil, errors.New("subtitle format is required")
	}
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", inputFormat, "-i", "pipe:0",
		"-f", "webvtt", "pipe:1",
	)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg subtitle conversion failed: %w (stderr: %s)", err, truncateStderr(stderr.String()))
	}
	if stdout.Len() == 0 {
		return nil, errors.New("ffmpeg produced empty subtitle output")
	}
	return stdout.Bytes(), nil
}

// srtToVTT converts SRT subtitle content to WebVTT format.
// SRT uses commas for millisecond separators; VTT uses periods.
// VTT requires a "WEBVTT" header.
//
// SRT authors place cues with ASS override blocks such as {\an8}. WebVTT has
// no such syntax, so a copied block renders as literal text on every WebVTT
// player and the placement is lost. The cue's \an alignment moves onto the
// timing line as WebVTT cue settings instead, and every override block is
// removed from the cue text. FFmpeg's SubRip decoder drops most of those
// blocks too, keeping only a leading \an.
func srtToVTT(input []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("WEBVTT\n\n")

	text := strings.TrimPrefix(string(input), "\ufeff")
	// \r\r\n (a CRLF file converted twice) is one break, and a lone \r is a
	// break in CR-only files. The longest sequence goes first so \r\r\n does
	// not become a blank line that ends the cue.
	text = strings.NewReplacer("\r\r\n", "\n", "\r\n", "\n", "\r", "\n").Replace(text)
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if !isSRTTimeLine(line) {
			// A whitespace-only line ends an SRT cue but not a WebVTT one.
			if strings.TrimSpace(line) == "" {
				line = ""
			}
			buf.WriteString(line)
			buf.WriteByte('\n')
			continue
		}
		// The cue text runs to the next blank line, or to the next timing
		// line when the file omits the blank line between cues. In that case
		// the line before the timing line is the next cue's number, if any.
		end := i + 1
		for end < len(lines) && strings.TrimSpace(lines[end]) != "" && !isSRTTimeLine(lines[end]) {
			end++
		}
		missingBlankLine := end < len(lines) && isSRTTimeLine(lines[end])
		if missingBlankLine && end-1 > i && isSRTCueNumber(lines[end-1]) {
			end--
		}
		alignment := 0
		cueText := make([]string, 0, end-i-1)
		for _, textLine := range lines[i+1 : end] {
			cleaned, lineAlignment := stripSRTOverrideBlocks(textLine)
			if alignment == 0 {
				alignment = lineAlignment
			}
			// A line that held only override blocks must go: left blank, it
			// would end the WebVTT cue early.
			if strings.TrimSpace(cleaned) == "" {
				continue
			}
			cueText = append(cueText, cleaned)
		}

		// SRT timestamps use comma: 00:01:23,456 --> 00:01:25,789
		// VTT timestamps use period: 00:01:23.456 --> 00:01:25.789
		buf.WriteString(strings.ReplaceAll(line, ",", "."))
		if settings := vttCueSettingsForASSAlignment(alignment); settings != "" {
			buf.WriteByte(' ')
			buf.WriteString(settings)
		}
		buf.WriteByte('\n')
		for _, textLine := range cueText {
			buf.WriteString(textLine)
			buf.WriteByte('\n')
		}
		if missingBlankLine {
			buf.WriteByte('\n')
		}
		i = end - 1
	}

	return buf.Bytes()
}

// isSRTCueNumber reports whether a line is an SRT cue number.
func isSRTCueNumber(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	for _, r := range line {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// stripSRTOverrideBlocks removes every {\...} override block from one line of
// SRT cue text and returns the first \an alignment (1-9) the blocks named, or
// 0. An unclosed block is ordinary text, as it is to FFmpeg.
func stripSRTOverrideBlocks(line string) (string, int) {
	var out strings.Builder
	alignment := 0
	for {
		start := strings.Index(line, `{\`)
		if start < 0 {
			break
		}
		length := strings.IndexByte(line[start:], '}')
		if length < 0 {
			break
		}
		out.WriteString(line[:start])
		for tag := range strings.SplitSeq(line[start+1:start+length], `\`) {
			if alignment != 0 || len(tag) != 3 || !strings.HasPrefix(tag, "an") {
				continue
			}
			if n := int(tag[2] - '0'); n >= 1 && n <= 9 {
				alignment = n
			}
		}
		line = line[start+length+1:]
	}
	out.WriteString(line)
	return out.String(), alignment
}

// SRTAlignmentTagForVTTCueSettings returns the {\anN} tag for cue settings
// that srtToVTT produced from one, or "" for any other settings. A consumer
// that writes the converted cues back out as SRT uses it to keep the
// placement.
func SRTAlignmentTagForVTTCueSettings(settings string) string {
	settings = strings.Join(strings.Fields(settings), " ")
	if settings == "" {
		return ""
	}
	for alignment := 1; alignment <= 9; alignment++ {
		if vttCueSettingsForASSAlignment(alignment) == settings {
			return `{\an` + strconv.Itoa(alignment) + `}`
		}
	}
	return ""
}

// vttCueSettingsForASSAlignment maps an ASS \an numpad alignment onto WebVTT
// cue settings. \an2 (bottom center) is the WebVTT default and, like 0 (no
// alignment), needs none. Columns use the physical left/right values because
// \an is physical, while start/end would flip on right-to-left text.
func vttCueSettingsForASSAlignment(alignment int) string {
	switch alignment {
	case 1:
		return "align:left"
	case 3:
		return "align:right"
	case 4:
		return "line:50%,center align:left"
	case 5:
		return "line:50%,center"
	case 6:
		return "line:50%,center align:right"
	case 7:
		return "line:0 align:left"
	case 8:
		return "line:0"
	case 9:
		return "line:0 align:right"
	default:
		return ""
	}
}

// srtTimestamp matches one SRT timestamp. SRT uses a comma before the
// milliseconds; some files use a period, which FFmpeg also accepts.
var srtTimestamp = regexp.MustCompile(`^\d+:\d{1,2}:\d{1,2}[,.]\d{1,3}$`)

// isSRTTimeLine reports whether a line is an SRT timing line:
// 00:01:23,456 --> 00:01:25,789, optionally followed by coordinates. Both
// timestamps must parse, because the converter ends a cue at the next timing
// line and cue text can itself contain an arrow ("Meet at 10:30. --> go").
func isSRTTimeLine(line string) bool {
	left, right, found := strings.Cut(strings.TrimSpace(line), "-->")
	if !found {
		return false
	}
	fields := strings.Fields(right)
	return len(fields) > 0 && srtTimestamp.MatchString(strings.TrimSpace(left)) && srtTimestamp.MatchString(fields[0])
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
