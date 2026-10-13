package playback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerinput"
)

const (
	workerSubtitleMapArg        = "-map"
	workerSubtitleCodecArg      = "-c:s"
	workerSubtitleMaxBytes      = 8 << 20
	workerSubtitleMaxProbeBytes = 1 << 20
	workerSubtitleTimeout       = 30 * time.Second
	workerSubtitleNoStdinArg    = "-nostdin"
	workerSubtitleOutputPipeArg = "pipe:1"
)

func prepareSubtitleFilterInputContext(ctx context.Context, opts *TranscodeOpts) error {
	if !workerinput.IsStandalone(ctx) {
		return prepareSubtitleFilterInput(opts)
	}
	if !opts.SubtitleBurnIn || opts.SubtitleTrackIndex < 0 || NeedsBurnIn(opts.SubtitleCodec) {
		return nil
	}
	if err := workerinput.ValidateOutput(ctx, opts.OutputDir); err != nil {
		return err
	}
	handle, ok := workerinput.Lookup(ctx, opts.InputPath)
	if !ok {
		return errors.New("subtitle source is outside admitted worker authority")
	}
	ctx, cancel := context.WithTimeout(ctx, workerSubtitleTimeout)
	defer cancel()
	key := sha256.Sum256([]byte(handle.Identity() + ":" + strconv.Itoa(opts.SubtitleTrackIndex) + ":" + opts.SubtitleCodec))
	dir := filepath.Join(opts.OutputDir, "subtitle-assets-"+hex.EncodeToString(key[:16]))
	if err := workerinput.ValidateOutput(ctx, dir); err != nil {
		return err
	}
	if readyWorkerSubtitleAssets(dir) {
		setWorkerSubtitleAssets(opts, dir)
		return nil
	}
	staging, err := os.MkdirTemp(opts.OutputDir, ".subtitle-assets-")
	if err != nil {
		return errors.New("create worker subtitle scratch")
	}
	defer func() { _ = os.RemoveAll(staging) }()
	codec := subtitleFormatASS
	if strings.EqualFold(opts.SubtitleCodec, subtitleFormatASS) || strings.EqualFold(opts.SubtitleCodec, "ssa") {
		codec = codecCopyV3
	}
	// No seek or start_at_zero: libass and Silo's copyts video graph consume
	// original container timestamps, including nonzero starts on restarts.
	args := []string{ffmpegHideBannerArg, workerSubtitleNoStdinArg, ffmpegLogLevelArg, ffmpegErrorLogLevel, "-copyts", "-i", opts.InputPath, workerSubtitleMapArg, "0:s:" + strconv.Itoa(opts.SubtitleTrackIndex), workerSubtitleCodecArg, codec, "-f", subtitleFormatASS, workerSubtitleOutputPipeArg}
	script, err := workerSubtitleOutput(ctx, opts.FFmpegPath, args, workerSubtitleMaxBytes)
	if err != nil {
		return fmt.Errorf("stage selected subtitle: %w", err)
	}
	if !strings.Contains(string(script), "[Events]") {
		return errors.New("staged subtitle is not an ASS script")
	}
	if err := os.WriteFile(filepath.Join(staging, "selected.ass"), script, 0o600); err != nil {
		return errors.New("write staged subtitle")
	}
	fonts, err := workerSubtitleFonts(ctx, opts.InputPath, opts.FFmpegPath)
	if err != nil {
		return err
	}
	fontDir := filepath.Join(staging, "fonts")
	if err := os.Mkdir(fontDir, 0o700); err != nil {
		return errors.New("create subtitle font scratch")
	}
	for _, font := range fonts {
		if err := os.WriteFile(filepath.Join(fontDir, font.Name), font.Data, 0o600); err != nil {
			return errors.New("write staged subtitle font")
		}
	}
	// The directory is session-owned; a non-directory/symlink collision is
	// rejected rather than following an attachment or cached filename.
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe subtitle scratch collision")
		}
		if err := os.RemoveAll(dir); err != nil {
			return errors.New("replace subtitle scratch")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect subtitle scratch")
	}
	if err := os.Rename(staging, dir); err != nil {
		return errors.New("publish subtitle scratch")
	}
	setWorkerSubtitleAssets(opts, dir)
	return nil
}

func readyWorkerSubtitleAssets(dir string) bool {
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return false
	}
	info, err := os.Lstat(filepath.Join(dir, "selected.ass"))
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > workerSubtitleMaxBytes {
		return false
	}
	fontDirInfo, err := os.Lstat(filepath.Join(dir, "fonts"))
	if err != nil || !fontDirInfo.IsDir() || fontDirInfo.Mode()&os.ModeSymlink != 0 {
		return false
	}
	fonts, err := os.ReadDir(filepath.Join(dir, "fonts"))
	if err != nil || len(fonts) > maxSubtitleFontAttachments {
		return false
	}
	var total int64
	for _, font := range fonts {
		info, err := font.Info()
		if err != nil || !info.Mode().IsRegular() || !isSupportedFontExt(filepath.Ext(font.Name())) || info.Size() > maxSubtitleFontBytes-total {
			return false
		}
		total += info.Size()
	}
	return true
}

