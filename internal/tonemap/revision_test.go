package tonemap

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestSourceRevisionRoundTripAndPathValidation verifies encoded revisions retain stable filesystem facts.
func TestSourceRevisionRoundTripAndPathValidation(t *testing.T) {
	path := t.TempDir() + "/source.mkv"
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := normalizeRevisionTime(info.ModTime())
	probed := time.Now().UTC()
	file := &models.MediaFile{
		ID: 42, FileSize: info.Size(), FileModifiedAt: &modified, FileHash: "hash", ProbeUpdatedAt: &probed,
		VideoTracks: []models.VideoTrack{{
			Codec: "hevc", Profile: "Main 10", BitDepth: 10, PixelFormat: "yuv420p10le",
			DVProfile: 7, DVBLCompatID: 6, DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true, DVRPUPresent: true,
			ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
		}},
	}
	revision := RevisionForFile(file)
	if !revision.Stable() {
		t.Fatalf("revision = %#v, want stable", revision)
	}
	decoded, err := DecodeSourceRevision(revision.Encode())
	if err != nil || decoded != revision {
		t.Fatalf("round trip = %#v, %v; want %#v", decoded, err, revision)
	}
	if err := revision.ValidatePath(path); err != nil {
		t.Fatalf("ValidatePath(original) = %v", err)
	}
	if err := os.WriteFile(path, []byte("replaced"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := revision.ValidatePath(path); err == nil {
		t.Fatal("ValidatePath accepted replacement bytes")
	}
}

func TestValidatePathRejectsReplacementWithPreservedSizeAndModTime(t *testing.T) {
	path := t.TempDir() + "/source.mkv"
	original := make([]byte, 128*1024)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	revision := SourceRevision{
		MediaFileID:          42,
		FileSize:             info.Size(),
		FileModifiedUnixNano: normalizeRevisionTime(info.ModTime()).UnixNano(),
		// The OpenSubtitles hash of an all-zero 128 KiB file is its size.
		FileHash: "0000000000020000",
	}
	replacement := make([]byte, len(original))
	replacement[0] = 1
	if err := os.WriteFile(path, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	if err := revision.ValidatePath(path); err == nil {
		t.Fatal("ValidatePath accepted replacement bytes with preserved size and modification time")
	}
}

// TestRevisionForFileChangesWithDolbyVisionPresenceFacts verifies metadata presence affects source identity.
func TestRevisionForFileChangesWithDolbyVisionPresenceFacts(t *testing.T) {
	modified := time.Now().UTC().Truncate(time.Microsecond)
	file := &models.MediaFile{ID: 1, FileSize: 100, FileModifiedAt: &modified, VideoTracks: []models.VideoTrack{{
		Codec: "hevc", DVProfile: 7, DVBLCompatID: 6, DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true,
	}}}
	before := RevisionForFile(file)
	file.VideoTracks[0].DVRPUPresent = true
	after := RevisionForFile(file)
	if before.StreamSignature == after.StreamSignature || before.Fingerprint() == after.Fingerprint() {
		t.Fatal("Dolby Vision presence change did not invalidate the source revision")
	}
}

func TestRevisionForFileNormalizesProbeTimestamp(t *testing.T) {
	probed := time.Date(2026, time.August, 13, 12, 34, 56, 123456789, time.FixedZone("test", -7*60*60))
	revision := RevisionForFile(&models.MediaFile{ID: 1, ProbeUpdatedAt: &probed})
	want := normalizeRevisionTime(probed).UnixNano()
	if revision.ProbeUpdatedUnixNano != want {
		t.Fatalf("ProbeUpdatedUnixNano = %d, want %d", revision.ProbeUpdatedUnixNano, want)
	}
}

func TestValidatePathRejectsNonPositiveMediaFileID(t *testing.T) {
	path := t.TempDir() + "/source.mkv"
	if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	revision := SourceRevision{MediaFileID: -1, FileSize: info.Size()}
	if err := revision.ValidatePath(path); err == nil {
		t.Fatal("ValidatePath accepted a non-positive media file id")
	}
}

func TestValidatePathClassifiesMissingSourceAsChanged(t *testing.T) {
	revision := SourceRevision{MediaFileID: 1, FileSize: 1}
	err := revision.ValidatePath(filepath.Join(t.TempDir(), "missing.mkv"))
	if !errors.Is(err, ErrSourceRevisionChanged) {
		t.Fatalf("ValidatePath(missing) = %v, want ErrSourceRevisionChanged", err)
	}
}

func TestValidateLivePrimaryVideoTrackRequiresExactFrozenSignature(t *testing.T) {
	track := models.VideoTrack{
		Codec: "hevc", Profile: "Main 10", Level: 153, Width: 3840, Height: 2160,
		FrameRate: "23.976", DVProfile: 7, DVLevel: 6, DVBLCompatID: 6,
		DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true, DVRPUPresent: true,
		ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
		BitDepth: 10, PixelFormat: "yuv420p10le",
	}
	frozen := RevisionForFile(&models.MediaFile{ID: 42, VideoTracks: []models.VideoTrack{track}})
	if err := ValidateLivePrimaryVideoTrack(frozen, track); err != nil {
		t.Fatalf("matching live track rejected: %v", err)
	}

	mutations := []struct {
		name   string
		mutate func(*models.VideoTrack)
	}{
		{name: "codec profile", mutate: func(track *models.VideoTrack) { track.Profile = "Main" }},
		{name: "bit depth", mutate: func(track *models.VideoTrack) { track.BitDepth = 8 }},
		{name: "PQ to HLG", mutate: func(track *models.VideoTrack) { track.ColorTransfer = "arib-std-b67" }},
		{name: "color primaries", mutate: func(track *models.VideoTrack) { track.ColorPrimaries = "bt709" }},
		{name: "color space", mutate: func(track *models.VideoTrack) { track.ColorSpace = "bt709" }},
		{name: "Dolby Vision profile", mutate: func(track *models.VideoTrack) { track.DVProfile = 8 }},
		{name: "Dolby Vision provenance", mutate: func(track *models.VideoTrack) { track.DVRPUPresent = false }},
		{name: "Dolby Vision enhancement layer presence", mutate: func(track *models.VideoTrack) { track.DVELPresent = true }},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			live := track
			tt.mutate(&live)
			if err := ValidateLivePrimaryVideoTrack(frozen, live); !errors.Is(err, ErrSourceRevisionChanged) {
				t.Fatalf("ValidateLivePrimaryVideoTrack() error = %v, want ErrSourceRevisionChanged", err)
			}
		})
	}
}
