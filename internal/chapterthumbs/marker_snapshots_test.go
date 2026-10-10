package chapterthumbs

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMarkerSnapshotExpiryEditsAndRemovedKinds(t *testing.T) {
	now := time.Now()
	file := &models.MediaFile{ID: 1, Duration: 36, IntroStart: new(2.0), IntroEnd: new(4.0)}
	snapshot := *file
	snapshot.MarkerThumbnailBaseSegments = models.EffectiveMarkerSegments(file)
	snapshot.MarkerSegments = []models.MarkerSegment{{Kind: "credits", StartSeconds: 30, EndSeconds: 33}}
	file.MarkerThumbnails = models.EffectiveMarkerThumbnails(&models.MediaFile{ID: 1, Duration: 36, MarkerSegments: snapshot.MarkerSegments})
	service := &Service{clock: func() time.Time { return now }, markerSnapshots: map[int]markerThumbnailSnapshot{1: {file: snapshot, expires: now.Add(time.Minute)}}}
	effective := service.withMarkerSnapshot(file)
	if segments := models.EffectiveMarkerSegments(effective); len(segments) != 1 || segments[0].Kind != "credits" {
		t.Fatalf("removed canonical kind resurrected: %v", segments)
	}
	now = now.Add(time.Minute)
	if service.withMarkerSnapshot(file) != file {
		t.Fatal("expired snapshot used")
	}
	now = now.Add(-time.Minute)
	file.IntroStart = new(3.0)
	if service.withMarkerSnapshot(file) != file {
		t.Fatal("manual edit ignored")
	}
}
