package catalog

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// recordingProbeEnsurer records which half of the ensurer contract each
// prepare path asks for.
type recordingProbeEnsurer struct {
	probeCalls  []int
	cachedCalls []int
}

func (e *recordingProbeEnsurer) EnsureProbeOnly(_ context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	e.probeCalls = append(e.probeCalls, file.ID)
	return file, nil
}

func (e *recordingProbeEnsurer) EnsureCopySafetyCached(_ context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	e.cachedCalls = append(e.cachedCalls, file.ID)
	return file, nil
}

type recordingCopySafetyRacer struct {
	raced []int
}

func (r *recordingCopySafetyRacer) RaceScan(fileID int) {
	r.raced = append(r.raced, fileID)
}

func h264File(id int, multiplePPS *bool) *models.MediaFile {
	return &models.MediaFile{
		ID:         id,
		CodecVideo: "h264",
		VideoTracks: []models.VideoTrack{{
			Codec:       "h264",
			MultiplePPS: multiplePPS,
		}},
	}
}

// Browse detail must never trigger the H.264 copy-safety scan: the verdict is
// not serialized into those responses, so the scan is pure warm-up and its
// read is what made first-time browsing slow on remote storage.
func TestPrepareBrowseFilesSkipsCopySafety(t *testing.T) {
	ensurer := &recordingProbeEnsurer{}
	racer := &recordingCopySafetyRacer{}
	svc := &DetailService{probeEnsurer: ensurer, copySafetyRacer: racer}
	files := []*models.MediaFile{h264File(1, nil), h264File(2, nil)}

	prepared := svc.prepareBrowseFiles(context.Background(), files)

	if len(prepared) != 2 {
		t.Fatalf("prepareBrowseFiles() returned %d files, want 2", len(prepared))
	}
	if len(ensurer.cachedCalls) != 0 {
		t.Fatalf("browse path resolved copy safety for %v, want probe repair only", ensurer.cachedCalls)
	}
	if len(ensurer.probeCalls) != 2 {
		t.Fatalf("browse path called EnsureProbeOnly %d times, want 2 — probe repair must still run", len(ensurer.probeCalls))
	}
	if len(racer.raced) != 0 {
		t.Fatalf("browse path raced scans for %v, want none", racer.raced)
	}
}

// The watch surfaces prepare a play, but must not block on the bitstream scan:
// they take the cached-only ensure and start the scan in the background.
func TestPreparePlaybackFilesUsesCachedEnsureAndRacesScan(t *testing.T) {
	ensurer := &recordingProbeEnsurer{}
	racer := &recordingCopySafetyRacer{}
	svc := &DetailService{probeEnsurer: ensurer, copySafetyRacer: racer}
	files := []*models.MediaFile{h264File(1, nil), h264File(2, nil)}

	prepared := svc.preparePlaybackFiles(context.Background(), files)

	if len(prepared) != 2 {
		t.Fatalf("preparePlaybackFiles() returned %d files, want 2", len(prepared))
	}
	if len(ensurer.cachedCalls) != 2 {
		t.Fatalf("watch path called EnsureCopySafetyCached %d times, want 2", len(ensurer.cachedCalls))
	}
	if len(racer.raced) != 2 || racer.raced[0] != 1 || racer.raced[1] != 2 {
		t.Fatalf("watch path raced %v, want scans for files 1 and 2", racer.raced)
	}
}

// A file whose verdict is already known, or that is not H.264, has nothing to
// resolve: no background scan may be started for it.
func TestPreparePlaybackFilesSkipsRaceWhenNothingToScan(t *testing.T) {
	known := false
	ensurer := &recordingProbeEnsurer{}
	racer := &recordingCopySafetyRacer{}
	svc := &DetailService{probeEnsurer: ensurer, copySafetyRacer: racer}
	files := []*models.MediaFile{
		h264File(1, &known),
		{ID: 2, CodecVideo: "hevc", VideoTracks: []models.VideoTrack{{Codec: "hevc"}}},
		{ID: 3},
	}

	svc.preparePlaybackFiles(context.Background(), files)

	if len(racer.raced) != 0 {
		t.Fatalf("watch path raced %v, want no scans for known or non-H.264 files", racer.raced)
	}
}

func TestPrepareFilesWithoutEnsurerPassesFilesThrough(t *testing.T) {
	svc := &DetailService{}
	files := []*models.MediaFile{{ID: 1}, nil, {ID: 2}}

	if got := len(svc.prepareBrowseFiles(context.Background(), files)); got != 2 {
		t.Fatalf("prepareBrowseFiles() returned %d files, want 2 (nil entries dropped)", got)
	}
	if got := len(svc.preparePlaybackFiles(context.Background(), files)); got != 2 {
		t.Fatalf("preparePlaybackFiles() returned %d files, want 2 (nil entries dropped)", got)
	}
}
