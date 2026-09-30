package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
)

// fakeOriginalsCleaner returns one scripted result per call and records the
// token each call started from.
type fakeOriginalsCleaner struct {
	results []chapterthumbs.OriginalsCleanupStats
	errs    []error
	tokens  []string
}

func (f *fakeOriginalsCleaner) Run(_ context.Context, token string, _ int) (chapterthumbs.OriginalsCleanupStats, error) {
	i := len(f.tokens)
	f.tokens = append(f.tokens, token)
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return f.results[i], err
}

func chapterOriginalsTask(t *testing.T, cleaner *fakeOriginalsCleaner, start *chapterOriginalsCheckpoint) (*CleanupChapterThumbnailOriginalsTask, *fakeSettingsStore) {
	t.Helper()
	store := &fakeSettingsStore{values: map[string]string{}}
	if start != nil {
		encoded, err := json.Marshal(start)
		if err != nil {
			t.Fatal(err)
		}
		store.values[ChapterThumbnailOriginalsCleanupKey] = string(encoded)
	}
	return NewCleanupChapterThumbnailOriginalsTask(cleaner, store, "store"), store
}

func savedChapterOriginalsCheckpoint(t *testing.T, store *fakeSettingsStore) chapterOriginalsCheckpoint {
	t.Helper()
	var saved chapterOriginalsCheckpoint
	if err := json.Unmarshal([]byte(store.values[ChapterThumbnailOriginalsCleanupKey]), &saved); err != nil {
		t.Fatalf("checkpoint unreadable: %v", err)
	}
	return saved
}

func TestCleanupChapterThumbnailOriginalsFinishesAndStopsScheduling(t *testing.T) {
	cleaner := &fakeOriginalsCleaner{results: []chapterthumbs.OriginalsCleanupStats{
		{Originals: 1000, Deleted: 1000, NextToken: "chapter-images/5/0/w300.webp"},
		{Originals: 10, Deleted: 9, Referenced: 1, Done: true},
	}}
	task, store := chapterOriginalsTask(t, cleaner, nil)

	if run, err := task.ShouldRun(t.Context()); err != nil || !run {
		t.Fatalf("ShouldRun before the cleanup = %v, %v; want true", run, err)
	}
	progress := &fakeProgress{}
	if err := task.Execute(t.Context(), progress); err != nil {
		t.Fatal(err)
	}
	if want := []string{"", "chapter-images/5/0/w300.webp"}; !slices.Equal(cleaner.tokens, want) {
		t.Fatalf("cleaner tokens = %v, want %v", cleaner.tokens, want)
	}
	// A referenced original serves its chapter; it does not hold the pass open.
	if saved := savedChapterOriginalsCheckpoint(t, store); !saved.Done || saved.Identity != "store" {
		t.Fatalf("checkpoint = %+v, want done for this storage", saved)
	}
	var result chapterthumbs.OriginalsCleanupStats
	if err := json.Unmarshal(progress.resultData, &result); err != nil || result.Deleted != 1009 {
		t.Fatalf("result = %+v (%v), want 1009 deleted", result, err)
	}
	if run, err := task.ShouldRun(t.Context()); err != nil || run {
		t.Fatalf("ShouldRun after the cleanup = %v, %v; want false", run, err)
	}

	// A manual run on finished storage starts a fresh pass.
	cleaner.results = append(cleaner.results, chapterthumbs.OriginalsCleanupStats{Done: true})
	if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatal(err)
	}
	if got := cleaner.tokens[len(cleaner.tokens)-1]; got != "" {
		t.Fatalf("manual run started from %q, want the beginning", got)
	}
}

func TestCleanupChapterThumbnailOriginalsRestartsAPassThatLeftOriginals(t *testing.T) {
	for name, deferred := range map[string]chapterthumbs.OriginalsCleanupStats{
		"too new":       {TooNew: 3, NextToken: "chapter-images/2/"},
		"failed delete": {DeleteFailed: 1, NextToken: "chapter-images/2/"},
	} {
		t.Run(name, func(t *testing.T) {
			cleaner := &fakeOriginalsCleaner{results: []chapterthumbs.OriginalsCleanupStats{deferred, {Done: true}}}
			task, store := chapterOriginalsTask(t, cleaner, nil)
			if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
				t.Fatal(err)
			}
			saved := savedChapterOriginalsCheckpoint(t, store)
			if saved.Done || saved.Token != "" || saved.Deferred {
				t.Fatalf("checkpoint = %+v, want a fresh pass on the next run", saved)
			}
			if run, _ := task.ShouldRun(t.Context()); !run {
				t.Fatal("ShouldRun = false; originals were left behind")
			}
		})
	}
}

func TestCleanupChapterThumbnailOriginalsResumesAfterAnError(t *testing.T) {
	boom := errors.New("storage unavailable")
	cleaner := &fakeOriginalsCleaner{
		results: []chapterthumbs.OriginalsCleanupStats{{TooNew: 1, NextToken: "chapter-images/9/"}},
		errs:    []error{boom},
	}
	task, store := chapterOriginalsTask(t, cleaner, &chapterOriginalsCheckpoint{Identity: "store", Token: "chapter-images/3/"})
	if err := task.Execute(t.Context(), &fakeProgress{}); !errors.Is(err, boom) {
		t.Fatalf("Execute() error = %v, want the cleaner error", err)
	}
	if cleaner.tokens[0] != "chapter-images/3/" {
		t.Fatalf("cleanup started from %q, want the saved token", cleaner.tokens[0])
	}
	saved := savedChapterOriginalsCheckpoint(t, store)
	if saved.Token != "chapter-images/9/" || !saved.Deferred || saved.Done {
		t.Fatalf("checkpoint = %+v, want the progress made and the deferral kept", saved)
	}
}

func TestCleanupChapterThumbnailOriginalsLeavesAnotherNodesCheckpoint(t *testing.T) {
	start := chapterOriginalsCheckpoint{Identity: "store", Token: "chapter-images/7/"}
	cleaner := &fakeOriginalsCleaner{results: []chapterthumbs.OriginalsCleanupStats{{Skipped: true}}}
	task, store := chapterOriginalsTask(t, cleaner, &start)
	if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatal(err)
	}
	if saved := savedChapterOriginalsCheckpoint(t, store); saved != start {
		t.Fatalf("checkpoint = %+v, want %+v untouched", saved, start)
	}
}

func TestCleanupChapterThumbnailOriginalsRunsAgainAfterAStorageMove(t *testing.T) {
	cleaner := &fakeOriginalsCleaner{results: []chapterthumbs.OriginalsCleanupStats{{Done: true}}}
	task, _ := chapterOriginalsTask(t, cleaner, &chapterOriginalsCheckpoint{Identity: "old-store", Token: "chapter-images/7/", Done: true})
	if run, err := task.ShouldRun(t.Context()); err != nil || !run {
		t.Fatalf("ShouldRun on new storage = %v, %v; want true", run, err)
	}
	if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatal(err)
	}
	if cleaner.tokens[0] != "" {
		t.Fatalf("cleanup on new storage started from %q, want the beginning", cleaner.tokens[0])
	}
}

func TestCleanupChapterThumbnailOriginalsWithoutStorage(t *testing.T) {
	task := NewCleanupChapterThumbnailOriginalsTask(nil, nil, "")
	if run, err := task.ShouldRun(context.Background()); err != nil || run {
		t.Fatalf("ShouldRun = %v, %v; want false", run, err)
	}
	if err := task.Execute(context.Background(), &fakeProgress{}); err != nil {
		t.Fatal(err)
	}
}
