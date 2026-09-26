package ai

import (
	"errors"
	"testing"
)

func TestJobBoundNotifierOwnsLiveAndPublicationEvents(t *testing.T) {
	for _, kind := range []JobKind{JobKindTranscribe, JobKindTranscribeTranslate} {
		t.Run(string(kind), func(t *testing.T) {
			defaultNotifier, bound := &recordingNotifier{}, &recordingNotifier{}
			repo := &recordingRepo{}
			svc := newRunTranscribeService(repo, &recordingTranslator{}, &chunkedTranscriber{detected: "en", chunks: [][]SubtitleCue{{testCue(0, "synthetic")}}}, &recordingSubtitleStore{}, defaultNotifier, Config{})
			job := &Job{ID: 7, MediaFileID: 10, Kind: kind, SourceIndex: -1, TargetLanguage: "fr", SessionID: "session", LiveNotifier: bound}
			svc.runTranscribe(t.Context(), job)
			if len(defaultNotifier.events) != 0 {
				t.Fatal("job used unrestricted notifier")
			}
			if len(bound.events) == 0 {
				t.Fatal("no bound events")
			}
			completed, ready := false, false
			for _, event := range bound.events {
				if event.kind == "completed" {
					completed = true
				}
				if len(event.kind) >= 6 && event.kind[:6] == "ready:" {
					ready = true
				}
			}
			if !completed || !ready {
				t.Fatalf("missing final notifications: %+v", bound.events)
			}
			before := len(bound.events)
			svc.finishWithError(t.Context(), job, errors.New("synthetic failure"))
			if len(defaultNotifier.events) != 0 || len(bound.events) != before+1 || bound.events[before].kind != "failed" {
				t.Fatal("error path escaped bound notifier")
			}
		})
	}
}
