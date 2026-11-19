package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestToneMapPathHashFailureIsTransient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short-source.mkv")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := validateToneMapSource(context.Background(), TranscodeOpts{
		InputPath: path, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1, FileSize: 5, FileHash: "0123456789abcdef"},
	})
	if !errors.Is(err, ErrToneMapSourceValidationUnavailable) || errors.Is(err, tonemap.ErrSourceRevisionChanged) {
		t.Fatalf("validateToneMapSource() error = %v, want transient validation unavailable", err)
	}
}

func TestResolveToneMapExecutorClassifiesCanceledProbeAsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolveToneMapExecutor(ctx, TranscodeOpts{
		ToneMapPolicy:        tonemap.PolicySoftwareOnly,
		ToneMapMode:          tonemap.ModeSoftware,
		ToneMapSourceKind:    tonemap.SourcePQ,
		ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{
			MediaFileID: 1,
			FileSize:    5,
		},
	})
	if !errors.Is(err, ErrToneMapExecutorUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveToneMapExecutor() error = %v, want executor unavailable + canceled", err)
	}
}
