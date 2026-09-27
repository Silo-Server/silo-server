package playback

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// TextSubtitleTrack names one cached text rendition of an embedded subtitle
// stream.
type TextSubtitleTrack struct {
	// Ordinal is the stream's position among the source's subtitle streams,
	// ffmpeg's 0:s:N.
	Ordinal int
	// Format is the output format: "srt" or "ass".
	Format string
}

// TextSubtitleOutput directs one rendition of a batch extract to a file.
type TextSubtitleOutput struct {
	TextSubtitleTrack
	Path string
}

// TextSubtitleBatchFunc demuxes a source once and writes every output.
// Production callers pass ExtractTextSubtitlesToFiles; tests substitute fakes.
type TextSubtitleBatchFunc func(ctx context.Context, inputPath string, outputs []TextSubtitleOutput) error

const (
	subtitleCodecMovText = "mov_text"
	defaultFFmpegBinary  = "ffmpeg"
)

// batchTextSubtitleCodecs lists the embedded codecs a batch extract converts.
// Anything else is extracted only when requested, so one unusual stream cannot
// fail the demux for every other track.
var batchTextSubtitleCodecs = map[string]bool{
	subtitleCodecSubRip:  true,
	subtitleFormatSRT:    true,
	subtitleFormatASS:    true,
	subtitleCodecSSA:     true,
	subtitleMuxerWebVTT:  true,
	subtitleCodecMovText: true,
	"text":               true,
}

// TextSubtitleTracks lists the renditions a batch extract produces for a
// file's embedded subtitle streams, given each stream's codec in stream
// order: SRT for every text stream, plus ASS for ASS/SSA streams, whose
// styling SRT drops.
func TextSubtitleTracks(codecs []string) []TextSubtitleTrack {
	tracks := make([]TextSubtitleTrack, 0, len(codecs))
	for ordinal, codec := range codecs {
		codec = strings.ToLower(strings.TrimSpace(codec))
		if !batchTextSubtitleCodecs[codec] {
			continue
		}
		tracks = append(tracks, TextSubtitleTrack{Ordinal: ordinal, Format: subtitleFormatSRT})
		if IsASS(codec) {
			tracks = append(tracks, TextSubtitleTrack{Ordinal: ordinal, Format: subtitleFormatASS})
		}
	}
	return tracks
}

// ExtractTextSubtitlesToFiles runs one ffmpeg demux that writes every output.
func ExtractTextSubtitlesToFiles(ctx context.Context, inputPath string, outputs []TextSubtitleOutput, ffmpegPath string) error {
	if len(outputs) == 0 {
		return nil
	}
	args := []string{ffmpegHideBannerArg, ffmpegLogLevelArg, ffmpegErrorLogLevel, "-y", "-i", inputPath}
	for _, output := range outputs {
		args = append(args, "-map", fmt.Sprintf("0:s:%d", output.Ordinal), "-f", output.Format, "file:"+output.Path)
	}
	if ffmpegPath == "" {
		ffmpegPath = defaultFFmpegBinary
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg subtitle batch extraction failed: %w (stderr: %s)", err, truncateStderr(stderr.String()))
	}
	return nil
}

