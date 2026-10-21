//go:build unix

package mediasample

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/processmetrics"
	"github.com/Silo-Server/silo-server/internal/procgroup/procgrouptest"
)

// An attempt that passes its deadline ends the whole process tree it started.
// When the ffmpeg path is a wrapper script that runs the real ffmpeg as a
// child, killing only the wrapper would leave the child running outside the
// analysis and trickplay limits, one more for every timeout.
func TestRunTimeoutKillsTheWrappersChild(t *testing.T) {
	for _, background := range []bool{false, true} {
		name := "foreground"
		if background {
			name = "background"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			wrapper := procgrouptest.WriteWrapper(t, "ffmpeg")
			runner := Runner{FFmpegPath: wrapper.Path, Workload: processmetrics.Analysis}
			req := validRequest()
			req.Background = background
			req.Attempts = []Attempt{{TimeoutSeconds: 3}}

			done := make(chan error, 1)
			go func() {
				_, err := runner.Run(context.Background(), req)
				done <- err
			}()
			pid := wrapper.ChildPID(t)
			err := <-done
			var sampleErr *Error
			if !errors.As(err, &sampleErr) || sampleErr.Reason != ReasonTimeout {
				t.Fatalf("error %v, want a timeout", err)
			}
			procgrouptest.RequireExited(t, pid)
		})
	}
}
