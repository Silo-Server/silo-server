package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type failingPublicationStore struct {
	*recordingSubtitleStore
	failure error
}

func (s failingPublicationStore) StoreSubtitle(_ context.Context, req subtitles.StoreSubtitleRequest) (*subtitles.DownloadedSubtitle, error) {
	s.stored = append(s.stored, req)
	return nil, s.failure
}
func TestPublicationFailureDoesNotAnnounceReadyOrComplete(t *testing.T) {
	for _, failure := range []error{subtitles.ErrAIJobInactive, subtitles.ErrAIPublicationUncertain, errors.New("publication failed before commit")} {
		for _, kind := range []JobKind{JobKindTranslate, JobKindTranscribe, JobKindTranscribeTranslate} {
			t.Run(string(kind)+"/"+failure.Error(), func(t *testing.T) {
				repo := &recordingRepo{}
				store := &recordingSubtitleStore{}
				notifier := &recordingNotifier{}
				svc := newRunTranscribeService(repo, &recordingTranslator{}, &chunkedTranscriber{detected: "en", chunks: [][]SubtitleCue{{testCue(0, "synthetic")}}}, store, notifier, Config{})
				svc.store = failingPublicationStore{store, failure}
				job := &Job{ID: 7, MediaFileID: 10, Kind: kind, SourceIndex: -1, TargetLanguage: "fr", SessionID: "synthetic-session"}
				if kind == JobKindTranslate {
					if svc.finishTranslatedTrack(t.Context(), t.Context(), job, []SubtitleCue{testCue(0, "synthetic")}, "translated", "track", true) {
						t.Fatal("unconfirmed publication succeeded")
					}
				} else {
					svc.runTranscribe(t.Context(), job)
				}
				if len(store.stored) != 1 || store.stored[0].Publication == nil || store.stored[0].Publication.JobID != job.ID {
					t.Fatal("output did not require job publication fence")
				}
				if complete := store.stored[0].Publication.Complete; complete != (kind != JobKindTranscribeTranslate) {
					t.Fatalf("complete=%v for %s", complete, kind)
				}
				if (errors.Is(failure, subtitles.ErrAIJobInactive) || errors.Is(failure, subtitles.ErrAIPublicationUncertain)) && len(repo.failures) != 0 {
					t.Fatal("inactive publisher attempted another terminal transition")
				}
				for _, event := range notifier.events {
					if (errors.Is(failure, subtitles.ErrAIJobInactive) || errors.Is(failure, subtitles.ErrAIPublicationUncertain)) && event.kind == "failed" {
						t.Fatal("inactive publisher announced a new failure")
					}
					if event.kind == "completed" || strings.HasPrefix(event.kind, "ready:") {
						t.Fatalf("unconfirmed output announced: %s", event.kind)
					}
				}
				if len(repo.completed) != 0 {
					t.Fatal("standalone job completion bypassed publication")
				}
			})
		}
	}
}
