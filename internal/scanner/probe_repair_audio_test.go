package scanner

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A successfully-probed audio-only file (audiobook/music) legitimately has no
// video codec/resolution/tracks. Requiring them made Ensure re-run ffprobe on
// every playback decision, since applyProbeData never populates video fields
// for an audio-only file.
func TestNeedsCriticalProbeRepair_ProbedAudioOnlyFileIsComplete(t *testing.T) {
	now := time.Now()
	f := &models.MediaFile{
		ProbeSource:    "local",
		ProbeUpdatedAt: &now,
		Duration:       3600,
		Container:      "mp3",
		CodecAudio:     "mp3",
		AudioTracks:    []models.AudioTrack{{Language: "eng"}},
		Chapters:       []models.MediaChapter{},
		// audio-only: CodecVideo, Resolution, VideoTracks intentionally empty
	}
	if NeedsCriticalProbeRepair(f) {
		t.Fatal("a successfully-probed audio-only file must not need probe repair")
	}
}

func TestNeedsCriticalProbeRepair_ProbedVideoMissingResolutionStillRepairs(t *testing.T) {
	now := time.Now()
	f := &models.MediaFile{
		ProbeSource:    "local",
		ProbeUpdatedAt: &now,
		Duration:       7200,
		Container:      "mkv",
		CodecAudio:     "aac",
		AudioTracks:    []models.AudioTrack{{Language: "eng"}},
		VideoTracks:    []models.VideoTrack{{}},
		CodecVideo:     "h264",
		Resolution:     "", // a real video file missing resolution must still repair
		Chapters:       []models.MediaChapter{},
	}
	if !NeedsCriticalProbeRepair(f) {
		t.Fatal("a video file missing resolution should still need probe repair")
	}
}

func TestNeedsCriticalProbeRepair_UnprobedFileRepairs(t *testing.T) {
	if !NeedsCriticalProbeRepair(&models.MediaFile{}) {
		t.Fatal("an unprobed file must need probe repair")
	}
}
