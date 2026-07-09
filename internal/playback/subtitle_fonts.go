package playback

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
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

	return dumpFontAttachments(ctx, inputPath, bin, streams, maxSubtitleFontBytes)
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

// dumpFontAttachments extracts every attachment in a single ffmpeg invocation.
//
// ffmpeg re-opens the (often network-backed) media file once per process, so
// the previous one-process-per-font approach paid that open cost N times and
// dominated latency — 17–60 s for anime releases carrying 15–47 fonts. Dumping
// all attachments in one pass opens the file once, cutting p95 from ~30 s to
// ~1–2 s. The `-map 0:t? -c copy` flags are required: without them ffmpeg
// decodes the whole video stream instead of stream-copying the attachments.
func dumpFontAttachments(ctx context.Context, inputPath string, ffmpegPath string, streams []attachmentProbeStream, maxBytes int64) ([]SubtitleFontAttachment, error) {
	dir, err := os.MkdirTemp("", "silo-subfonts-*")
	if err != nil {
		return nil, fmt.Errorf("subtitle fonts: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	// A fresh temp dir guarantees the dump targets don't pre-exist, so ffmpeg
	// never prompts to overwrite (which would hang the process).
	args := make([]string, 0, 4+len(streams)*2+8)
	args = append(args, "-hide_banner", "-nostats", "-loglevel", "error")
	paths := make([]string, len(streams))
	for i, stream := range streams {
		paths[i] = filepath.Join(dir, strconv.Itoa(i))
		args = append(args, fmt.Sprintf("-dump_attachment:%d", stream.Index), paths[i])
	}
	args = append(args, "-i", inputPath, "-map", "0:t?", "-c", "copy", "-f", "null", "-")

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("subtitle fonts: start attachment extract: %w", err)
	}

	// ffmpeg writes each attachment to disk in full before we get a chance to
	// read it, so unlike the old pipe-per-attachment reader (which killed at
	// maxBytes+1) nothing here bounds what ffmpeg spills. Guard against a
	// container whose oversized "font" attachments would otherwise fill the
	// disk by killing the process once the dump dir crosses the cap.
	overLimit, stopWatch := watchDumpSize(cmd, dir, maxBytes)

	runErr := cmd.Wait()
	stopWatch()
	if overLimit.Load() {
		return nil, fmt.Errorf("subtitle fonts: attached font data exceeds %d bytes", maxBytes)
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("subtitle fonts: extract attachments: %w (stderr: %s)",
			runErr, truncateStderr(stderr.String()))
	}

	var total int64
	fonts := make([]SubtitleFontAttachment, 0, len(streams))
	for i, stream := range streams {
		fallbackName := fmt.Sprintf("attachment-%d%s", i, fontAttachmentExt(stream))
		// Stat before reading so an over-limit attachment trips the cap
		// without being pulled into memory.
		info, err := os.Stat(paths[i])
		if errors.Is(err, os.ErrNotExist) {
			// ffmpeg silently skips attachments it can't stream-copy; treat
			// a missing dump file as an absent font rather than a failure.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("subtitle fonts: stat attachment %q: %w", fallbackName, err)
		}
		total += info.Size()
		if total > maxBytes {
			return nil, fmt.Errorf("subtitle fonts: attached font data exceeds %d bytes", maxBytes)
		}
		data, err := os.ReadFile(paths[i])
		if err != nil {
			return nil, fmt.Errorf("subtitle fonts: read attachment %q: %w", fallbackName, err)
		}
		fonts = append(fonts, SubtitleFontAttachment{
			Name: safeAttachmentDisplayName(stream, fallbackName),
			Data: data,
		})
	}

	return fonts, nil
}

// watchDumpSize polls the dump directory while ffmpeg runs and kills the
// process if the total bytes written exceed maxBytes, restoring the hard size
// bound the previous streaming reader enforced. It returns a flag set when the
// cap is tripped and a stop function the caller must invoke after cmd.Wait.
// The worst-case overshoot is one poll interval of ffmpeg writes, which is far
// smaller than the unbounded spill it replaces.
func watchDumpSize(cmd *exec.Cmd, dir string, maxBytes int64) (*atomic.Bool, func()) {
	overLimit := &atomic.Bool{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if dirBytes(dir) > maxBytes {
					overLimit.Store(true)
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
					return
				}
			}
		}
	}()
	return overLimit, func() {
		close(stop)
		<-done
	}
}

// dirBytes returns the total size of the regular files directly inside dir.
// Errors (e.g. a file removed mid-scan) are treated as zero so the watchdog
// never blocks extraction on a transient stat failure.
func dirBytes(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total
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