// ExtractTextTracks returns want's rendition from the cache or a fresh
// extract. A miss demuxes the source once for want and for every other
// uncached rendition in tracks, as Jellyfin does: each full read of a large
// remux on network storage can take minutes, and a viewer switching tracks
// would otherwise pay that again for every track. A request whose rendition
// is already being filled waits for that fill instead of starting a second
// demux. When the batch cannot produce want, want is extracted on its own
// with single.
func (c *SubtitleCache) ExtractTextTracks(
	ctx context.Context,
	inputPath string,
	want TextSubtitleTrack,
	tracks []TextSubtitleTrack,
	batch TextSubtitleBatchFunc,
	single func(context.Context) ([]byte, error),
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	want.Format = normalizeCachedTextSubtitleFormat(want.Format)
	if want.Format == "" {
		return nil, fmt.Errorf("unsupported cached subtitle format")
	}
	if data, found, err := c.lookupTextRendition(inputPath, want); found {
		return data, err
	}
	if c.waitForTextFill(ctx, inputPath, want) {
		if data, found, err := c.lookupTextRendition(inputPath, want); found {
			return data, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	fills := c.beginTextFills(inputPath, append([]TextSubtitleTrack{want}, tracks...))
	if len(fills) == 0 || fills[0].track != want || batch == nil {
		// The cache is disabled, or another request reserved want after the
		// checks above.
		discardTextFills(fills)
		if c.waitForTextFill(ctx, inputPath, want) {
			if data, found, err := c.lookupTextRendition(inputPath, want); found {
				return data, err
			}
		}
		return c.ExtractText(ctx, inputPath, want.Ordinal, want.Format, single)
	}
	if err := commitTextFills(ctx, inputPath, fills, batch); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		slog.WarnContext(ctx, "subtitle batch extraction failed; extracting the requested track alone",
			"input", inputPath, "track", want.Ordinal, "tracks", len(fills), "error", err)
		return c.ExtractText(ctx, inputPath, want.Ordinal, want.Format, single)
	}
	if data, found, err := c.lookupTextRendition(inputPath, want); found {
		return data, err
	}
	// The batch ran but want could not be published (for example, the source
	// changed during the demux).
	return c.ExtractText(ctx, inputPath, want.Ordinal, want.Format, single)
}

// lookupTextRendition reads a cached rendition. A stream without cues is
// cached as an empty file so it is not demuxed again, and reads as the same
// error a direct extract of it returns.
func (c *SubtitleCache) lookupTextRendition(inputPath string, track TextSubtitleTrack) ([]byte, bool, error) {
	data, ok := c.LookupText(inputPath, track.Ordinal, track.Format)
	if !ok {
		return nil, false, nil
	}
	if len(data) == 0 {
		return nil, true, fmt.Errorf("ffmpeg produced empty subtitle output for track %d", track.Ordinal)
	}
	return data, true, nil
}

// WarmTextTracks starts a detached batch extract of every uncached rendition
// in tracks, so a later track selection is served from the cache. Like
// WarmInBackground it takes a warm slot without blocking and is dropped when
// every slot is busy. A nil receiver is a no-op.
func (c *SubtitleCache) WarmTextTracks(inputPath string, tracks []TextSubtitleTrack, batch TextSubtitleBatchFunc) {
	if c == nil || batch == nil || len(tracks) == 0 {
		return
	}
	select {
	case c.warmSem <- struct{}{}:
	default:
		slog.Debug("subtitle text warm skipped: all warm slots busy", "input", inputPath)
		return
	}
	fills := c.beginTextFills(inputPath, tracks)
	if len(fills) == 0 {
		<-c.warmSem
		return
	}
	go func() {
		defer func() { <-c.warmSem }()
		ctx, cancel := context.WithTimeout(context.Background(), subtitleCacheWarmTimeout)
		defer cancel()
		start := time.Now()
		if err := commitTextFills(ctx, inputPath, fills, batch); err != nil {
			slog.Warn("subtitle text warm failed", "input", inputPath, "tracks", len(fills),
				"elapsed_ms", time.Since(start).Milliseconds(), "error", err)
			return
		}
		slog.Info("subtitle text warm finished", "input", inputPath, "tracks", len(fills),
			"elapsed_ms", time.Since(start).Milliseconds())
	}()
}

type textFill struct {
	track TextSubtitleTrack
	fill  *SubtitleCacheFill
}

// beginTextFills reserves a fill for every rendition in tracks that is not
// cached or already being filled, keeping the order of tracks.
func (c *SubtitleCache) beginTextFills(inputPath string, tracks []TextSubtitleTrack) []textFill {
	fills := make([]textFill, 0, len(tracks))
	seen := make(map[TextSubtitleTrack]bool, len(tracks))
	for _, track := range tracks {
		track.Format = normalizeCachedTextSubtitleFormat(track.Format)
		if track.Format == "" || seen[track] {
			continue
		}
		seen[track] = true
		if _, _, ok := c.cachedFormatEntryPath(inputPath, track.Ordinal, track.Format); ok {
			continue
		}
		if fill := c.beginFill(inputPath, track.Ordinal, track.Format); fill != nil {
			fills = append(fills, textFill{track: track, fill: fill})
		}
	}
	return fills
}

// commitTextFills runs the batch into the fills' temp files and publishes
// every rendition. A stream with no cues publishes an empty file, which
// records that the demux found nothing, so later warms do not read the whole
// source for it again.
func commitTextFills(ctx context.Context, inputPath string, fills []textFill, batch TextSubtitleBatchFunc) error {
	outputs := make([]TextSubtitleOutput, len(fills))
	for i, f := range fills {
		outputs[i] = TextSubtitleOutput{TextSubtitleTrack: f.track, Path: f.fill.tmp.Name()}
	}
	if err := batch(ctx, inputPath, outputs); err != nil {
		discardTextFills(fills)
		return err
	}
	for _, f := range fills {
		if err := f.fill.Commit(); err != nil {
			slog.WarnContext(ctx, "subtitle cache commit failed",
				"input", inputPath, "track", f.track.Ordinal, "format", f.track.Format, "error", err)
		}
	}
	return nil
}

func discardTextFills(fills []textFill) {
	for _, f := range fills {
		f.fill.Discard()
	}
}

// waitForTextFill waits for an in-flight fill of track to finish. It reports
// false when no fill was in flight or ctx ended first.
func (c *SubtitleCache) waitForTextFill(ctx context.Context, inputPath string, track TextSubtitleTrack) bool {
	if c.dir() == "" {
		return false
	}
	src, err := os.Stat(inputPath)
	if err != nil {
		return false
	}
	key := subtitleCacheFormatKey(inputPath, track.Ordinal, src.ModTime(), src.Size(), track.Format)
	c.mu.Lock()
	done := c.inflight[key]
	c.mu.Unlock()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
