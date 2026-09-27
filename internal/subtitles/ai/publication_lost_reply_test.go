package ai

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// committedFinalReplyLoss models a successful atomic publication whose reply
// is lost. Storage's real PostgreSQL lost-reply regression covers persisted
// metadata and retained bytes; this test follows that outcome through Service.
type committedFinalReplyLoss struct {
	*recordingSubtitleStore
	persisted Job
}

func (s *committedFinalReplyLoss) StoreSubtitle(_ context.Context, req subtitles.StoreSubtitleRequest) (*subtitles.DownloadedSubtitle, error) {
	if req.Publication == nil || !req.Publication.Complete {
		panic("expected final atomic publication")
	}
	s.stored = append(s.stored, req)
	s.persisted = Job{ID: req.Publication.JobID, Status: JobStatusCompleted, ResultSubtitleID: new(12)}
	return nil, fmt.Errorf("commit reply lost: %w", subtitles.ErrAIPublicationUncertain)
}
func TestLostSuccessfulPublicationDoesNotAnnounceFailure(t *testing.T) {
	for _, kind := range []JobKind{JobKindTranslate, JobKindTranscribe} {
		t.Run(string(kind), func(t *testing.T) {
			repo := &recordingRepo{}
			recorded := &recordingSubtitleStore{}
			store := &committedFinalReplyLoss{recordingSubtitleStore: recorded}
			notifier := &recordingNotifier{}
			svc := newRunTranscribeService(repo, &recordingTranslator{}, &chunkedTranscriber{detected: "en", chunks: [][]SubtitleCue{{testCue(0, "synthetic")}}}, recorded, notifier, Config{})
			svc.store = store
			job := &Job{ID: 7, MediaFileID: 10, Kind: kind, SourceIndex: -1, TargetLanguage: "fr", SessionID: "synthetic-session"}
			if kind == JobKindTranslate {
				if svc.finishTranslatedTrack(t.Context(), t.Context(), job, []SubtitleCue{testCue(0, "synthetic")}, "translated", "track", true) {
					t.Fatal("lost reply treated as confirmed completion")
				}
			} else {
				svc.runTranscribe(t.Context(), job)
			}
			if store.persisted.ID != job.ID || store.persisted.Status != JobStatusCompleted || store.persisted.ResultSubtitleID == nil || *store.persisted.ResultSubtitleID != 12 {
				t.Fatal("fixture did not commit final output")
			}
			if len(repo.failures) != 0 || len(repo.completed) != 0 {
				t.Fatal("unknown outcome attempted a new terminal write")
			}
			for _, event := range notifier.events {
				if event.kind == "failed" || event.kind == "completed" || strings.HasPrefix(event.kind, "ready:") {
					t.Fatalf("unknown outcome announced %s", event.kind)
				}
			}
		})
	}
}
