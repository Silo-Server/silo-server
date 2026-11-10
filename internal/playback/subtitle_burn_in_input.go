package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/fspath"
)

// Text subtitle burn-in input.
//
// FFmpeg's libass filter (subtitles=) reads every packet of the file it is
// given while it initializes, before it passes on the first frame. Given the
// media file, that is a read of the whole file: 88 s for a 16 GB Matroska
// remux on network storage, far past ManifestStartupTimeout. The selected
// track is instead staged in the session directory as a file of that one
// track, read through the source's own index (embeddedsub), with the
// source's attached fonts beside it, which libass otherwise loads from the
// media file.

const (
	// stagedSubtitleDirPrefix names a start's staging directory in the
	// session directory. Each start stages into its own, so concurrent starts
	// of one session never replace files a running FFmpeg reads, and the
	// segment route, which serves only files directly in the session
	// directory, never serves them.
	stagedSubtitleDirPrefix = "subtitle-stage-"
	// stagedSubtitleTrackName and stagedSubtitleFontsDir are the staged
	// track and the directory of its fonts, in a staging directory.
	stagedSubtitleTrackName = "track.mks"
	stagedSubtitleFontsDir  = "fonts"
	// stageSubtitleTimeout bounds staging. It stays under the shortest
	// budget a remote start gives the node to answer (jellycompat's 20 s),
	// so a slow read falls back to the media file instead of failing.
	stageSubtitleTimeout = 15 * time.Second
	// stageSubtitleMaxBytes bounds the read of the staged track: its packets
	// plus embeddedsub's estimate of 1.5 KB of metadata a packet. Heavy ASS
	// typesetting runs to tens of thousands of packets (17,834 and 6.6 MB in
	// one release, 34 MB as counted), and a refused read here fails the
	// start, where keptsub's tighter bound only demuxes a sidecar instead.
	stageSubtitleMaxBytes = 64 << 20
	// stageSubtitleReadConcurrency is the reads a staging keeps in flight. A
	// whole track is a small read for each of its cues, so the time is
	// network-storage latency: a cold track of 10,000 cues took 27 s at
	// embeddedsub's default of 16 and 13 s at 64, while 128 gained little.
	stageSubtitleReadConcurrency = 64
	// stageSubtitleBusyRetry is how long staging waits before it asks again
	// for a packet read the process refused as busy.
	stageSubtitleBusyRetry = 250 * time.Millisecond
	// maxStagedSubtitleFonts and maxStagedSubtitleFontBytes bound the fonts
	// written for libass. Release groups attach dozens of fonts to one file,
	// some hundreds (258 fonts, 76 MiB, in one library); the client font
	// bundle's tighter limits are for JSON sent to players. The count bounds
	// FFmpeg's argument list.
	maxStagedSubtitleFonts     = 1024
	maxStagedSubtitleFontBytes = 256 << 20
)

// errStagedSubtitleFontsTooLarge refuses to stage a track whose source has
// more fonts than staging writes: libass reading the media file would load
// them all.
var errStagedSubtitleFontsTooLarge = errors.New("subtitle burn-in: source has more font attachments than can be staged")

// stagedSubtitleReads shares one read of a track among the starts that stage
// it at the same time: embeddedsub refuses a second read of the same track
// while one runs.
var stagedSubtitleReads singleflight.Group

// stagedBurnInSubtitle is a staged text track and the directory of its fonts.
type stagedBurnInSubtitle struct {
	trackPath string
	fontsDir  string
}

// stageBurnInSubtitle writes the burn-in track of opts and the source's
// attached fonts into a new staging directory in opts.OutputDir. An error
// means the track can't be staged and libass reads the media file as before;
// it is ctx's error when ctx ended.
func stageBurnInSubtitle(ctx context.Context, opts TranscodeOpts) (stagedBurnInSubtitle, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, stageSubtitleTimeout)
	defer cancel()

	idx, err := SubtitleIndexes().Index(ctx, opts.InputPath)
	if err != nil {
		return stagedBurnInSubtitle{}, err
	}
	// In a Matroska file the index lists the subtitle tracks FFmpeg makes
	// streams of, in track order (isSubtitleTrack), so a track's ordinal among
	// them is FFmpeg's s:N, which SubtitleTrackIndex names. An MP4 file's
	// index leaves out subtitle tracks FFmpeg lists, such as closed captions,
	// so its ordinals can name another track. The codec check refuses a file
	// where the two disagree anyway.
	if idx.Container != embeddedsub.ContainerMatroska {
		return stagedBurnInSubtitle{}, fmt.Errorf("subtitle burn-in: %s tracks are not staged", idx.Container)
	}
	if opts.SubtitleTrackIndex < 0 || opts.SubtitleTrackIndex >= len(idx.Tracks) {
		return stagedBurnInSubtitle{}, fmt.Errorf("subtitle burn-in: index has no subtitle track %d", opts.SubtitleTrackIndex)
	}
	track := &idx.Tracks[opts.SubtitleTrackIndex]
	if codec := track.FFmpegCodec(); opts.SubtitleCodec == "" || !strings.EqualFold(codec, opts.SubtitleCodec) {
		return stagedBurnInSubtitle{}, fmt.Errorf("subtitle burn-in: index track %d is %q, plan names %q", opts.SubtitleTrackIndex, codec, opts.SubtitleCodec)
	}
	if track.Unsupported != "" {
		return stagedBurnInSubtitle{}, fmt.Errorf("%w: %s", embeddedsub.ErrUnsupported, track.Unsupported)
	}

	dir, err := os.MkdirTemp(opts.OutputDir, stagedSubtitleDirPrefix)
	if err != nil {
		return stagedBurnInSubtitle{}, err
	}
	staged := stagedBurnInSubtitle{
		trackPath: filepath.Join(dir, stagedSubtitleTrackName),
		fontsDir:  filepath.Join(dir, stagedSubtitleFontsDir),
	}
	if err := stageBurnInSubtitleFiles(ctx, opts, idx, track, staged); err != nil {
		_ = os.RemoveAll(dir)
		return stagedBurnInSubtitle{}, err
	}
	slog.InfoContext(ctx, "burn-in subtitle staged", "component", "playback", "playback_session_id", opts.SessionID,
		"subtitle_track", opts.SubtitleTrackIndex, "duration_ms", time.Since(started).Milliseconds())
	return staged, nil
}

