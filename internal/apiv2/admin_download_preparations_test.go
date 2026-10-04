package apiv2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminDownloadPreparations struct{ lastLimit int }

func (*fakeAdminDownloadPreparations) Available() bool { return true }

func (f *fakeAdminDownloadPreparations) List(_ context.Context, limit int) (*downloads.PreparationList, error) {
	f.lastLimit = limit
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	later := at.Add(5 * time.Minute)
	node := 9
	season, episode := 2, 1
	return &downloads.PreparationList{
		Counts: downloads.PreparationCounts{Running: 1, Queued: 1, FailedRecent: 1},
		Items: []downloads.Preparation{
			{
				ArtifactID: "art-running", State: downloads.PreparationRunning, Format: "transcode",
				MediaFileID: 42, ContentID: "movie:dune-2", MediaTitle: "Dune: Part Two", MediaType: "movie",
				SourceContainer: "mkv", SourceVideoCodec: "hevc", SourceResolution: "2160p", SourceHDR: true,
				SourceAudioTracks: []models.AudioTrack{{Codec: "truehd", Channels: 8, Language: "eng"}, {Codec: "aac", Channels: 2}},
				SourceFileSize:    61_800_000_000, SourceDuration: 9972, SourceBitrateKbps: 58_000,
				TargetContainer: "mp4", TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetResolution: "1080p",
				TargetBitrateKbps: 10_000, ToneMapMode: "software", ToneMapSourceKind: "hdr10", AllAudioTracks: true,
				WorkerKind: downloads.WorkerNode, WorkerNodeID: &node, WorkerName: "gpu-01",
				Attempts: 1, MaxAttempts: 3, CreatedAt: at, StartedAt: &at,
				Progress: &downloads.ArtifactProgress{EncodedSeconds: 4686, DurationSeconds: 9972, Speed: 3.4, UpdatedAt: later},
				Requesters: []downloads.PreparationRequester{
					{UserID: 7, Username: "alex", ProfileID: "p1", ProfileName: "Alex", DeviceID: "d1", DeviceName: "iPhone", DevicePlatform: "ios", Status: "preparing"},
				},
			},
			{
				ArtifactID: "art-queued", State: downloads.PreparationQueued, Format: "remux", QueuePosition: 1,
				MediaFileID: 43, ContentID: "series:severance", EpisodeID: "episode:severance-s02e01",
				MediaTitle: "Severance", MediaType: "series", SeriesName: "Severance", EpisodeName: "Hello, Ms. Cobel",
				SeasonNumber: &season, EpisodeNumber: &episode,
				TargetContainer: "mp4", TargetVideoCodec: "copy", TargetAudioCodec: "copy",
				MaxAttempts: 3, CreatedAt: at,
				Requesters: []downloads.PreparationRequester{{UserID: 7, Username: "alex", ProfileID: "p1", Status: "preparing"}},
			},
			{
				ArtifactID: "art-failed", State: downloads.PreparationFailed, Format: "transcode",
				MediaFileID: 44, MediaTitle: "Oppenheimer", MediaType: "movie",
				TargetContainer: "mp4", TargetVideoCodec: "h264", TargetAudioCodec: "aac",
				WorkerKind: downloads.WorkerServer, WorkerName: "api-1",
				Attempts: 3, MaxAttempts: 3, ErrorMessage: "ffmpeg: No space left on device ",
				CreatedAt: at, StartedAt: &at, CompletedAt: &later,
				Requesters: []downloads.PreparationRequester{{UserID: 8, Username: "jordan", ProfileID: "p2", Status: "failed"}},
			},
		},
	}, nil
}

func TestAdminDownloadPreparationOfRedactsErrorCredentials(t *testing.T) {
	list, _ := new(fakeAdminDownloadPreparations).List(context.Background(), 10)
	p := list.Items[2]
	p.ErrorMessage = "node https://node.example/prepare?token=SECRET: desc = api_key=SECRET"
	got := adminDownloadPreparationOf(p).Error
	if strings.Contains(got, "SECRET") || !strings.Contains(got, "node https://node.example/prepare?token=") {
		t.Fatalf("error = %q", got)
	}
}
