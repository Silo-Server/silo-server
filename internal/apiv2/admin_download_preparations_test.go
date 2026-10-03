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

func TestAdminDownloadPreparationOfMapsRunningJob(t *testing.T) {
	list, _ := new(fakeAdminDownloadPreparations).List(context.Background(), 10)
	got := adminDownloadPreparationOf(list.Items[0])
	if got.LogSessionID != "download-prepare-art-running" {
		t.Fatalf("log session = %q", got.LogSessionID)
	}
	if got.Worker == nil || got.Worker.Kind != "node" || got.Worker.NodeID == nil || *got.Worker.NodeID != "9" || got.Worker.Name != "gpu-01" {
		t.Fatalf("worker = %+v", got.Worker)
	}
	if got.Progress == nil || got.Progress.EncodedSeconds != 4686 || got.Progress.Speed != 3.4 {
		t.Fatalf("progress = %+v", got.Progress)
	}
	if got.QueuePosition != nil || got.FailedAt != nil {
		t.Fatalf("running job carries queue position %v or failed_at %v", got.QueuePosition, got.FailedAt)
	}
	if len(got.Source.AudioTracks) != 2 || got.Source.AudioTracks[1].Channels == nil || *got.Source.AudioTracks[1].Channels != 2 {
		t.Fatalf("audio tracks = %+v", got.Source.AudioTracks)
	}
	if got.Output.BitrateKbps == nil || *got.Output.BitrateKbps != 10_000 || !got.Output.AllAudioTracks {
		t.Fatalf("output = %+v", got.Output)
	}
}

func TestAdminDownloadPreparationOfMapsQueuedAndFailedJobs(t *testing.T) {
	list, _ := new(fakeAdminDownloadPreparations).List(context.Background(), 10)
	queued := adminDownloadPreparationOf(list.Items[1])
	if queued.QueuePosition == nil || *queued.QueuePosition != 1 || queued.Worker != nil || queued.Output.BitrateKbps != nil {
		t.Fatalf("queued = %+v", queued)
	}
	if queued.Requesters[0].DeviceID != "" || queued.Source.AudioTracks == nil {
		t.Fatalf("queued requesters/source = %+v / %+v", queued.Requesters, queued.Source)
	}
	failed := adminDownloadPreparationOf(list.Items[2])
	if failed.FailedAt == nil || failed.Error != "ffmpeg: No space left on device" {
		t.Fatalf("failed = %+v", failed)
	}
	if failed.Worker == nil || failed.Worker.Kind != "server" || failed.Worker.NodeID != nil {
		t.Fatalf("failed worker = %+v", failed.Worker)
	}
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
