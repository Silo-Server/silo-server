package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
)

// fakeOriginalsPage is one scripted cleaner page.
type fakeOriginalsPage struct {
	stats chapterthumbs.OriginalsCleanupStats
	next  string
	err   error
}

// fakeOriginalsCleaner returns scripted pages and records the token each page
// started from. locked makes Exclusive report another holder.
type fakeOriginalsCleaner struct {
	pages  []fakeOriginalsPage
	tokens []string
	locked bool
	inLock bool
}

func (f *fakeOriginalsCleaner) Exclusive(_ context.Context, fn func() error) (bool, error) {
	if f.locked {
		return false, nil
	}
	f.inLock = true
	defer func() { f.inLock = false }()
	return true, fn()
}

func (f *fakeOriginalsCleaner) Page(_ context.Context, token string) (chapterthumbs.OriginalsCleanupStats, string, error) {
	if !f.inLock {
		panic("Page called without holding the cleanup lock")
	}
	i := len(f.tokens)
	f.tokens = append(f.tokens, token)
	p := f.pages[i]
	return p.stats, p.next, p.err
}

var chapterOriginalsArmed = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

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
	task := NewCleanupChapterThumbnailOriginalsTask(cleaner, store, "store")
	task.now = func() time.Time { return chapterOriginalsArmed.Add(2 * chapterOriginalsGrace) }
	return task, store
}

// armed is a checkpoint whose grace period has passed.
func armed(cp chapterOriginalsCheckpoint) *chapterOriginalsCheckpoint {
	cp.Identity = "store"
	cp.ArmedAt = chapterOriginalsArmed
	return &cp
}

func savedChapterOriginalsCheckpoint(t *testing.T, store *fakeSettingsStore) chapterOriginalsCheckpoint {
	t.Helper()
	var saved chapterOriginalsCheckpoint
	if err := json.Unmarshal([]byte(store.values[ChapterThumbnailOriginalsCleanupKey]), &saved); err != nil {
		t.Fatalf("checkpoint unreadable: %v", err)
	}
	return saved
}

func TestCleanupChapterThumbnailOriginalsWaitsOutTheGraceBeforeDeleting(t *testing.T) {
	cleaner := &fakeOriginalsCleaner{pages: []fakeOriginalsPage{{stats: chapterthumbs.OriginalsCleanupStats{Deleted: 4}}}}
	task, store := chapterOriginalsTask(t, cleaner, nil)
	first := chapterOriginalsArmed
	task.now = func() time.Time { return first }

	// The first run only arms the cleanup.
	if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatal(err)
	}
	if len(cleaner.tokens) != 0 {
		t.Fatal("the first run deleted before the grace period")
	}
	if saved := savedChapterOriginalsCheckpoint(t, store); !saved.ArmedAt.Equal(first) || saved.Done {
		t.Fatalf("checkpoint = %+v, want armed at the first run", saved)
	}

	// A restart inside the grace period does not reset or skip the wait.
	task.now = func() time.Time { return first.Add(chapterOriginalsGrace - time.Minute) }
	if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatal(err)
	}
	if len(cleaner.tokens) != 0 {
		t.Fatal("a run inside the grace period deleted")
	}
	if saved := savedChapterOriginalsCheckpoint(t, store); !saved.ArmedAt.Equal(first) {
		t.Fatalf("armed_at = %v, want it kept at %v", saved.ArmedAt, first)
	}

	task.now = func() time.Time { return first.Add(chapterOriginalsGrace) }
	if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatal(err)
	}
	if len(cleaner.tokens) != 1 {
		t.Fatal("the run after the grace period did not clean")
	}
}

