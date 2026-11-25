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

	ffmpegMapArg             = "-map"
	ffmpegVideoFramesArg     = "-frames:v"
	ffmpegNullMuxer          = "null"
	subtitleFontTTFMIME      = "font/ttf"
	subtitleFontFallbackMIME = "application/octet-stream"
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
	streams, _, err := probeFontAttachmentStreams(ctx, inputPath, ffprobePathFromFFmpeg(ffmpegPath))
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

// ExtractAttachedSubtitleFonts extracts the font attachments of a media file
// for its embedded ASS/SSA track at subtitleTrack, the track's ordinal among
// the file's subtitle streams. Matroska ASS releases commonly include the exact
// fonts needed by the script; loading them into JASSUB is the closest browser
// equivalent to libass on a native player. The result holds at most 32 fonts
// and 32 MiB of font data, in container order. When the file attaches more,
// the attachments whose family the track's script names come first and the
// rest fill what room is left in container order, so it can be partial or
// empty with a nil error. The script's styles come from the track header;
// its \fn overrides count once cache, which may be nil, holds the extracted
// track.
func ExtractAttachedSubtitleFonts(ctx context.Context, inputPath string, ffmpegPath string, subtitleTrack int, cache *SubtitleCache) ([]SubtitleFontAttachment, error) {
	return extractSubtitleFontBundle(ctx, inputPath, ffmpegPath, func(ctx context.Context) []string {
		return subtitleScriptFonts(ctx, inputPath, subtitleTrack, cache)
	})
}

// ExtractAttachedSubtitleFont extracts only the requested container stream.
// Font validation and the attachment count and byte limits match bundles, but a
// font over the byte limit is an error here, where a bundle skips it.
func ExtractAttachedSubtitleFont(ctx context.Context, inputPath, ffmpegPath string, index int) (*SubtitleFontAttachment, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	streams, videoStream, err := probeSubtitleFontStreams(ctx, inputPath, ffmpegPath)
	if err != nil || len(streams) == 0 {
		return nil, err
	}
	if len(streams) > maxSubtitleFontAttachments {
		streams = streams[:maxSubtitleFontAttachments]
	}
	selected := slices.IndexFunc(streams, func(stream attachmentProbeStream) bool { return stream.Index == index })
	if selected < 0 {
		return nil, nil
	}
	name := safeAttachmentDisplayName(streams[selected], fmt.Sprintf("attachment-%d%s", selected, fontAttachmentExt(streams[selected])))
	fonts, err := dumpFontAttachments(ctx, inputPath, resolveFontFFmpeg(ffmpegPath), streams[selected:selected+1], videoStream, maxSubtitleFontBytes)
	if err != nil || len(fonts) == 0 {
		return nil, err
	}
	fonts[0].Name = name
	return &fonts[0], nil
}

// extractSubtitleFontBundle builds a bundle within the attachment count and
// byte limits. scriptFonts names the font families the subtitle script uses;
// it is called only when the attachments do not all fit, since reading the
// script and the fonts' own names costs more than the bundle itself.
func extractSubtitleFontBundle(ctx context.Context, inputPath, ffmpegPath string, scriptFonts func(context.Context) []string) ([]SubtitleFontAttachment, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	streams, videoStream, err := probeSubtitleFontStreams(ctx, inputPath, ffmpegPath)
	if err != nil || len(streams) == 0 {
		return nil, err
	}
	bin := resolveFontFFmpeg(ffmpegPath)

	kept, err := chooseBundleFonts(ctx, inputPath, bin, streams, videoStream, scriptFonts)
	if err != nil {
		return nil, err
	}
	return dumpFontAttachments(ctx, inputPath, bin, kept, videoStream, maxSubtitleFontBytes)
}

