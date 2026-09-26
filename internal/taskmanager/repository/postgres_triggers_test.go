package repository

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

func TestScheduleCASAndEmptyPersistence(t *testing.T) {
	pool := taskHistoryTestPool(t)
	key := taskKey(t, pool, "schedule")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM task_triggers WHERE task_key=$1`, key)
		_, _ = pool.Exec(context.Background(), `DELETE FROM task_schedules WHERE task_key=$1`, key)
	})
	repo := NewPgTriggerRepository(pool)
	initial, err := repo.GetSchedule(t.Context(), key)
	if err != nil || initial.Revision != 0 {
		t.Fatalf("initial: %+v %v", initial, err)
	}
	saved, err := repo.ReplaceSchedule(t.Context(), key, 0, []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := repo.ReplaceSchedule(t.Context(), key, saved.Revision, nil); errs <- err })
	}
	wg.Wait()
	close(errs)
	conflicts := 0
	for err := range errs {
		if _, ok := errors.AsType[*taskmanager.ScheduleConflict](err); ok {
			conflicts++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("conflicts=%d", conflicts)
	}
	configs, err := repo.GetTriggers(t.Context(), key)
	if err != nil || configs == nil || len(configs) != 0 {
		t.Fatalf("empty schedule not retained: %+v %v", configs, err)
	}
	current, err := repo.GetSchedule(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetTriggers(t.Context(), key, []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "01:00"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReplaceSchedule(t.Context(), key, current.Revision, nil); err == nil {
		t.Fatal("legacy writer failed to invalidate guard")
	}
}

func TestTaskHistoryPageTimestampTies(t *testing.T) {
	pool := taskHistoryTestPool(t)
	key := taskKey(t, pool, "pages")
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	var expected []int64
	for range 5 {
		expected = append(expected, insertTaskExecution(t, pool, key, at))
	}
	repo := NewPgExecutionRepository(pool)
	var beforeID int64
	var before time.Time
	var seen []int64
	for {
		page, err := repo.ListPage(t.Context(), key, before, beforeID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			seen = append(seen, e.ID)
		}
		last := page[len(page)-1]
		beforeID = last.ID
		before = last.CompletedAt
	}
	slices.Reverse(expected)
	if !slices.Equal(seen, expected) {
		t.Fatalf("page traversal=%v want %v", seen, expected)
	}
	if _, err := repo.ListPage(t.Context(), key, time.Time{}, 0, 202); err == nil {
		t.Fatal("unbounded limit accepted")
	}
}
