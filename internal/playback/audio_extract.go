package playback

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AudioChunk is one extracted piece of an audio track. Start is the chunk's
// exact start within the extracted audio (seconds) as reported by ffmpeg's
// segment muxer — the muxer cuts at packet boundaries near, not exactly at,
// the requested length, so assuming index*chunkSeconds would accumulate
// subtitle timing error.
type AudioChunk struct {
	Path  string
	Start float64
}

// ExtractAudioChunks extracts one audio track from a media file into
// fixed-length 16 kHz mono WAV chunks under dir — the input format Whisper
// endpoints want, sized to stay under typical upload limits (a 10-minute
// chunk is ~19 MB). One ffmpeg pass segments the whole track; the returned
// chunks are in chronological order with exact start offsets. The caller owns
// dir and its cleanup.
func ExtractAudioChunks(ctx context.Context, filePath string, audioTrackIndex int, dir, ffmpegPath string, chunkSeconds int) ([]AudioChunk, error) {
	if chunkSeconds <= 0 {
		chunkSeconds = 600
	}
	if audioTrackIndex < 0 {
		audioTrackIndex = 0
	}

	listPath := filepath.Join(dir, "segments.csv")
	args := audioChunkExtractionArgs(filePath, audioTrackIndex, listPath,
		filepath.Join(dir, "chunk%05d.wav"), 0, chunkSeconds)

	cmd := exec.CommandContext(ctx, audioExtractionFFmpegBinary(ffmpegPath), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg audio extraction failed: %w (stderr: %s)",
			err, truncateStderr(stderr.String()))
	}

	starts := parseSegmentList(listPath)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read audio chunk dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "chunk") && strings.HasSuffix(e.Name(), ".wav") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("ffmpeg produced no audio chunks for track %d", audioTrackIndex)
	}

	chunks := make([]AudioChunk, len(names))
	for i, name := range names {
		start, ok := starts[name]
		if !ok {
			// Segment list missing/unparsable: fall back to the nominal grid.
			start = float64(i * chunkSeconds)
		}
		chunks[i] = AudioChunk{Path: filepath.Join(dir, name), Start: start}
	}
	return chunks, nil
}

// ExtractAudioChunksFrom extracts one audio track starting at startSec and calls
// onSegment as each WAV segment is closed by ffmpeg. onSegment is synchronous:
// if it blocks on ASR/translation, ffmpeg may continue extracting later chunks
// into dir, so callers should remove processed chunks promptly. The caller owns
// dir and cleanup. Segment starts are absolute media positions.
func ExtractAudioChunksFrom(
	ctx context.Context,
	filePath string,
	audioTrackIndex int,
	dir, ffmpegPath string,
	startSec float64,
	chunkSeconds int,
	onSegment func(AudioChunk) error,
) error {
	if chunkSeconds <= 0 {
		chunkSeconds = 600
	}
	if audioTrackIndex < 0 {
		audioTrackIndex = 0
	}
	if startSec < 0 {
		startSec = 0
	}

	listPath := filepath.Join(dir, "segments.csv")
	args := audioChunkExtractionArgs(filePath, audioTrackIndex, listPath,
		filepath.Join(dir, "chunk%05d.wav"), startSec, chunkSeconds)

	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, audioExtractionFFmpegBinary(ffmpegPath), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg audio extraction: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	seen := map[string]bool{}
	tailer := &segmentListTailer{}
	emit := func(flush bool) error {
		return emitNewSegmentListEntries(listPath, dir, startSec, seen, tailer, flush, onSegment)
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case waitErr := <-waitCh:
			segmentErr := emit(true)
			if segmentErr != nil {
				return segmentErr
			}
			if waitErr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("ffmpeg audio extraction failed: %w (stderr: %s)",
					waitErr, truncateStderr(stderr.String()))
			}
			if len(seen) == 0 {
				return fmt.Errorf("ffmpeg produced no audio chunks for track %d", audioTrackIndex)
			}
			return nil
		case <-ticker.C:
			if err := emit(false); err != nil {
				cancel()
				<-waitCh
				return err
			}
		case <-ctx.Done():
			cancel()
			<-waitCh
			return ctx.Err()
		}
	}
}

func audioChunkExtractionArgs(
	filePath string,
	audioTrackIndex int,
	listPath string,
	outPattern string,
	startSec float64,
	chunkSeconds int,
) []string {
	args := []string{}
	if startSec > 0 {
		args = append(args, "-ss", strconv.FormatFloat(startSec, 'f', 3, 64))
	}
	args = append(args,
		"-i", filePath,
		"-vn", "-sn", "-dn",
		"-map", fmt.Sprintf("0:a:%d", audioTrackIndex),
		"-ac", "1",
		"-ar", "16000",
		"-c:a", "pcm_s16le",
		"-f", "segment",
		"-segment_time", strconv.Itoa(chunkSeconds),
		"-segment_list", listPath,
		"-segment_list_type", "csv",
		"-y", outPattern,
	)
	return args
}