// chooseBundleFonts returns the attachments a bundle holds, in container
// order. It errs only when ctx ends.
func chooseBundleFonts(ctx context.Context, inputPath, ffmpegPath string, streams []attachmentProbeStream, videoStream int, scriptFonts func(context.Context) []string) ([]attachmentProbeStream, error) {
	kept := fontStreamsWithinLimits(streams, maxSubtitleFontBytes, maxSubtitleFontAttachments)
	if len(kept) == len(streams) {
		return kept, nil
	}
	var wanted, wantedKept int
	if families := scriptFonts(ctx); len(families) > 0 {
		// Reading the attachments' names gets half of what is left of the
		// deadline, so storage too slow for it still leaves time to serve the
		// bundle in container order.
		namesCtx, cancel := ctx, context.CancelFunc(func() {})
		if deadline, ok := ctx.Deadline(); ok {
			namesCtx, cancel = context.WithDeadline(ctx, time.Now().Add(time.Until(deadline)/2))
		}
		ordered, n, err := scriptFontStreamsFirst(namesCtx, inputPath, ffmpegPath, streams, videoStream, families)
		cancel()
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			slog.WarnContext(ctx, "subtitle font bundle could not read attached font names; keeping container order",
				"component", "playback", "input", inputPath, "error", err)
		default:
			kept = fontStreamsWithinLimits(ordered, maxSubtitleFontBytes, maxSubtitleFontAttachments)
			wanted, wantedKept = n, countStreams(kept, ordered[:n])
			slices.SortFunc(kept, func(a, b attachmentProbeStream) int { return a.Index - b.Index })
		}
	}
	slog.InfoContext(ctx, "subtitle font bundle over limit; leaving out fonts that do not fit",
		"component", "playback", "input", inputPath, "omitted", len(streams)-len(kept), "kept", len(kept),
		"script_fonts", wanted, "script_fonts_kept", wantedKept,
		"limit_fonts", maxSubtitleFontAttachments, "limit_bytes", maxSubtitleFontBytes)
	return kept, nil
}

func probeSubtitleFontStreams(ctx context.Context, inputPath, ffmpegPath string) ([]attachmentProbeStream, int, error) {
	if strings.TrimSpace(inputPath) == "" {
		return nil, -1, errors.New("subtitle fonts: input path is required")
	}
	return probeFontAttachmentStreams(ctx, inputPath, ffprobePathFromFFmpeg(ffmpegPath))
}

func resolveFontFFmpeg(ffmpegPath string) string {
	if strings.TrimSpace(ffmpegPath) == "" {
		return defaultFFmpegBinary
	}
	return ffmpegPath
}

func countStreams(streams, of []attachmentProbeStream) int {
	n := 0
	for _, stream := range streams {
		if slices.ContainsFunc(of, func(o attachmentProbeStream) bool { return o.Index == stream.Index }) {
			n++
		}
	}
	return n
}

