package playback

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

// SubtitleFontBundleItem is the JSON-safe representation sent to web players.
type SubtitleFontBundleItem struct {
	Name string `json:"name"`
	Data string `json:"data"`
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

	bin := ffmpegPath
	if strings.TrimSpace(bin) == "" {
		bin = "ffmpeg"
	}

	var total int64
	fonts := make([]SubtitleFontAttachment, 0, len(streams))
	for i, stream := range streams {
		fallbackName := fmt.Sprintf("attachment-%d%s", i, fontAttachmentExt(stream))
		remaining := maxSubtitleFontBytes - total
		data, err := extractFontAttachment(ctx, inputPath, bin, stream, fallbackName, remaining)
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		if total > maxSubtitleFontBytes {
			return nil, fmt.Errorf("subtitle fonts: attached font data exceeds %d bytes", maxSubtitleFontBytes)
		}
		fonts = append(fonts, SubtitleFontAttachment{
			Name: safeAttachmentDisplayName(stream, fallbackName),
			Data: data,
		})
	}

	return fonts, nil
}

// EncodeSubtitleFontBundle converts raw font attachments to base64 JSON items.
func EncodeSubtitleFontBundle(fonts []SubtitleFontAttachment) []SubtitleFontBundleItem {
	items := make([]SubtitleFontBundleItem, 0, len(fonts))
	for _, font := range fonts {
		items = append(items, SubtitleFontBundleItem{
			Name: font.Name,
			Data: base64.StdEncoding.EncodeToString(font.Data),
		})
	}
	return items
}

func extractFontAttachment(ctx context.Context, inputPath string, ffmpegPath string, stream attachmentProbeStream, name string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("subtitle fonts: attached font data exceeds %d bytes", maxSubtitleFontBytes)
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	args := []string{
		"-hide_banner", "-nostats", "-loglevel", "error",
		fmt.Sprintf("-dump_attachment:%d", stream.Index), "pipe:1",
		"-i", inputPath,
		"-map", "0:t?",
		"-c", "copy",
		"-f", "null", "-",
	}
	cmd := exec.CommandContext(cmdCtx, ffmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("subtitle fonts: open attachment pipe %q: %w", name, err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("subtitle fonts: start attachment extract %q: %w", name, err)
	}

	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, &io.LimitedReader{R: stdout, N: maxBytes + 1})
	tooLarge := int64(buf.Len()) > maxBytes
	if tooLarge {
		cancel()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}

	waitErr := cmd.Wait()
	if tooLarge {
		return nil, fmt.Errorf("subtitle fonts: attached font data exceeds %d bytes", maxSubtitleFontBytes)
	}
	if copyErr != nil {
		return nil, fmt.Errorf("subtitle fonts: read attachment %q: %w", name, copyErr)
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("subtitle fonts: extract attachment %q: %w (stderr: %s)",
			name, waitErr, truncateStderr(stderr.String()))
	}
	return buf.Bytes(), nil
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
	ffmpegPath = strings.TrimSpace(ffmpegPath)
	if ffmpegPath == "" {
		return "ffprobe"
	}
	base := filepath.Base(ffmpegPath)
	if i := strings.LastIndex(base, "ffmpeg"); i >= 0 {
		return filepath.Join(filepath.Dir(ffmpegPath), base[:i]+"ffprobe"+base[i+len("ffmpeg"):])
	}
	return "ffprobe"
}
