package handlers

import (
	"context"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

type subtitleCreateRepo struct {
	ai.JobRepository
	existing *ai.Job
	inserted *ai.Job
	lookups  int
}

func (r *subtitleCreateRepo) GetActiveJobByIdempotencyKey(context.Context, string) (*ai.Job, error) {
	r.lookups++
	return r.existing, nil
}
func (r *subtitleCreateRepo) InsertJob(_ context.Context, job *ai.Job, _ *ai.JobQuota) error {
	job.ID = 9007199254740993
	job.CreatedAt = time.Now()
	job.UpdatedAt = job.CreatedAt
	copy := *job
	r.inserted = &copy
	return nil
}
func (r *subtitleCreateRepo) FailJob(context.Context, int64, ai.JobStatus, string) error { return nil }

func TestSubtitleAICreateAuthorizesAndBindsBeforeEnqueue(t *testing.T) {
	for _, name := range []string{"new-live", "deduplicated", "background", "foreign-session", "foreign-profile", "hidden-file", "wrong-filter"} {
		t.Run(name, func(t *testing.T) {
			repo := &subtitleCreateRepo{}
			sem := make(chan struct{}, 1)
			sem <- struct{}{}
			svc := ai.NewService(t.Context(), ai.Config{Configured: true, TranslateEnabled: true, ChatModel: "synthetic"}, repo, nil, nil, nil, nil, nil, nil, "", nil, sem)
			handler := NewSubtitleAIHandler(svc)
			sessions := playback.NewSessionManager(0, 0)
			session, err := sessions.StartSession(7, "viewer", 42, playback.PlayDirect, false)
			if err != nil {
				t.Fatal(err)
			}
			handler.LiveNotifier = playback.NewSubtitleReadyNotifier(sessions, playback.NewRealtimeHub(), nil)
			var hidden error
			if name == "hidden-file" {
				hidden = catalog.ErrItemNotFound
			}
			handler.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{err: hidden}}
			ctx := apimw.SetProfileID(apimw.SetClaims(t.Context(), &auth.Claims{UserID: 7, Role: "user"}), "viewer")
			filter := catalog.AccessFilter{UserID: 7, ProfileID: "viewer"}
			command := SubtitleAICreateCommand{MediaFileID: 42, Kind: ai.JobKindTranslate, SourceIndex: 3, TargetLanguage: "fr", SessionID: session.ID, StartPosition: 12.5}
			switch name {
			case "deduplicated":
				repo.existing = &ai.Job{ID: 9, MediaFileID: 42, Status: ai.JobStatusRunning, RequestedBy: new(99)}
			case "background":
				command.SessionID = ""
			case "foreign-session":
				command.SessionID = "foreign"
			case "foreign-profile":
				ctx = apimw.SetProfileID(ctx, "other")
				filter.ProfileID = "other"
			case "wrong-filter":
				filter.UserID = 99
			}
			result, err := handler.CreateSubtitleAIJob(ctx, filter, command)
			if name == "foreign-session" || name == "foreign-profile" || name == "hidden-file" || name == "wrong-filter" {
				if err == nil || repo.lookups != 0 || repo.inserted != nil {
					t.Fatal("unauthorized request reached enqueue", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name == "deduplicated" {
				if result.LiveDeliveryAttached || repo.inserted != nil || result.Job != repo.existing || result.Job.LiveNotifier != nil {
					t.Fatal("dedup rebound another requester's job")
				}
				return
			}
			job := repo.inserted
			if job == nil || job.RequestedBy == nil || *job.RequestedBy != 7 || job.MediaFileID != 42 || job.SourceIndex != 3 || job.StartPosition != 12.5 || job.SessionID != command.SessionID {
				t.Fatalf("wrong persisted attribution/input %+v", job)
			}
			if result.LiveDeliveryAttached != (name == "new-live") || (job.LiveNotifier != nil) != (name == "new-live") {
				t.Fatal("wrong live binding")
			}
		})
	}
}
