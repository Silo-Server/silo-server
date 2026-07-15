package scanner

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNeedsCriticalProbeRepairSTRMUsesDeferredPlaybackMetadata(t *testing.T) {
	now := time.Now()
	file := &models.MediaFile{
		FilePath:       "/media/movie.strm",
		ProbeSource:    "strm",
		ProbeUpdatedAt: &now,
		Duration:       7200,
		Container:      "strm",
		CodecVideo:     "h264",
		CodecAudio:     "aac",
		Resolution:     "1080p",
		VideoTracks:    []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}},
		AudioTracks:    []models.AudioTrack{{Codec: "aac", Channels: 2}},
		Chapters:       []models.MediaChapter{},
	}
	if NeedsCriticalProbeRepair(file) {
		t.Fatal(".strm placeholder metadata must not block catalog or playback on ffprobe")
	}
}
