package apiv2

import (
	"context"
	"net/http"
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
		Counts: downloads.PreparationCounts{Running: 1, Queued: 1, Paused: 1, FailedRecent: 1},
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
				ArtifactID: "art-paused", State: downloads.PreparationPaused, Format: "transcode",
				MediaFileID: 45, ContentID: "movie:heat-1995", MediaTitle: "Heat", MediaType: "movie",
				TargetContainer: "mp4", TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetResolution: "720p",
				TargetBitrateKbps: 2_000, MaxAttempts: 3, CreatedAt: at, PausedAt: &later,
				Requesters: []downloads.PreparationRequester{{UserID: 8, Username: "jordan", ProfileID: "p2", Status: "preparing"}},
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

// fakeAdminDownloadPreparationControls answers as if art-queued and
// art-paused were unfinished jobs and art-failed a failed one.
type fakeAdminDownloadPreparationControls struct{ lastIDs []string }

func (f *fakeAdminDownloadPreparationControls) results(ids []string, outcome func(id string) downloads.PreparationOutcome) []downloads.PreparationResult {
	f.lastIDs = ids
	seen := map[string]bool{}
	out := make([]downloads.PreparationResult, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, downloads.PreparationResult{ArtifactID: id, Outcome: outcome(id)})
	}
	return out
}

func (f *fakeAdminDownloadPreparationControls) PausePreparations(_ context.Context, ids []string) ([]downloads.PreparationResult, error) {
	return f.results(ids, func(id string) downloads.PreparationOutcome {
		switch id {
		case "art-queued":
			return downloads.PreparationApplied
		case "art-paused":
			return downloads.PreparationUnchanged
		case "art-failed":
			return downloads.PreparationNotApplicable
		}
		return downloads.PreparationNotFound
	}), nil
}

func (f *fakeAdminDownloadPreparationControls) ResumePreparations(_ context.Context, ids []string) ([]downloads.PreparationResult, error) {
	return f.results(ids, func(id string) downloads.PreparationOutcome {
		switch id {
		case "art-paused":
			return downloads.PreparationApplied
		case "art-queued":
			return downloads.PreparationUnchanged
		case "art-failed":
			return downloads.PreparationNotApplicable
		}
		return downloads.PreparationNotFound
	}), nil
}

func (f *fakeAdminDownloadPreparationControls) CancelPreparations(_ context.Context, ids []string) ([]downloads.PreparationResult, error) {
	return f.results(ids, func(id string) downloads.PreparationOutcome {
		switch id {
		case "art-queued", "art-paused", "art-failed":
			return downloads.PreparationApplied
		}
		return downloads.PreparationNotFound
	}), nil
}

// adminDownloadPreparationControlFixtureCases covers pausing, resuming and
// canceling preparation jobs.
func adminDownloadPreparationControlFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "pause_admin_download_preparations", operationID: "pauseAdminDownloadPreparations", method: http.MethodPost, path: Prefix + "/admin/downloads/preparations/pause", headers: bearer(adminToken), body: `{"ids":["art-queued","art-paused","art-failed","art-gone","art-queued"]}`, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminDownloadPreparationActionOutputBody", scenario: "Pausing reports one outcome per distinct job: a queued job pauses, a paused one is unchanged, a failed one cannot be paused, and an unknown one is not found."},
		{name: "resume_admin_download_preparations", operationID: "resumeAdminDownloadPreparations", method: http.MethodPost, path: Prefix + "/admin/downloads/preparations/resume", headers: bearer(adminToken), body: `{"ids":["art-paused","art-queued"]}`, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminDownloadPreparationActionOutputBody", scenario: "Resuming a paused job applies; a job that was never paused is unchanged."},
		{name: "cancel_admin_download_preparations", operationID: "cancelAdminDownloadPreparations", method: http.MethodPost, path: Prefix + "/admin/downloads/preparations/cancel", headers: bearer(adminToken), body: `{"ids":["art-queued","art-failed","art-ready"]}`, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminDownloadPreparationActionOutputBody", scenario: "Canceling removes unfinished and failed jobs; a job that already finished preparing is not found."},
		{name: "cancel_admin_download_preparations_invalid_id", operationID: "cancelAdminDownloadPreparations", method: http.MethodPost, path: Prefix + "/admin/downloads/preparations/cancel", headers: bearer(adminToken), body: `{"ids":["../etc"]}`, status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Problem", scenario: "A job id outside the artifact id alphabet is rejected before any job is touched."},
	}
}

func TestAdminDownloadPreparationOfRedactsErrorCredentials(t *testing.T) {
	list, _ := new(fakeAdminDownloadPreparations).List(context.Background(), 10)
	p := list.Items[3]
	p.ErrorMessage = "node https://node.example/prepare?token=SECRET: desc = api_key=SECRET"
	got := adminDownloadPreparationOf(p).Error
	if strings.Contains(got, "SECRET") || !strings.Contains(got, "node https://node.example/prepare?token=") {
		t.Fatalf("error = %q", got)
	}
}