// fontStreamsWithinLimits keeps attachments, in the given order, while their
// probed sizes fit maxBytes and their count maxCount, and skips any that would
// overflow the byte limit. A release that ships more font data than the limit
// still gets each font that fits in what is left of the limit, instead of an
// error that leaves the script with no attached fonts at all. Sizes of zero or
// less are kept so dumpFontAttachments still rejects the probe.
func fontStreamsWithinLimits(streams []attachmentProbeStream, maxBytes int64, maxCount int) []attachmentProbeStream {
	kept := make([]attachmentProbeStream, 0, min(len(streams), maxCount))
	var total int64
	for _, stream := range streams {
		if len(kept) == maxCount {
			break
		}
		if stream.ExtraDataSize > maxBytes-total {
			continue
		}
		kept = append(kept, stream)
		total += max(stream.ExtraDataSize, 0)
	}
	return kept
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
// videoStream is the source's first video stream, or -1 (see
// attachmentDumpOutputArgs).
func dumpFontAttachments(ctx context.Context, inputPath string, ffmpegPath string, streams []attachmentProbeStream, videoStream int, maxBytes int64) ([]SubtitleFontAttachment, error) {
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
	data := make([]byte, 0, expected)
	fonts := make([]SubtitleFontAttachment, 0, len(streams))
	err := readFontAttachments(ctx, inputPath, ffmpegPath, streams, videoStream, func(stream attachmentProbeStream, font []byte) {
		start := len(data)
		data = append(data, font...)
		fonts = append(fonts, SubtitleFontAttachment{
			StreamIndex: stream.Index,
			Name:        safeAttachmentDisplayName(stream, fmt.Sprintf("attachment-%d%s", len(fonts), fontAttachmentExt(stream))),
			Data:        data[start:len(data):len(data)],
		})
	})
	if err != nil {
		return nil, err
	}
	return fonts, nil
}

// fontPipeDumpArgs has FFmpeg write streams' attachments to stdout, one after
// another.
func fontPipeDumpArgs(inputPath string, streams []attachmentProbeStream, videoStream int) []string {
	args := []string{ffmpegHideBannerArg, "-nostats", ffmpegLogLevelArg, ffmpegErrorLogLevel}
	for _, stream := range streams {
		args = append(args, fmt.Sprintf("-dump_attachment:%d", stream.Index), "pipe:1")
	}
	return append(append(args, "-i", inputPath), attachmentDumpOutputArgs(videoStream)...)
}

// attachmentDumpOutputArgs is the output of an FFmpeg run that dumps
// attachments, which FFmpeg does from the container's header when it opens
// the input. The output only has to let it stop there: mapping attachments
// alone, or every stream, still demuxes the whole file (38 s for a 15 GB
// Matroska file on network storage), while one video stream limited to no
// frames ends the run at once. A source without video (videoStream -1) keeps
// the attachment-only output, which reads it whole.
func attachmentDumpOutputArgs(videoStream int) []string {
	args := []string{ffmpegMapArg, "0:t?"}
	if videoStream >= 0 {
		args = []string{ffmpegMapArg, fmt.Sprintf("0:%d", videoStream), ffmpegVideoFramesArg, "0"}
	}
	return append(args, "-c", codecCopyV3, "-f", ffmpegNullMuxer, "-")
}

// probeFontAttachmentStreams returns the source's font attachments and the
// index of its first video stream, or -1.
func probeFontAttachmentStreams(ctx context.Context, inputPath string, ffprobePath string) ([]attachmentProbeStream, int, error) {
	bin := ffprobePath
	if strings.TrimSpace(bin) == "" {
		bin = "ffprobe"
	}
	cmd := fspath.CommandContext(ctx, bin,
		"-v", "error",
		"-show_entries", "stream=index,codec_name,codec_type,extradata_size:stream_tags=filename,mimetype",
		"-of", "json",
		inputPath,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, -1, err
	}
	if err := cmd.Start(); err != nil {
		return nil, -1, err
	}
	const maxProbeBytes = 1 << 20
	out, readErr := io.ReadAll(io.LimitReader(stdout, maxProbeBytes+1))
	if readErr != nil || len(out) > maxProbeBytes {
		_ = procgroup.Kill(cmd)
		_ = cmd.Wait()
		return nil, -1, errors.New("subtitle fonts: attachment metadata exceeds probe limit or could not be read")
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, -1, ctx.Err()
		}
		return nil, -1, fmt.Errorf("subtitle fonts: probe attachments: %w", err)
	}

	var probed attachmentProbeOutput
	if err := json.Unmarshal(out, &probed); err != nil {
		return nil, -1, fmt.Errorf("subtitle fonts: parse attachment probe: %w", err)
	}

	streams := make([]attachmentProbeStream, 0, len(probed.Streams))
	videoStream := -1
	for _, stream := range probed.Streams {
		switch {
		case isFontAttachment(stream):
			streams = append(streams, stream)
		case videoStream < 0 && strings.EqualFold(stream.CodecType, "video"):
			videoStream = stream.Index
		}
	}
	return streams, videoStream, nil
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
