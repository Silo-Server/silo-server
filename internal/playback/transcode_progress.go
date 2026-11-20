package playback

import (
	"bufio"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// attachProgressPipe connects a spawning FFmpeg process's -progress stream to
// the session for a plan that reports frames, and returns nil for a copy/remux
// plan that asks for no progress. Callers must call it before cmd.Start and
// must consume the returned pipe after the process has been reaped; a failed
// Start closes it, and teardown cancels the process context, which closes it
// too, so nothing ever waits on a blocked read.
func attachProgressPipe(cmd *exec.Cmd, opts TranscodeOpts) (io.ReadCloser, error) {
	if !reportsVideoFrames(opts) {
		return nil, nil
	}
	return cmd.StdoutPipe()
}

// consumeProgress reads FFmpeg's key=value -progress stream and records the
// frame count and output time on the session. It runs detached: Close cancels
// the process context, which closes the pipe, so a read error at the end of a
// generation is the normal exit and never an error to report. The reader is
// fenced by the generation it was spawned for, so a line still buffered from a
// replaced process cannot lend its frames to the replacement.
func (s *TranscodeSession) consumeProgress(pipe io.ReadCloser, generation uint64) {
	if pipe == nil {
		return
	}
	defer func() { _ = pipe.Close() }()
	reader := bufio.NewReader(pipe)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 && !s.observeProgressLine(line, generation) {
			return
		}
		if err != nil {
			return
		}
	}
}

// observeProgressLine folds one -progress line into the session and reports
// whether the reader's generation is still the live one.
func (s *TranscodeSession) observeProgressLine(line string, generation uint64) bool {
	key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
	if !ok {
		return true
	}
	switch strings.TrimSpace(key) {
	case "frame":
		frames, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || frames < 0 {
			return true
		}
		s.mu.Lock()
		if generation != s.progressGeneration {
			s.mu.Unlock()
			return false
		}
		if frames > s.videoFrames {
			s.videoFrames = frames
			s.lastVideoFrameAt = time.Now()
		}
		s.mu.Unlock()
	case "out_time_us", "out_time_ms":
		// Both keys carry microseconds; out_time_ms is a historical misnomer.
		// Before the first packet FFmpeg reports N/A, which simply does not
		// parse and leaves the previous reading in place.
		micros, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || micros < 0 {
			return true
		}
		s.mu.Lock()
		if generation != s.progressGeneration {
			s.mu.Unlock()
			return false
		}
		s.videoOutTimeUS = micros
		s.mu.Unlock()
	}
	return true
}

// VideoFrameProgress reports the current generation's decoded video frame
// count and the newest FFmpeg out_time_us reading. Both are zero for a plan
// that reports no frames (copy/remux) and for a generation that has decoded
// nothing.
func (s *TranscodeSession) VideoFrameProgress() (frames, outTimeUS int64) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.videoFrames, s.videoOutTimeUS
}
