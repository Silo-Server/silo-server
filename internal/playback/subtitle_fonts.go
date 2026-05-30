package playback

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	maxSubtitleFontAttachments = 32
	maxSubtitleFontBytes       = 32 << 20 // 32 MiB
)

// SubtitleFontAttachment is a font attached to a media container for ASS/SSA
// subtitle rendering.
type SubtitleFontAttachment struct {
	Name string
	Data []byte
}

type attachmentProbeOutput struct {
	Streams []attachmentProbeStream `json:"streams"`
}

type attachmentProbeStream struct {
	Index     int               `json:"index"`
	CodecName string            `json:"codec_name"`
	CodecType string            `json:"codec_type"`
	Tags      map[string]string `json:"tags"`
}

// ExtractAttachedSubtitleFonts extracts font attachments from a media file.
// Matroska ASS releases commonly include the exact fonts needed by the script;
// loading them into JASSUB is the closest browser equivalent to libass on a
// native player.
func ExtractAttachedSubtitleFonts(ctx context.Context, inputPath string, ffmpegPath string) ([]SubtitleFontAttachment, error) {
	if strings.TrimSpace(inputPath) == "" {
		return nil, fmt.Errorf("subtitle fonts: input path is required")
	}

	streams, err := probeFontAttachmentStreams(ctx, inputPath, ffprobePathFromFFmpeg(ffmpegPath))
	if err != nil {
		return nil, err
	}
	if len(streams) == 0 {
		return nil, nil
	}
	if len(streams) > maxSubtitleFontAttachments {
		streams = streams[:maxSubtitleFontAttachments]
	}

	tmpDir, err := os.MkdirTemp("", "silo-subtitle-fonts-*")
	if err != nil {
		return nil, fmt.Errorf("subtitle fonts: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	args := []string{"-hide_banner", "-nostats", "-loglevel", "error"}
	outputNames := make([]string, 0, len(streams))
	for i, stream := range streams {
		name := fmt.Sprintf("attachment-%d%s", i, fontAttachmentExt(stream))
		outputNames = append(outputNames, name)
		args = append(args, fmt.Sprintf("-dump_attachment:%d", stream.Index), name)
	}
	args = append(args, "-i", inputPath, "-map", "0:t?", "-c", "copy", "-f", "null", "-")

	bin := ffmpegPath
	if strings.TrimSpace(bin) == "" {
		bin = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = tmpDir
	stderr, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("subtitle fonts: extract attachments: %w (stderr: %s)", err, truncateStderr(string(stderr)))
	}

	var total int64
	fonts := make([]SubtitleFontAttachment, 0, len(streams))
	for i, name := range outputNames {
		path := filepath.Join(tmpDir, name)
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("subtitle fonts: read extracted attachment %q: %w", name, err)
		}
		total += info.Size()
		if total > maxSubtitleFontBytes {
			return nil, fmt.Errorf("subtitle fonts: attached font data exceeds %d bytes", maxSubtitleFontBytes)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("subtitle fonts: read extracted attachment %q: %w", name, err)
		}
		fonts = append(fonts, SubtitleFontAttachment{
			Name: safeAttachmentDisplayName(streams[i], name),
			Data: data,
		})
	}

	return fonts, nil
}

func probeFontAttachmentStreams(ctx context.Context, inputPath string, ffprobePath string) ([]attachmentProbeStream, error) {
	bin := ffprobePath
	if strings.TrimSpace(bin) == "" {
		bin = "ffprobe"
	}
	cmd := exec.CommandContext(ctx, bin,
		"-v", "error",
		"-select_streams", "t",
		"-show_entries", "stream=index,codec_name,codec_type:stream_tags=filename,mimetype",
		"-of", "json",
		inputPath,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("subtitle fonts: probe attachments: %w", err)
	}

	var probed attachmentProbeOutput
	if err := json.Unmarshal(out, &probed); err != nil {
		return nil, fmt.Errorf("subtitle fonts: parse attachment probe: %w", err)
	}

	streams := make([]attachmentProbeStream, 0, len(probed.Streams))
	for _, stream := range probed.Streams {
		if isFontAttachment(stream) {
			streams = append(streams, stream)
		}
	}
	return streams, nil
}

func isFontAttachment(stream attachmentProbeStream) bool {
	if strings.ToLower(stream.CodecType) != "attachment" {
		return false
	}
	codec := strings.ToLower(stream.CodecName)
	switch codec {
	case "ttf", "otf", "ttc", "otc", "woff", "woff2":
		return true
	}
	filename := strings.ToLower(stream.Tags["filename"])
	switch filepath.Ext(filename) {
	case ".ttf", ".otf", ".ttc", ".otc", ".woff", ".woff2":
		return true
	}
	mimetype := strings.ToLower(stream.Tags["mimetype"])
	return strings.Contains(mimetype, "font") ||
		strings.Contains(mimetype, "truetype") ||
		strings.Contains(mimetype, "opentype") ||
		strings.Contains(mimetype, "woff")
}

func fontAttachmentExt(stream attachmentProbeStream) string {
	if ext := strings.ToLower(filepath.Ext(stream.Tags["filename"])); isSupportedFontExt(ext) {
		return ext
	}
	switch strings.ToLower(stream.CodecName) {
	case "ttf":
		return ".ttf"
	case "otf":
		return ".otf"
	case "ttc":
		return ".ttc"
	case "otc":
		return ".otc"
	case "woff":
		return ".woff"
	case "woff2":
		return ".woff2"
	default:
		return ".font"
	}
}

func isSupportedFontExt(ext string) bool {
	switch ext {
	case ".ttf", ".otf", ".ttc", ".otc", ".woff", ".woff2":
		return true
	default:
		return false
	}
}

func safeAttachmentDisplayName(stream attachmentProbeStream, fallback string) string {
	name := filepath.Base(stream.Tags["filename"])
	if name == "." || name == string(filepath.Separator) || strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}

func ffprobePathFromFFmpeg(ffmpegPath string) string {
	if strings.TrimSpace(ffmpegPath) == "" {
		return "ffprobe"
	}
	if i := strings.LastIndex(ffmpegPath, "ffmpeg"); i >= 0 {
		return ffmpegPath[:i] + "ffprobe" + ffmpegPath[i+len("ffmpeg"):]
	}
	return "ffprobe"
}
