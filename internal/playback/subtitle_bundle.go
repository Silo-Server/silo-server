package playback

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// A subtitle bundle is what a transcode node without the library mounted
// needs to burn a text subtitle in. The libass subtitles filter opens its
// file itself, outside FFmpeg's input restrictions, so it must not open the
// node's relay URL; and loading a track demuxes the whole source, which only
// the core can do without moving the source over the network. So the core
// builds the bundle from its own copy of the source and keeps it in its
// subtitle cache (SubtitleCache.OpenSubtitleBundle), and the node fetches it,
// a few megabytes, before FFmpeg starts (relayed_subtitles.go).
//
// The bundle is a tar of the selected track as an ASS script (text formats
// convert to ASS, ASS and SSA are copied), extracted with -copyts so its
// events keep the container timestamps the transcode's frames carry, and the
// source's font attachments in a fonts directory. The filter reads the script
// (si=0) with that fontsdir, which is what it would have found reading the
// source itself. The fonts are the attachments isFontAttachment accepts, all
// of them up to bounds no real release reaches; libass loads every one too.

const (
	// subtitleBundleScript and subtitleBundleFontsDir name the script and the
	// fonts directory, in the bundle and in a node's unpacked copy.
	subtitleBundleScript   = "selected.ass"
	subtitleBundleFontsDir = "fonts"
	// maxBundleScriptBytes bounds the extracted script: typesetting and
	// karaoke heavy ASS runs to tens of MiB, and the bound only stops a
	// runaway extraction. maxBundleFontBytes and maxBundleFonts bound the
	// fonts, past which a pathological file's further attachments are left
	// out; real releases carry tens of fonts and tens of MiB.
	maxBundleScriptBytes = 256 << 20
	maxBundleFontBytes   = 256 << 20
	maxBundleFonts       = 1024
	// maxSubtitleBundleBytes bounds a bundle as a node reads it: the script
	// and fonts at their bounds, with a header and padding for each entry
	// and the end of the archive.
	maxSubtitleBundleBytes = maxBundleScriptBytes + maxBundleFontBytes + (maxBundleFonts+3)*1024

	// subtitleBundleFailureTTL is how long a failed build answers the
	// requests for its bundle with its failure, so a node polling for it does
	// not start the whole-source demux again at once.
	subtitleBundleFailureTTL = time.Minute
	// subtitleBundleBuildTimeout bounds one build, a demux of the whole
	// source from the core's storage, as a background warm is bounded.
	subtitleBundleBuildTimeout = subtitleCacheWarmTimeout

	// subtitleBundleCopyFormat and subtitleBundleASSFormat are the bundle's
	// cache formats: a copied ASS or SSA track, or one converted to ASS.
	subtitleBundleCopyFormat = "burnin-copy.tar"
	subtitleBundleASSFormat  = "burnin-ass.tar"
	subtitleBundleExt        = ".tar"

	ffmpegNoStdinArg = "-nostdin"
	ffmpegNoStatsArg = "-nostats"
	ffmpegMapArg     = "-map"
	ffmpegStdout     = "pipe:1"
	ttfFontExt       = ".ttf"
	utf8BOM          = "\xef\xbb\xbf"
	assScriptHeader  = "[Script Info]"
)

// ErrSubtitleBundlePending is a bundle the core is still building, or has
// not begun for want of a free build slot. The node asks again.
var ErrSubtitleBundlePending = errors.New("subtitle bundle is being built")

// ErrSubtitleBundleFailed is a bundle the source cannot give: FFmpeg fails on
// its bytes, the track is not one that converts to an ASS script, or its
// attached fonts are not what their metadata says. Building it again fails
// the same way. A build that fails otherwise (the cache's disk, a process
// that cannot start, the build's timeout) returns its error unwrapped.
var ErrSubtitleBundleFailed = errors.New("subtitle bundle cannot be built from the source")

// processStartError is a process that could not be started, which says
// nothing about the source it was to read.
type processStartError struct{ err error }

func (e processStartError) Error() string { return e.err.Error() }
func (e processStartError) Unwrap() error { return e.err }

// SubtitleBundleSource names the track a bundle carries.
type SubtitleBundleSource struct {
	// Path is the source on the core's storage.
	Path string
	// Track is the subtitle stream's ordinal among the source's subtitle
	// streams (0:s:Track), and Codec its codec.
	Track int
	Codec string
	// FFmpegPath is the FFmpeg binary; ffprobe is found next to it.
	FFmpegPath string
	// Matches, when set, is the revision the bundle must be built from: a
	// source it does not match fails with nodesource.ErrSourceChanged.
	Matches func(os.FileInfo) bool
}

