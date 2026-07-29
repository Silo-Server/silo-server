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

func TestNeedsCriticalProbeRepair_ProbedVideoMissingColorRangeRepairsOnce(t *testing.T) {
	now := time.Now()
	f := &models.MediaFile{
		ProbeSource:    "local",
		ProbeUpdatedAt: &now,
		Duration:       7200,
		Container:      "mkv",
		CodecAudio:     "aac",
		AudioTracks:    []models.AudioTrack{{Language: "eng"}},
		CodecVideo:     "h264",
		Resolution:     "1080p",
		VideoTracks:    []models.VideoTrack{{Codec: "h264"}},
		Chapters:       []models.MediaChapter{},
	}

	if !NeedsCriticalProbeRepair(f) {
		t.Fatal("a legacy video without color range should need probe repair")
	}

	f.VideoTracks[0].ColorRange = "unknown"
	if NeedsCriticalProbeRepair(f) {
		t.Fatal("a reprobed video with unknown color range should not repair again")
	}
}

func TestNeedsCriticalProbeRepair_UnprobedFileRepairs(t *testing.T) {
	if !NeedsCriticalProbeRepair(&models.MediaFile{}) {
		t.Fatal("an unprobed file must need probe repair")
	}
}

func implausiblyShortLargeVideoFile(probedAt time.Time) *models.MediaFile {
	return &models.MediaFile{
		ProbeSource:    "local",
		ProbeUpdatedAt: &probedAt,
		FileSize:       1_200_000_000,
		Duration:       4,
		Container:      "mkv",
		CodecAudio:     "aac",
		AudioTracks:    []models.AudioTrack{{Language: "eng"}},
		CodecVideo:     "h264",
		Resolution:     "720p",
		VideoTracks:    []models.VideoTrack{{Codec: "h264", ColorRange: "unknown"}},
		Chapters:       []models.MediaChapter{},
	}
}

func TestNeedsCriticalProbeRepair_ImplausiblyShortLargeVideoRepairs(t *testing.T) {
	f := implausiblyShortLargeVideoFile(legacyProbeDurationFixTime.Add(-time.Hour))

	if !NeedsCriticalProbeRepair(f) {
		t.Fatal("a large video claiming to be four seconds should need probe repair")
	}
}

// A short duration re-derived by the fixed parser (packet scan) is
// authoritative: re-flagging it would reprobe genuinely short clips on every
// playback decision forever.
func TestNeedsCriticalProbeRepair_ShortLargeVideoReprobedAfterFixConverges(t *testing.T) {
	f := implausiblyShortLargeVideoFile(legacyProbeDurationFixTime.Add(time.Hour))

	if NeedsCriticalProbeRepair(f) {
		t.Fatal("a short large video already reprobed by the fixed parser must not repair again")
	}
}

func TestNeedsCriticalProbeRepairScanState_LegacyShortDurationRepairs(t *testing.T) {
	probedAt := legacyProbeDurationFixTime.Add(-time.Hour)
	f := &scanStateFile{
		ProbeSource:    "local",
		ProbeUpdatedAt: &probedAt,
		FileSize:       1_200_000_000,
		Duration:       4,
		Container:      "mkv",
		CodecVideo:     "h264",
		CodecAudio:     "aac",
		Resolution:     "720p",
		HasVideoTracks: true,
		HasAudioTracks: true,
		HasChapters:    true,
	}

	if !needsCriticalProbeRepairScanState(f) {
		t.Fatal("library scans must flag legacy-collapsed durations for reprobe")
	}

	reprobedAt := legacyProbeDurationFixTime.Add(time.Hour)
	f.ProbeUpdatedAt = &reprobedAt
	if needsCriticalProbeRepairScanState(f) {
		t.Fatal("a short large video already reprobed by the fixed parser must not repair again")
	}
}