// stageBurnInSubtitleFiles writes the fonts, then the track, of a staging.
// Fonts go first: a source staging refuses is found from its header.
func stageBurnInSubtitleFiles(ctx context.Context, opts TranscodeOpts, idx *embeddedsub.Index, track *embeddedsub.Track, staged stagedBurnInSubtitle) error {
	if err := os.Mkdir(staged.fontsDir, 0o700); err != nil {
		return err
	}
	if err := writeAttachedSubtitleFonts(ctx, opts.InputPath, opts.FFmpegPath, staged.fontsDir); err != nil {
		return err
	}
	packets, err := readStagedSubtitlePackets(ctx, opts.InputPath, idx, track.Number)
	if err != nil {
		// A damaged track also comes back here: FFmpeg demuxing the source
		// may read more of it.
		return err
	}
	f, err := os.Create(staged.trackPath)
	if err != nil {
		return err
	}
	err = embeddedsub.WriteStream(f, idx, track, packets)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// readStagedSubtitlePackets reads a whole track through the index, shared
// with every other start staging it now. The read runs to its own deadline
// when the start that began it gives up, so the starts that joined it, or a
// retry of the same start, still get its packets.
func readStagedSubtitlePackets(ctx context.Context, path string, idx *embeddedsub.Index, number uint64) ([]embeddedsub.Packet, error) {
	key := fmt.Sprintf("%s\x00%d\x00%d\x00%d", path, idx.Size, idx.ModTime.UnixNano(), number)
	read := stagedSubtitleReads.DoChan(key, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stageSubtitleTimeout)
		defer cancel()
		for {
			res, err := embeddedsub.ReadPackets(ctx, path, idx, number, embeddedsub.WholeFrom, embeddedsub.WholeTo,
				embeddedsub.ReadOptions{MaxBytes: stageSubtitleMaxBytes, Concurrency: stageSubtitleReadConcurrency})
			if !errors.Is(err, embeddedsub.ErrReadBusy) {
				if err != nil {
					return nil, err
				}
				return res.Packets, nil
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(stageSubtitleBusyRetry):
			}
		}
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-read:
		if r.Err != nil {
			return nil, r.Err
		}
		packets, ok := r.Val.([]embeddedsub.Packet)
		if !ok {
			return nil, fmt.Errorf("subtitle burn-in: shared read returned %T", r.Val)
		}
		return packets, nil
	}
}

// writeAttachedSubtitleFonts writes the source's attached fonts into dir,
// named by their stream index, in one FFmpeg run that reads only the
// container's header. Each font goes straight to its file, at a path staging
// chooses rather than the attachment's own file name.
func writeAttachedSubtitleFonts(ctx context.Context, inputPath, ffmpegPath, dir string) error {
	ffmpegPath = ResolveFFmpegPath(ffmpegPath)
	streams, videoStream, err := probeFontAttachmentStreams(ctx, inputPath, ffprobePathFromFFmpeg(ffmpegPath))
	if err != nil {
		return err
	}
	if len(streams) == 0 {
		return nil
	}
	if len(streams) > maxStagedSubtitleFonts {
		return errStagedSubtitleFontsTooLarge
	}
	var total int64
	paths := make([]string, len(streams))
	args := []string{ffmpegHideBannerArg, ffmpegLogLevelArg, ffmpegErrorLogLevel, "-y"}
	for i, stream := range streams {
		if stream.ExtraDataSize <= 0 {
			return errors.New("subtitle burn-in: invalid font attachment size")
		}
		if total += stream.ExtraDataSize; total > maxStagedSubtitleFontBytes {
			return errStagedSubtitleFontsTooLarge
		}
		// libass finds a font by the names inside it, so the file name only
		// has to be unique.
		paths[i] = filepath.Join(dir, fmt.Sprintf("font-%d%s", stream.Index, fontAttachmentExt(stream)))
		args = append(args, fmt.Sprintf("-dump_attachment:%d", stream.Index), paths[i])
	}
	args = append(append(args, "-i", inputPath), attachmentDumpOutputArgs(videoStream)...)
	if out, err := fspath.CommandContext(ctx, ffmpegPath, args...).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("subtitle burn-in: write attached fonts: %w: %s", err, bytes.TrimSpace(out))
	}
	for i, stream := range streams {
		info, err := os.Stat(paths[i])
		if err != nil {
			return err
		}
		if info.Size() != stream.ExtraDataSize {
			return fmt.Errorf("subtitle burn-in: font attachment %d is %d bytes, probed %d", stream.Index, info.Size(), stream.ExtraDataSize)
		}
	}
	return nil
}