// subtitleBundleFormat is the cache format of codec's bundle.
func subtitleBundleFormat(codec string) string {
	if copiesASSTrack(codec) {
		return subtitleBundleCopyFormat
	}
	return subtitleBundleASSFormat
}

// copiesASSTrack reports a track already in ASS, which the bundle copies.
func copiesASSTrack(codec string) bool {
	codec = strings.ToLower(codec)
	return codec == subtitleFormatASS || codec == subtitleCodecSSA
}

// subtitleBundleFailure is a failed build the cache remembers.
type subtitleBundleFailure struct {
	err error
	at  time.Time
}

// OpenSubtitleBundle opens src's bundle from the cache, under the key of the
// source's revision as it stats now, which must match src.Matches. When it is
// not cached it begins building it in the background, once one of the
// cache's warm slots is free, and waits for the build while ctx lasts:
// ErrSubtitleBundlePending when ctx ends first. A build that failed answers
// with its failure, wrapping ErrSubtitleBundleFailed when the source's bytes
// are at fault, for subtitleBundleFailureTTL. A build only commits under the
// revision it began on (SubtitleCacheFill.Commit), so the bundle opened is
// that revision's. The caller closes the file.
func (c *SubtitleCache) OpenSubtitleBundle(ctx context.Context, src SubtitleBundleSource) (*os.File, error) {
	dir := c.dir()
	if dir == "" {
		return nil, errors.New("subtitle bundle: the subtitle cache is disabled")
	}
	format := subtitleBundleFormat(src.Codec)
	for {
		info, err := os.Stat(src.Path)
		if err != nil {
			return nil, fmt.Errorf("subtitle bundle: %w: %w", nodesource.ErrSourceGone, err)
		}
		if src.Matches != nil && !src.Matches(info) {
			return nil, fmt.Errorf("subtitle bundle: %w", nodesource.ErrSourceChanged)
		}
		key := subtitleCacheFormatKey(src.Path, src.Track, info.ModTime(), info.Size(), format)
		if f, err := os.Open(filepath.Join(dir, key)); err == nil {
			// Recency for LRU eviction, as lookup records it.
			now := time.Now()
			_ = os.Chtimes(f.Name(), now, now)
			return f, nil
		}
		if err := c.bundleFailure(key); err != nil {
			return nil, err
		}
		inflight, err := c.startBundleBuild(src, format, key)
		if err != nil {
			return nil, err
		}
		// With no build of it in flight, every slot was busy: try again.
		var retry <-chan time.Time
		if inflight == nil {
			retry = time.After(time.Second)
		}
		select {
		case <-inflight:
		case <-retry:
		case <-ctx.Done():
			return nil, ErrSubtitleBundlePending
		}
	}
}

// bundleFailure is the remembered failure of the build under key, if any.
func (c *SubtitleCache) bundleFailure(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	failure, ok := c.bundleFailures[key]
	if !ok {
		return nil
	}
	if time.Since(failure.at) >= subtitleBundleFailureTTL {
		delete(c.bundleFailures, key)
		return nil
	}
	return failure.err
}

// startBundleBuild begins building src's bundle under key unless a build of
// it is in flight, and returns the channel that closes when that build ends;
// nil when every warm slot was busy and no build of it is in flight. It fails
// when the cache cannot take a build.
func (c *SubtitleCache) startBundleBuild(src SubtitleBundleSource, format, key string) (<-chan struct{}, error) {
	inflight := func() (chan struct{}, bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		done, ok := c.inflight[key]
		return done, ok
	}
	select {
	case c.warmSem <- struct{}{}:
	default:
		done, _ := inflight()
		return done, nil
	}
	fill := c.beginFill(src.Path, src.Track, format)
	if fill == nil {
		<-c.warmSem
		if done, ok := inflight(); ok {
			return done, nil
		}
		return nil, errors.New("subtitle bundle: the subtitle cache cannot take a build")
	}
	done, _ := inflight()
	go c.buildBundle(src, fill)
	return done, nil
}

