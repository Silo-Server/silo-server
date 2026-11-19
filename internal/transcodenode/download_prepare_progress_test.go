package transcodenode

import (
	"context"
	"sync"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type recordingPrepareLogSink struct {
	mu       sync.Mutex
	sessions []string
	lines    []string
}

func (s *recordingPrepareLogSink) WriteLine(_ context.Context, sessionID string, _ playback.FFmpegLogAttrs, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, sessionID)
	s.lines = append(s.lines, line)
}

func (s *recordingPrepareLogSink) WriteEvent(_ context.Context, sessionID string, _ playback.FFmpegLogAttrs, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, sessionID)
}
