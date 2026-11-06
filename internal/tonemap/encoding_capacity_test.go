package tonemap

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/encodinglimit"
)

func TestProbeCapacityExhaustionIsTransient(t *testing.T) {
	_, _, err := probeWithRunner(context.Background(), "ffmpeg", "none", "", func(context.Context, string, ...string) ([]byte, error) {
		return nil, encodinglimit.ErrAtCapacity
	})
	if !errors.Is(err, encodinglimit.ErrAtCapacity) {
		t.Fatalf("capacity was converted into unsupported hardware: %v", err)
	}
}