func audioExtractionFFmpegBinary(ffmpegPath string) string {
	if ffmpegPath != "" {
		return ffmpegPath
	}
	return "ffmpeg"
}

// parseSegmentList reads ffmpeg's CSV segment list (filename,start,end per
// line) into a filename → start map. Best effort: a missing or malformed list
// yields an empty map and callers fall back to nominal chunk starts.
func parseSegmentList(listPath string) map[string]float64 {
	starts := map[string]float64{}
	f, err := os.Open(listPath)
	if err != nil {
		return starts
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return starts
	}
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		start, err := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		if err != nil || start < 0 {
			continue
		}
		starts[filepath.Base(strings.TrimSpace(row[0]))] = start
	}
	return starts
}

func emitNewSegmentListEntries(
	listPath, dir string,
	startOffset float64,
	seen map[string]bool,
	tailer *segmentListTailer,
	flush bool,
	onSegment func(AudioChunk) error,
) error {
	entries := tailer.read(listPath, flush)
	for _, entry := range entries {
		key := filepath.Base(entry.name)
		if seen[key] {
			continue
		}
		seen[key] = true
		path := entry.name
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if onSegment != nil {
			if err := onSegment(AudioChunk{Path: path, Start: startOffset + entry.start}); err != nil {
				return err
			}
		}
	}
	return nil
}

type segmentListEntry struct {
	name  string
	start float64
}

type segmentListTailer struct {
	offset  int64
	pending string
}

func (t *segmentListTailer) read(listPath string, flush bool) []segmentListEntry {
	f, err := os.Open(listPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err == nil && info.Size() < t.offset {
		t.offset = 0
		t.pending = ""
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	t.offset += int64(len(data))

	text := t.pending + string(data)
	if text == "" {
		return nil
	}
	lineEnd := strings.LastIndexByte(text, '\n')
	if lineEnd < 0 {
		if !flush {
			t.pending = text
			return nil
		}
		t.pending = ""
		return parseSegmentListEntries(strings.NewReader(text))
	}

	complete := text[:lineEnd+1]
	t.pending = text[lineEnd+1:]
	if flush && t.pending != "" {
		complete += t.pending
		t.pending = ""
	}
	return parseSegmentListEntries(strings.NewReader(complete))
}

func parseSegmentListEntries(r io.Reader) []segmentListEntry {
	reader := csv.NewReader(r)
	var out []segmentListEntry
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Keep completed rows; malformed rows are ignored best-effort.
			break
		}
		if len(row) < 2 {
			continue
		}
		start, err := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		if err != nil || start < 0 {
			continue
		}
		name := strings.TrimSpace(row[0])
		if name == "" {
			continue
		}
		out = append(out, segmentListEntry{name: name, start: start})
	}
	return out
}

// maxPlausibleAudioDelay caps the probed audio/container start delta; beyond
// this it is far more likely a probing artifact than a real stream delay.
const maxPlausibleAudioDelay = 30.0

// ProbeAudioStartOffset returns the audio stream's start time relative to the
// container start (seconds). Whisper timestamps are relative to the first
// audio sample, while the playback timeline starts at the container start —
// in containers with delayed audio (TS remuxes especially) the difference is
// a constant subtitle sync error unless corrected. Best effort: any probe
// failure returns 0.
func ProbeAudioStartOffset(ctx context.Context, filePath string, audioTrackIndex int, ffmpegPath string) float64 {
	if audioTrackIndex < 0 {
		audioTrackIndex = 0
	}
	ffprobe := "ffprobe"
	if ffmpegPath != "" {
		// ffprobe ships next to ffmpeg in every distribution Silo supports.
		ffprobe = filepath.Join(filepath.Dir(ffmpegPath), "ffprobe")
	}

	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error",
		"-print_format", "json",
		"-show_entries", "format=start_time",
		"-select_streams", fmt.Sprintf("a:%d", audioTrackIndex),
		"-show_entries", "stream=start_time",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}

	var probe struct {
		Format struct {
			StartTime string `json:"start_time"`
		} `json:"format"`
		Streams []struct {
			StartTime string `json:"start_time"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probe); err != nil || len(probe.Streams) == 0 {
		return 0
	}
	formatStart, err1 := strconv.ParseFloat(probe.Format.StartTime, 64)
	streamStart, err2 := strconv.ParseFloat(probe.Streams[0].StartTime, 64)
	if err1 != nil || err2 != nil {
		return 0
	}
	delta := streamStart - formatStart
	if math.IsNaN(delta) || math.Abs(delta) > maxPlausibleAudioDelay {
		return 0
	}
	return delta
}
