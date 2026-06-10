package playback

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ExtractAudioChunks extracts one audio track from a media file into
// fixed-length 16 kHz mono WAV chunks under dir — the input format Whisper
// endpoints want, sized to stay under typical upload limits (a 10-minute
// chunk is ~19 MB). One ffmpeg pass segments the whole track; the returned
// paths are in chronological order. The caller owns dir and its cleanup.
func ExtractAudioChunks(ctx context.Context, filePath string, audioTrackIndex int, dir, ffmpegPath string, chunkSeconds int) ([]string, error) {
	if chunkSeconds <= 0 {
		chunkSeconds = 600
	}
	if audioTrackIndex < 0 {
		audioTrackIndex = 0
	}

	args := []string{
		"-i", filePath,
		"-vn", "-sn", "-dn",
		"-map", fmt.Sprintf("0:a:%d", audioTrackIndex),
		"-ac", "1",
		"-ar", "16000",
		"-c:a", "pcm_s16le",
		"-f", "segment",
		"-segment_time", strconv.Itoa(chunkSeconds),
		"-y", filepath.Join(dir, "chunk%05d.wav"),
	}

	ffmpeg := "ffmpeg"
	if ffmpegPath != "" {
		ffmpeg = ffmpegPath
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg audio extraction failed: %w (stderr: %s)",
			err, truncateStderr(stderr.String()))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read audio chunk dir: %w", err)
	}
	var chunks []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "chunk") && strings.HasSuffix(e.Name(), ".wav") {
			chunks = append(chunks, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(chunks)
	if len(chunks) == 0 {
		return nil, fmt.Errorf("ffmpeg produced no audio chunks for track %d", audioTrackIndex)
	}
	return chunks, nil
}