func setWorkerSubtitleAssets(opts *TranscodeOpts, dir string) {
	opts.subtitleFilterInputPath = filepath.Join(dir, "selected.ass")
	opts.subtitleFilterFontsDir = filepath.Join(dir, "fonts")
	opts.subtitleFilterStaged = true
}

// workerSubtitleOutput bounds stdout, stderr, lifetime and pipe cleanup of
// each staging process. Source protocols are applied by the shared guard.
func workerSubtitleOutput(ctx context.Context, bin string, args []string, maxBytes int64) ([]byte, error) {
	cmd := workerinput.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 5 * time.Second
	cmd.Stderr = newBoundedTailBuffer(stderrTailMaxBytes)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("create subtitle process output")
	}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("start subtitle process")
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	if readErr != nil || int64(len(data)) > maxBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.New("subtitle process output exceeds limit or could not be read")
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("subtitle process failed")
	}
	return data, nil
}

func workerSubtitleFonts(ctx context.Context, input, ffmpeg string) ([]SubtitleFontAttachment, error) {
	data, err := workerSubtitleOutput(ctx, ffprobePathFromFFmpeg(ffmpeg), []string{"-v", ffmpegErrorLogLevel, "-select_streams", "t", "-show_entries", "stream=index,codec_name,codec_type,extradata_size:stream_tags=filename,mimetype", "-of", "json", input}, workerSubtitleMaxProbeBytes)
	if err != nil {
		return nil, fmt.Errorf("probe subtitle fonts: %w", err)
	}
	var metadata attachmentProbeOutput
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, errors.New("invalid subtitle font metadata")
	}
	var streams []attachmentProbeStream
	var expected int64
	for _, stream := range metadata.Streams {
		if !isFontAttachment(stream) {
			continue
		}
		if stream.Index < 0 || stream.ExtraDataSize < 1 || stream.ExtraDataSize > maxSubtitleFontBytes-expected || !isSupportedFontExt(fontAttachmentExt(stream)) {
			return nil, errors.New("subtitle font attachment exceeds typed asset limits")
		}
		for _, prior := range streams {
			if prior.Index == stream.Index {
				return nil, errors.New("duplicate subtitle font attachment index")
			}
		}
		expected += stream.ExtraDataSize
		streams = append(streams, stream)
		if len(streams) > maxSubtitleFontAttachments {
			return nil, errors.New("too many subtitle font attachments")
		}
	}
	if len(streams) == 0 {
		return nil, nil
	}
	args := []string{ffmpegHideBannerArg, workerSubtitleNoStdinArg, ffmpegLogLevelArg, ffmpegErrorLogLevel}
	for _, stream := range streams {
		args = append(args, "-dump_attachment:"+strconv.Itoa(stream.Index), workerSubtitleOutputPipeArg)
	}
	// Attachments live in the container header. Do not traverse the media
	// duration after dumping the probed extradata.
	args = append(args, "-i", input, workerSubtitleMapArg, "0:t?", "-c", codecCopyV3, "-t", "0", "-f", "null", "-")
	data, err = workerSubtitleOutput(ctx, ffmpeg, args, expected)
	if err != nil {
		return nil, fmt.Errorf("extract subtitle fonts: %w", err)
	}
	if int64(len(data)) != expected {
		return nil, errors.New("subtitle font bytes differ from probed size")
	}
	var fonts []SubtitleFontAttachment
	offset := 0
	for _, stream := range streams {
		end := offset + int(stream.ExtraDataSize)
		font := data[offset:end:end]
		if len(font) < 4 {
			return nil, errors.New("invalid subtitle font representation")
		}
		switch string(font[:4]) {
		case "\x00\x01\x00\x00", "OTTO", "ttcf", "wOFF", "wOF2":
		default:
			return nil, errors.New("invalid subtitle font representation")
		}
		fonts = append(fonts, SubtitleFontAttachment{StreamIndex: stream.Index, Name: "attachment-" + strconv.Itoa(stream.Index) + fontAttachmentExt(stream), Data: font})
		offset = end
	}
	return fonts, nil
}

func workerSubtitleFilter(opts TranscodeOpts, filenameKeyword bool) string {
	path, index := opts.InputPath, opts.SubtitleTrackIndex
	if opts.subtitleFilterInputPath != "" {
		path = opts.subtitleFilterInputPath
	}
	if opts.subtitleFilterStaged {
		index = 0
	}
	name := "subtitles="
	if filenameKeyword {
		name += "filename="
	}
	filter := fmt.Sprintf("%s'%s':si=%d", name, escapeFilterPath(path), index)
	if opts.subtitleFilterStaged && opts.subtitleFilterFontsDir != "" {
		filter += ":fontsdir='" + escapeFilterPath(opts.subtitleFilterFontsDir) + "'"
	}
	return filter
}