// buildBundle writes src's bundle into fill and commits it, or remembers why
// it could not. It holds one of the cache's warm slots.
func (c *SubtitleCache) buildBundle(src SubtitleBundleSource, fill *SubtitleCacheFill) {
	defer func() { <-c.warmSem }()
	ctx, cancel := context.WithTimeout(context.Background(), subtitleBundleBuildTimeout)
	defer cancel()
	started := time.Now()
	err := writeSubtitleBundle(ctx, src, filepath.Dir(fill.tmp.Name()), fill.Tee(io.Discard))
	if err != nil {
		// Remembered whatever its cause, so a node polling for the bundle
		// does not begin the build again at once; only a fault of the
		// source's answers 422 (ErrSubtitleBundleFailed).
		// Remember before the fill releases its key, so no request in
		// between begins the build again.
		c.mu.Lock()
		c.bundleFailures[fill.key] = subtitleBundleFailure{err: err, at: time.Now()}
		c.mu.Unlock()
		fill.Discard()
		slog.Warn("subtitle bundle build failed", "component", "playback",
			"input", src.Path, "track", src.Track, "elapsed_ms", time.Since(started).Milliseconds(), "error", err)
		return
	}
	if err := fill.Commit(); err != nil {
		slog.Warn("subtitle bundle commit failed", "component", "playback",
			"input", src.Path, "track", src.Track, "error", err)
		return
	}
	slog.Info("subtitle bundle built", "component", "playback",
		"input", src.Path, "track", src.Track, "elapsed_ms", time.Since(started).Milliseconds())
}

// writeSubtitleBundle writes src's bundle to w: the script, extracted into a
// temporary file in scratchDir so its size heads its entry, then the fonts,
// streamed from FFmpeg. A failure of FFmpeg or ffprobe on the source, or of
// the checks of what they wrote, wraps ErrSubtitleBundleFailed.
func writeSubtitleBundle(ctx context.Context, src SubtitleBundleSource, scratchDir string, w io.Writer) error {
	sourceFault := func(err error) error {
		if err == nil || ctx.Err() != nil || errors.As(err, new(processStartError)) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrSubtitleBundleFailed, err)
	}
	script, err := os.CreateTemp(scratchDir, "bundle-script.part-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = script.Close()
		_ = os.Remove(script.Name())
	}()
	codec := subtitleFormatASS
	if copiesASSTrack(src.Codec) {
		codec = codecCopyV3
	}
	ffmpeg := ResolveFFmpegPath(src.FFmpegPath)
	err = boundedRun(ctx, ffmpeg, []string{
		ffmpegHideBannerArg, ffmpegNoStdinArg, ffmpegLogLevelArg, ffmpegErrorLogLevel,
		// No seek and no start_at_zero: the events keep the source's
		// timestamps, which the transcode's -copyts frames carry too.
		"-copyts", "-i", src.Path,
		ffmpegMapArg, "0:s:" + strconv.Itoa(src.Track), "-c:s", codec, "-f", subtitleFormatASS, ffmpegStdout,
	}, script, maxBundleScriptBytes)
	if err != nil {
		return sourceFault(fmt.Errorf("extract track: %w", err))
	}
	if err := checkASSScript(script.Name()); err != nil {
		return sourceFault(err)
	}
	info, err := script.Stat()
	if err != nil {
		return err
	}
	if _, err := script.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	if err := writeBundleEntry(tw, subtitleBundleScript, info.Size(), script); err != nil {
		return err
	}
	streams, err := probeFontAttachmentStreams(ctx, src.Path, ffprobePathFromFFmpeg(ffmpeg))
	if err != nil {
		return sourceFault(err)
	}
	err = streamFontAttachments(ctx, src.Path, ffmpeg, bundleFontStreams(streams), func(_ int, stream attachmentProbeStream, data io.Reader) error {
		name := path.Join(subtitleBundleFontsDir, "attachment-"+strconv.Itoa(stream.Index)+bundleFontExt(stream))
		return writeBundleEntry(tw, name, stream.ExtraDataSize, data)
	})
	if err != nil {
		return sourceFault(err)
	}
	return tw.Close()
}

// writeBundleEntry writes one regular file of size bytes read from r.
func writeBundleEntry(tw *tar.Writer, name string, size int64, r io.Reader) error {
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Size: size, Mode: 0o600}); err != nil {
		return err
	}
	if _, err := io.CopyN(tw, r, size); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// bundleFontStreams keeps the fonts a bundle carries: in container order,
