package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type finishedJobRepo struct {
	recordingRepo
	finished chan JobStatus
}

func (r *finishedJobRepo) FailJob(ctx context.Context, id int64, status JobStatus, message string) error {
	err := r.recordingRepo.FailJob(ctx, id, status, message)
	r.finished <- status
	return err
}

func TestDisconnectedTextProviderRejectsTranslationAndPreservesASR(t *testing.T) {
	reads := 0
	client := llm.NewClient(llm.Config{TextConfigSource: func(context.Context) (llm.Config, error) {
		reads++
		return llm.Config{}, errors.New("sign in required")
	}})
	repo := &finishedJobRepo{finished: make(chan JobStatus, 1)}
	cfg := Config{Configured: true, SpeechConfigured: true, TranslateEnabled: true, TranscribeEnabled: true, ChatModel: "stale-model", ASRModel: "whisper-1", TextConfigSource: client.TextConfig}
	svc := NewService(t.Context(), cfg, repo, nil, stubTranscriber{}, nil, nil, nilFileResolver{}, nil, "", nil, nil)
	if svc.Enabled(t.Context()) || !svc.TranscribeEnabled() {
		t.Fatal("disconnected text provider changed ASR readiness or advertised translation")
	}
	for _, kind := range []JobKind{JobKindTranslate, JobKindTranscribeTranslate} {
		if _, err := svc.Enqueue(t.Context(), JobRequest{MediaFileID: 1, Kind: kind, TargetLanguage: "fr"}); !errors.Is(err, ErrEngineNotConfigured) {
			t.Fatalf("%s enqueue=%v", kind, err)
		}
	}
	if repo.inserts != 0 {
		t.Fatal("disconnected provider inserted translation jobs")
	}
	before := reads
	job, err := svc.Enqueue(t.Context(), JobRequest{MediaFileID: 1, Kind: JobKindTranscribe})
	if err != nil || job.Model != "whisper-1" || reads != before {
		t.Fatalf("plain ASR consulted text provider: job=%v, err=%v", job, err)
	}
	select {
	case <-repo.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("ASR fixture did not finish")
	}
}

type snapshotTranslator struct {
	client *llm.Client
	models chan string
}

type notifyingSubtitleStore struct {
	recordingSubtitleStore
	finished chan JobStatus
}

func (s *notifyingSubtitleStore) StoreSubtitle(ctx context.Context, req subtitles.StoreSubtitleRequest) (*subtitles.DownloadedSubtitle, error) {
	sub, err := s.recordingSubtitleStore.StoreSubtitle(ctx, req)
	if err == nil && req.Provider == providerTranslated {
		s.finished <- JobStatusCompleted
	}
	return sub, err
}

func (s snapshotTranslator) Translate(ctx context.Context, req TranslateRequest, onBatch func([]SubtitleCue, int, int)) ([]SubtitleCue, error) {
	cfg, err := s.client.TextConfig(ctx)
	if err != nil {
		return nil, err
	}
	s.models <- cfg.ChatModel
	if onBatch != nil {
		onBatch(req.Cues, len(req.Cues), len(req.Cues))
	}
	return req.Cues, nil
}
