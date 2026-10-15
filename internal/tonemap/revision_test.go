package tonemap

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/nodesource/nodesourcetest"
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
	if err := revision.ValidatePath(t.Context(), path); err != nil {
		t.Fatalf("ValidatePath(original) = %v", err)
	}
	if err := os.WriteFile(path, []byte("replaced"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := revision.ValidatePath(t.Context(), path); err == nil {
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

	if err := revision.ValidatePath(t.Context(), path); err == nil {
		t.Fatal("ValidatePath accepted replacement bytes with preserved size and modification time")
	}
}

func TestRevisionForFileNormalizesProbeTimestamp(t *testing.T) {
	probed := time.Date(2026, time.August, 13, 12, 34, 56, 123456789, time.FixedZone("test", -7*60*60))
	revision := RevisionForFile(&models.MediaFile{ID: 1, ProbeUpdatedAt: &probed})
	want := time.Date(2026, time.August, 13, 19, 34, 56, 123456000, time.UTC).UnixNano()
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
	if err := revision.ValidatePath(t.Context(), path); err == nil {
		t.Fatal("ValidatePath accepted a non-positive media file id")
	}
}

func TestValidatePathClassifiesMissingSourceAsChanged(t *testing.T) {
	revision := SourceRevision{MediaFileID: 1, FileSize: 1}
	err := revision.ValidatePath(t.Context(), filepath.Join(t.TempDir(), "missing.mkv"))
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

// On a transcode node with core source access the source is a loopback URL:
// ValidatePath describes it through its handle, hashes it with two ranged
// reads, and compares its modification time to the second the relay knows.
func TestValidatePathThroughTheRelay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.mkv")
	data := make([]byte, 3*openSubtitlesHashBlockSize+17)
	for i := range data {
		data[i] = byte(i * 13)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 3, 1, 12, 0, 0, 987654000, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	hash, err := computeOpenSubtitlesHash(t.Context(), path, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	h := nodesourcetest.Relayed(t, path)
	revision := SourceRevision{MediaFileID: 42, FileSize: int64(len(data)), FileModifiedUnixNano: stamp.UnixNano(), FileHash: hash}
	if relayedHash, err := computeOpenSubtitlesHash(t.Context(), h.Input(), int64(len(data))); err != nil || relayedHash != hash {
		t.Fatalf("relayed hash = %q, %v; want the local %q", relayedHash, err, hash)
	}
	if err := revision.ValidatePath(t.Context(), h.Input()); err != nil {
		t.Fatalf("ValidatePath(relayed) = %v", err)
	}

	changed := revision
	changed.FileHash = "0000000000000000"
	if err := changed.ValidatePath(t.Context(), h.Input()); !errors.Is(err, ErrSourceRevisionChanged) {
		t.Fatalf("another hash = %v, want ErrSourceRevisionChanged", err)
	}
	resized := revision
	resized.FileSize++
	if err := resized.ValidatePath(t.Context(), h.Input()); !errors.Is(err, ErrSourceRevisionChanged) {
		t.Fatalf("another size = %v, want ErrSourceRevisionChanged", err)
	}
	// Within the second the relay compares; a local file is compared to the
	// microsecond.
	sameSecond := revision
	sameSecond.FileModifiedUnixNano = stamp.Truncate(time.Second).UnixNano()
	if err := sameSecond.ValidatePath(t.Context(), h.Input()); err != nil {
		t.Fatalf("relayed time within the second = %v", err)
	}
	if err := sameSecond.ValidatePath(t.Context(), path); !errors.Is(err, ErrSourceRevisionChanged) {
		t.Fatalf("local time off by a fraction of a second = %v, want ErrSourceRevisionChanged", err)
	}
	retimed := revision
	retimed.FileModifiedUnixNano = stamp.Add(time.Second).UnixNano()
	if err := retimed.ValidatePath(t.Context(), h.Input()); !errors.Is(err, ErrSourceRevisionChanged) {
		t.Fatalf("relayed time a second later = %v, want ErrSourceRevisionChanged", err)
	}

	// A source the job released can't be checked, which says nothing about
	// the file.
	url := h.Input()
	_ = h.Close()
	if err := revision.ValidatePath(t.Context(), url); err == nil || errors.Is(err, ErrSourceRevisionChanged) {
		t.Fatalf("released source = %v, want an unavailable check", err)
	}
}

// The core's lasting answers that a relayed source is gone or changed are a
// changed revision; a refused grant or a transient failure is not.
func TestSourceWithdrawn(t *testing.T) {
	for status, want := range map[int]bool{
		http.StatusGone: true, http.StatusPreconditionFailed: true, http.StatusNotFound: true,
		http.StatusForbidden: false, http.StatusBadGateway: false,
	} {
		err := &fs.PathError{Op: "read", Path: "source", Err: &nodesource.StatusError{Op: "read", Status: status}}
		if got := sourceWithdrawn(err); got != want {
			t.Errorf("sourceWithdrawn(%d) = %t, want %t", status, got, want)
		}
	}
	if sourceWithdrawn(errors.New("connection reset")) {
		t.Error("a transport error reads as a withdrawn source")
	}
}
