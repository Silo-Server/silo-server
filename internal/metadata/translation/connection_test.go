package translation

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
)

func TestDisconnectedProviderRejectsAllMetadataEnqueuePaths(t *testing.T) {
	repo := newFakeRepo()
	client := llm.NewClient(llm.Config{TextConfigSource: func(context.Context) (llm.Config, error) {
		return llm.Config{}, chatgpt.ErrSignInRequired
	}})
	cfg := Config{Enabled: true, Configured: true, ChatModel: "stale-model", OnView: "auto", TextConfigSource: client.TextConfig}
	content := seriesContent()
	content.missing = 1
	svc := NewService(t.Context(), cfg, repo, content, &fakeLocs{}, (&upperChat{}).fn, nil, nil)
	if svc.Enabled(t.Context()) || svc.OnViewMode(t.Context()) != "off" {
		t.Fatal("disconnected text provider advertised as available")
	}
	if _, err := svc.Enqueue(t.Context(), JobRequest{ContentID: "series1", TargetLanguage: "fr"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("admin enqueue=%v", err)
	}
	if _, err := svc.RequestOnView(t.Context(), TargetItem, "series1", "fr", nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("viewer enqueue=%v", err)
	}
	svc.AutoEnqueue(t.Context(), "series1", "fr")
	if repo.nextID != 0 {
		t.Fatal("disconnected provider inserted durable jobs")
	}
}

func TestMetadataJobUsesEnqueueProviderAcrossQueuedAndBatchedWork(t *testing.T) {
	var current atomic.Pointer[llm.Config]
	current.Store(new(llm.Config{BaseURL: "https://provider.example.test", ChatModel: "selected-model"}))
	client := llm.NewClient(llm.Config{TextConfigSource: func(context.Context) (llm.Config, error) { return *current.Load(), nil }})
	repo, content, locs, chat := newFakeRepo(), seriesContent(), &fakeLocs{}, &upperChat{}
	for i := range metadataBatchSize {
		content.seasons = append(content.seasons, ChildText{ContentID: fmt.Sprint("season", i), SeasonNumber: i + 3, Overview: "Another season."})
	}
	sem := jobrunner.NewSemaphore(1)
	sem <- struct{}{}
	models := make(chan string, 3)
	svc := NewService(t.Context(), Config{Enabled: true, Configured: true, ChatModel: "stale-model", TextConfigSource: client.TextConfig}, repo, content, locs,
		func(ctx context.Context, system, user string) (string, error) {
			cfg, err := client.TextConfig(ctx)
			if err != nil {
				return "", err
			}
			models <- cfg.ChatModel
			current.Store(new(llm.Config{BaseURL: "https://later.example.test", ChatModel: "changed-between-batches"}))
			return chat.fn(ctx, system, user)
		}, sem, nil)
	job, err := svc.Enqueue(t.Context(), JobRequest{ContentID: "series1", TargetLanguage: "fr", IncludeChildren: true})
	if err != nil {
		t.Fatal(err)
	}
	if job.Model != "selected-model" || job.IdempotencyKey != idempotencyKey(TargetItem, "series1", "fr", job.Model, true, false) {
		t.Fatal("recorded model did not use live selection")
	}
	current.Store(new(llm.Config{BaseURL: "https://later.example.test", ChatModel: "changed-while-queued"}))
	<-sem
	waitDone(t, repo)
	if final := repo.job(job.ID); final.Status != jobrunner.StatusCompleted {
		t.Fatalf("job failed: %s", final.ErrorMessage)
	}
	if len(models) != 2 {
		t.Fatalf("batches=%d, want 2", len(models))
	}
	for range 2 {
		if model := <-models; model != job.Model {
			t.Fatalf("batch model=%q, recorded=%q", model, job.Model)
		}
	}
}