// those with data, at most maxBundleFonts of them, skipping any that would
// take the total past maxBundleFontBytes.
func bundleFontStreams(streams []attachmentProbeStream) []attachmentProbeStream {
	var kept []attachmentProbeStream
	var total int64
	seen := make(map[int]bool)
	for _, stream := range streams {
		if len(kept) == maxBundleFonts {
			break
		}
		if stream.Index < 0 || seen[stream.Index] || stream.ExtraDataSize < 1 || stream.ExtraDataSize > maxBundleFontBytes-total {
			continue
		}
		seen[stream.Index] = true
		total += stream.ExtraDataSize
		kept = append(kept, stream)
	}
	return kept
}

// bundleFontExt is the extension a font is carried under: the attachment's,
// when supported, else ".ttf", which libass loads whatever the font's actual
// format. The attachment's own filename, which the source chooses, never
// becomes a path.
func bundleFontExt(stream attachmentProbeStream) string {
	if ext := fontAttachmentExt(stream); isSupportedFontExt(ext) {
		return ext
	}
	return ttfFontExt
}

// unpackSubtitleBundle writes the bundle read from r into dir, which exists:
// the script and the fonts directory. It accepts only the entries a bundle
// holds, within the bundle's bounds, and a script that starts as an ASS
// script does.
func unpackSubtitleBundle(r io.Reader, dir string) error {
	fontDir := filepath.Join(dir, subtitleBundleFontsDir)
	if err := os.Mkdir(fontDir, 0o700); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	var fonts int
	var fontBytes int64
	script := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read subtitle bundle: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Size < 0 {
			return fmt.Errorf("subtitle bundle entry %q is not a regular file", hdr.Name)
		}
		var target string
		switch entryDir, name := path.Split(hdr.Name); {
		case hdr.Name == subtitleBundleScript && !script && hdr.Size <= maxBundleScriptBytes:
			script = true
			target = filepath.Join(dir, subtitleBundleScript)
		case entryDir == subtitleBundleFontsDir+"/" && validBundleFontName(name) &&
			fonts < maxBundleFonts && hdr.Size <= maxBundleFontBytes-fontBytes:
			fonts++
			fontBytes += hdr.Size
			target = filepath.Join(fontDir, name)
		default:
			return fmt.Errorf("unexpected subtitle bundle entry %q", hdr.Name)
		}
		if err := writeBundleFile(target, tr, hdr.Size); err != nil {
			return err
		}
	}
	if !script {
		return errors.New("the subtitle bundle has no script")
	}
	return checkASSScript(filepath.Join(dir, subtitleBundleScript))
}

// validBundleFontName reports a font file name a bundle may carry: a plain
// name with a supported font extension.
func validBundleFontName(name string) bool {
	return name != "" && !strings.HasPrefix(name, ".") && filepath.Base(name) == name &&
		!strings.ContainsAny(name, `/\`) && isSupportedFontExt(strings.ToLower(filepath.Ext(name)))
}

// writeBundleFile creates target, which must not exist, with size bytes of r.
func writeBundleFile(target string, r io.Reader, size int64) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.CopyN(f, r, size)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// checkASSScript requires the script at path to start as an ASS script does,
// with its [Script Info] section, after an optional byte order mark and blank
// space.
func checkASSScript(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 64)
	n, _ := io.ReadFull(f, head)
	text := strings.TrimLeft(strings.TrimPrefix(string(head[:n]), utf8BOM), " \t\r\n")
	if !strings.HasPrefix(text, assScriptHeader) {
		return errors.New("the extracted track is not an ASS script")
	}
	return nil
}

// boundedRun runs bin, copying its stdout to w, and fails when the process
// fails or writes more than maxBytes.
func boundedRun(ctx context.Context, bin string, args []string, w io.Writer, maxBytes int64) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 5 * time.Second
	stderr := newBoundedTailBuffer(stderrTailMaxBytes)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return processStartError{err}
	}
	n, copyErr := io.Copy(w, io.LimitReader(stdout, maxBytes+1))
	if copyErr != nil || n > maxBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if copyErr != nil {
			return fmt.Errorf("write output: %w", copyErr)
		}
		return fmt.Errorf("output exceeds %d bytes", maxBytes)
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if tail := truncateStderr(stderr.String()); tail != "" {
			return fmt.Errorf("%w (stderr: %s)", err, tail)
		}
		return err
	}
	return nil
}