func TestCleanupChapterThumbnailOriginalsFinishesAndStopsScheduling(t *testing.T) {
	cleaner := &fakeOriginalsCleaner{pages: []fakeOriginalsPage{
		{stats: chapterthumbs.OriginalsCleanupStats{Originals: 1000, Deleted: 1000}, next: "chapter-images/5/0/w300.webp"},
		{stats: chapterthumbs.OriginalsCleanupStats{Originals: 10, Deleted: 9, Referenced: 1}},
	}}
	task, store := chapterOriginalsTask(t, cleaner, armed(chapterOriginalsCheckpoint{}))

	if run, err := task.ShouldRun(t.Context()); err != nil || !run {
		t.Fatalf("ShouldRun before the cleanup = %v, %v; want true", run, err)
	}
	progress := &fakeProgress{}
	if err := task.Execute(t.Context(), progress); err != nil {
		t.Fatal(err)
	}
	if want := []string{"", "chapter-images/5/0/w300.webp"}; !slices.Equal(cleaner.tokens, want) {
		t.Fatalf("page tokens = %v, want %v", cleaner.tokens, want)
	}
	// An original kept for a chapter with no w300 image cannot be deleted by
	// running again, so it does not hold the pass open.
	saved := savedChapterOriginalsCheckpoint(t, store)
	if !saved.Done || saved.Identity != "store" || !saved.ArmedAt.Equal(chapterOriginalsArmed) {
		t.Fatalf("checkpoint = %+v, want done for this storage", saved)
	}
	var result chapterthumbs.OriginalsCleanupStats
	if err := json.Unmarshal(progress.resultData, &result); err != nil || result.Deleted != 1009 || result.Referenced != 1 {
		t.Fatalf("result = %+v (%v), want 1009 deleted and 1 kept", result, err)
	}
	if run, err := task.ShouldRun(t.Context()); err != nil || run {
		t.Fatalf("ShouldRun after the cleanup = %v, %v; want false", run, err)
	}

	// A manual run on finished storage starts a fresh pass right away.
	cleaner.pages = append(cleaner.pages, fakeOriginalsPage{})
	if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatal(err)
	}
	if len(cleaner.tokens) != 3 || cleaner.tokens[2] != "" {
		t.Fatalf("manual run tokens = %v, want a pass from the beginning", cleaner.tokens)
	}
}

func TestCleanupChapterThumbnailOriginalsRestartsAPassThatLeftOriginals(t *testing.T) {
	for name, deferred := range map[string]chapterthumbs.OriginalsCleanupStats{
		"too new":       {TooNew: 3},
		"failed delete": {DeleteFailed: 1},
	} {
		t.Run(name, func(t *testing.T) {
			cleaner := &fakeOriginalsCleaner{pages: []fakeOriginalsPage{{stats: deferred, next: "chapter-images/2/"}, {}}}
			task, store := chapterOriginalsTask(t, cleaner, armed(chapterOriginalsCheckpoint{}))
			if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
				t.Fatal(err)
			}
			saved := savedChapterOriginalsCheckpoint(t, store)
			if saved.Done || saved.Token != "" || saved.Deferred || !saved.ArmedAt.Equal(chapterOriginalsArmed) {
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
	cleaner := &fakeOriginalsCleaner{pages: []fakeOriginalsPage{
		{stats: chapterthumbs.OriginalsCleanupStats{TooNew: 1}, next: "chapter-images/9/"},
		{err: boom, next: "chapter-images/9/"},
	}}
	task, store := chapterOriginalsTask(t, cleaner, armed(chapterOriginalsCheckpoint{Token: "chapter-images/3/"}))
	if err := task.Execute(t.Context(), &fakeProgress{}); !errors.Is(err, boom) {
		t.Fatalf("Execute() error = %v, want the cleaner error", err)
	}
	if want := []string{"chapter-images/3/", "chapter-images/9/"}; !slices.Equal(cleaner.tokens, want) {
		t.Fatalf("page tokens = %v, want %v", cleaner.tokens, want)
	}
	saved := savedChapterOriginalsCheckpoint(t, store)
	if saved.Token != "chapter-images/9/" || !saved.Deferred || saved.Done {
		t.Fatalf("checkpoint = %+v, want the failed page kept and the deferral recorded", saved)
	}
}

func TestCleanupChapterThumbnailOriginalsLeavesAnotherNodesCheckpoint(t *testing.T) {
	start := armed(chapterOriginalsCheckpoint{Token: "chapter-images/7/"})
	cleaner := &fakeOriginalsCleaner{locked: true}
	task, store := chapterOriginalsTask(t, cleaner, start)
	if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatal(err)
	}
	if saved := savedChapterOriginalsCheckpoint(t, store); saved != *start {
		t.Fatalf("checkpoint = %+v, want %+v untouched", saved, *start)
	}
}

func TestCleanupChapterThumbnailOriginalsRearmsAfterAStorageMove(t *testing.T) {
	cleaner := &fakeOriginalsCleaner{}
	old := armed(chapterOriginalsCheckpoint{Token: "chapter-images/7/", Done: true})
	old.Identity = "old-store"
	task, store := chapterOriginalsTask(t, cleaner, old)
	if run, err := task.ShouldRun(t.Context()); err != nil || !run {
		t.Fatalf("ShouldRun on new storage = %v, %v; want true", run, err)
	}
	if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatal(err)
	}
	if len(cleaner.tokens) != 0 {
		t.Fatal("cleaned new storage without arming it first")
	}
	if saved := savedChapterOriginalsCheckpoint(t, store); saved.Identity != "store" || saved.Token != "" || saved.Done {
		t.Fatalf("checkpoint = %+v, want a fresh checkpoint for the new storage", saved)
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
