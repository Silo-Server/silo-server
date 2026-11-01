package playback

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"

	"github.com/Silo-Server/silo-server/internal/procgroup"
)

const (
	maxSubtitleFontAttachments = 32
	maxSubtitleFontBytes       = 32 << 20 // 32 MiB
	subtitleFontTTFMIME        = "font/ttf"
	subtitleFontFallbackMIME   = "application/octet-stream"
)

// SubtitleFontAttachment is a font attached to a media container for ASS/SSA
// subtitle rendering.
type SubtitleFontAttachment struct {
	// StreamIndex is the original container stream index used by attachment routes.
	StreamIndex int
	Name        string
	Data        []byte
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
	Index         int               `json:"index"`
	ExtraDataSize int64             `json:"extradata_size"`
	CodecName     string            `json:"codec_name"`
	CodecType     string            `json:"codec_type"`
	Tags          map[string]string `json:"tags"`
}

// SubtitleFontMetadata identifies an attached font without extracting its bytes.
type SubtitleFontMetadata struct {
	Index    int
	Codec    string
	FileName string
	MimeType string
}

var subtitleFontProbeSlots = make(chan struct{}, 2)

// ListAttachedSubtitleFonts bounds concurrent metadata probes and their duration.
// Busy or unavailable probes return an error; callers can omit optional discovery.
func ListAttachedSubtitleFonts(ctx context.Context, inputPath, ffmpegPath string) ([]SubtitleFontMetadata, error) {
	select {
	case subtitleFontProbeSlots <- struct{}{}:
		defer func() { <-subtitleFontProbeSlots }()
	default:
		return nil, errors.New("subtitle font probes busy")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	streams, err := probeFontAttachmentStreams(ctx, inputPath, ffprobePathFromFFmpeg(ffmpegPath))
	if err != nil {
		return nil, err
	}
	if len(streams) > maxSubtitleFontAttachments {
		streams = streams[:maxSubtitleFontAttachments]
	}
	fonts := make([]SubtitleFontMetadata, 0, len(streams))
	for i, stream := range streams {
		name := safeAttachmentDisplayName(stream, fmt.Sprintf("attachment-%d%s", i, fontAttachmentExt(stream)))
		fonts = append(fonts, SubtitleFontMetadata{Index: stream.Index, Codec: stream.CodecName, FileName: name, MimeType: SubtitleFontMIMEType(name)})
	}
	return fonts, nil
}

// ExtractAttachedSubtitleFonts extracts font attachments from a media file.
// Matroska ASS releases commonly include the exact fonts needed by the script;
// loading them into JASSUB is the closest browser equivalent to libass on a
// native player. The result holds at most 32 fonts and 32 MiB of font data;
// fonts that do not fit are skipped in container order, so it can be partial or
// empty with a nil error.
func ExtractAttachedSubtitleFonts(ctx context.Context, inputPath string, ffmpegPath string) ([]SubtitleFontAttachment, error) {
	return extractAttachedSubtitleFonts(ctx, inputPath, ffmpegPath, nil)
}

// ExtractAttachedSubtitleFont extracts only the requested container stream.
// Font validation and the attachment count and byte limits match bundles, but a
// font over the byte limit is an error here, where a bundle skips it.
func ExtractAttachedSubtitleFont(ctx context.Context, inputPath, ffmpegPath string, index int) (*SubtitleFontAttachment, error) {
	fonts, err := extractAttachedSubtitleFonts(ctx, inputPath, ffmpegPath, &index)
	if err != nil || len(fonts) == 0 {
		return nil, err
	}
	return &fonts[0], nil
}

func extractAttachedSubtitleFonts(ctx context.Context, inputPath, ffmpegPath string, index *int) ([]SubtitleFontAttachment, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
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
	selectedName := ""
	if index != nil {
		if len(streams) > maxSubtitleFontAttachments {
			streams = streams[:maxSubtitleFontAttachments]
		}
		selected := slices.IndexFunc(streams, func(stream attachmentProbeStream) bool { return stream.Index == *index })
		if selected < 0 {
			return nil, nil
		}
		selectedName = safeAttachmentDisplayName(streams[selected], fmt.Sprintf("attachment-%d%s", selected, fontAttachmentExt(streams[selected])))
		streams = streams[selected : selected+1]
	} else {
		var omitted int
		streams, omitted = fontStreamsWithinBudget(streams, maxSubtitleFontBytes)
		if len(streams) > maxSubtitleFontAttachments {
			streams = streams[:maxSubtitleFontAttachments]
		}
		if omitted > 0 {
			slog.InfoContext(ctx, "subtitle font bundle over size limit; leaving out fonts that do not fit",
				"component", "playback", "input", inputPath, "omitted", omitted, "kept", len(streams), "limit_bytes", maxSubtitleFontBytes)
		}
	}

	bin := ffmpegPath
	if strings.TrimSpace(bin) == "" {
		bin = "ffmpeg"
	}

	fonts, err := dumpFontAttachments(ctx, inputPath, bin, streams, maxSubtitleFontBytes)
	if selectedName != "" && len(fonts) > 0 {
		fonts[0].Name = selectedName
	}
	return fonts, err
}

// fontStreamsWithinBudget keeps attachments, in container order, while their
// probed sizes fit maxBytes and skips any that would overflow it. A release that
// ships more font data than the limit still gets each font that fits in what is
// left of the limit, instead of an error that leaves the script with no attached
// fonts at all. Sizes of zero or less are kept so dumpFontAttachments still
// rejects the probe.
func fontStreamsWithinBudget(streams []attachmentProbeStream, maxBytes int64) ([]attachmentProbeStream, int) {
	kept := make([]attachmentProbeStream, 0, len(streams))
	var total int64
	for _, stream := range streams {
		if stream.ExtraDataSize > maxBytes-total {
			continue
		}
		kept = append(kept, stream)
		total += max(stream.ExtraDataSize, 0)
	}
	return kept, len(streams) - len(kept)
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

// dumpFontAttachments concatenates the selected attachments on stdout in one
// FFmpeg process. Probed extradata sizes frame each font; a bounded pipe reader
// rejects oversized or changed output without ever spilling font data to disk.
func dumpFontAttachments(ctx context.Context, inputPath string, ffmpegPath string, streams []attachmentProbeStream, maxBytes int64) ([]SubtitleFontAttachment, error) {
	var expected int64
	for _, stream := range streams {
		if stream.ExtraDataSize <= 0 {
			return nil, errors.New("subtitle fonts: invalid attachment data size")
		}
		if stream.ExtraDataSize > maxBytes-expected {
			return nil, fmt.Errorf("subtitle fonts: attached font data exceeds %d bytes", maxBytes)
		}
		expected += stream.ExtraDataSize
	}
	if expected == 0 {
		return nil, nil
	}
	args := []string{ffmpegHideBannerArg, "-nostats", ffmpegLogLevelArg, ffmpegErrorLogLevel}
	for _, stream := range streams {
		args = append(args, fmt.Sprintf("-dump_attachment:%d", stream.Index), "pipe:1")
	}
	args = append(args, "-i", inputPath, "-map", "0:t?", "-c", "copy", "-f", "null", "-")
	cmd := fspath.CommandContext(ctx, ffmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("subtitle fonts: start attachment extract: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, expected+1))
	if readErr != nil || int64(len(data)) > expected {
		_ = procgroup.Kill(cmd)
		_ = cmd.Wait()
		return nil, errors.New("subtitle fonts: attachment data exceeds probed size or could not be read")
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("subtitle fonts: extract attachments: %w", err)
	}
	if int64(len(data)) != expected {
		return nil, errors.New("subtitle fonts: attachment data differs from probed size")
	}
	fonts := make([]SubtitleFontAttachment, 0, len(streams))
	offset := 0
	for i, stream := range streams {
		end := offset + int(stream.ExtraDataSize)
		fonts = append(fonts, SubtitleFontAttachment{
			StreamIndex: stream.Index,
			Name:        safeAttachmentDisplayName(stream, fmt.Sprintf("attachment-%d%s", i, fontAttachmentExt(stream))),
			Data:        data[offset:end:end],
		})
		offset = end
	}
	return fonts, nil
}

func probeFontAttachmentStreams(ctx context.Context, inputPath string, ffprobePath string) ([]attachmentProbeStream, error) {
	bin := ffprobePath
	if strings.TrimSpace(bin) == "" {
		bin = "ffprobe"
	}
	cmd := fspath.CommandContext(ctx, bin,
		"-v", "error",
		"-select_streams", "t",
		"-show_entries", "stream=index,codec_name,codec_type,extradata_size:stream_tags=filename,mimetype",
		"-of", "json",
		inputPath,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	const maxProbeBytes = 1 << 20
	out, readErr := io.ReadAll(io.LimitReader(stdout, maxProbeBytes+1))
	if readErr != nil || len(out) > maxProbeBytes {
		_ = procgroup.Kill(cmd)
		_ = cmd.Wait()
		return nil, errors.New("subtitle fonts: attachment metadata exceeds probe limit or could not be read")
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
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
	if !isSupportedFontExt(strings.ToLower(filepath.Ext(name))) {
		return fallback
	}
	return name
}

// SubtitleFontMIMEType never trusts a container's MIME metadata or the host's
// extension registrations when serving authenticated attachment bytes inline.
func SubtitleFontMIMEType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ttf":
		return subtitleFontTTFMIME
	case ".otf":
		return "font/otf"
	case ".ttc", ".otc":
		return "font/collection"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	default:
		return subtitleFontFallbackMIME
	}
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
