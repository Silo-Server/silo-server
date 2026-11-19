package scanner

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNeedsCriticalProbeRepair_LegacyAudiobookCoverArtRepairs(t *testing.T) {
	now := time.Now()
	f := &models.MediaFile{
		BaseType: "audiobook", ProbeSource: "local", ProbeUpdatedAt: &now,
		Duration: 3600, Container: "mp4", CodecAudio: "aac", CodecVideo: "mjpeg",
		AudioTracks: []models.AudioTrack{{Codec: "aac"}},
		VideoTracks: []models.VideoTrack{{Codec: "mjpeg", Width: 500, Height: 500, ColorRange: "unknown"}},
		Chapters:    []models.MediaChapter{},
	}
	if !NeedsCriticalProbeRepair(f) {
		t.Fatal("legacy attached-picture video metadata must trigger a repair probe")
	}
}
