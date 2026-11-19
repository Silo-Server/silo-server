package scanner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestProbePrimaryVideoTrackHonorsCallerTimeout(t *testing.T) {
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, ffprobe, "#!/bin/sh\nexec sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := ProbePrimaryVideoTrack(ctx, ffprobe, "movie.mkv")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ProbePrimaryVideoTrack() error = %v, want context deadline", err)
	}
}
