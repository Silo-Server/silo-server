package autoscan

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

type fakeStore struct {
	settings Settings
	sources  []Source
	advanced map[string]time.Time
}

func (f *fakeStore) GetSettings(context.Context) (Settings, error)        { return f.settings, nil }
func (f *fakeStore) ListEnabledSources(context.Context) ([]Source, error) { return f.sources, nil }
func (f *fakeStore) AdvanceLastPoll(_ context.Context, id string, at time.Time) error {
	if f.advanced == nil {
		f.advanced = map[string]time.Time{}
	}
	f.advanced[id] = at
	return nil
}

type fakeHistory struct {
	paths map[string][]string
	err   error
}

func (f *fakeHistory) ImportedPaths(_ context.Context, baseURL, _ string, _ time.Time) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.paths[baseURL], nil
}

type fakeResolver struct{}

func (fakeResolver) Resolve(_ context.Context, req scantrigger.Request) (*scantrigger.Target, error) {
	if len(req.Path) >= 11 && req.Path[:11] == "/mnt/media/" {
		return &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: scantrigger.ModeSubtree, Path: req.Path, Trigger: req.Trigger}, nil
	}
	return nil, nil
}

type recordingQueuer struct{ enqueued []scantrigger.Target }

func (q *recordingQueuer) EnqueueScans(_ context.Context, targets []scantrigger.Target) error {
	q.enqueued = append(q.enqueued, targets...)
	return nil
}

type allowSuppressor struct{}

func (allowSuppressor) ShouldScan(context.Context, int, time.Duration) (bool, error) {
	return true, nil
}
func (allowSuppressor) Release(context.Context, int) error { return nil }

type recordingSuppressor struct {
	claimed  []int
	released []int
}

func (s *recordingSuppressor) ShouldScan(_ context.Context, folderID int, _ time.Duration) (bool, error) {
	s.claimed = append(s.claimed, folderID)
	return true, nil
}
func (s *recordingSuppressor) Release(_ context.Context, folderID int) error {
	s.released = append(s.released, folderID)
	return nil
}

type failingQueuer struct{}

func (failingQueuer) EnqueueScans(context.Context, []scantrigger.Target) error {
	return context.DeadlineExceeded
}

func TestPollOnceEnqueuesDedupedFolders(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, PollIntervalMinutes: 10, DebounceSeconds: 60},
		sources: []Source{{
			IntegrationID: "i1", Kind: "sonarr", BaseURL: "http://sonarr", APIKeyRef: "k", Enabled: true,
		}},
	}
	hist := &fakeHistory{paths: map[string][]string{
		"http://sonarr": {
			"/mnt/media/Show/S01/E01.mkv",
			"/mnt/media/Show/S01/E02.mkv",
			"/outside/lib/x.mkv",
		},
	}}
	q := &recordingQueuer{}
	svc := NewService(store, hist, fakeResolver{}, q, allowSuppressor{}, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("expected 1 deduped folder scan, got %d: %+v", len(q.enqueued), q.enqueued)
	}
	if q.enqueued[0].Trigger != "autoscan" || q.enqueued[0].Folder.ID != 7 {
		t.Fatalf("unexpected target: %+v", q.enqueued[0])
	}
	if _, ok := store.advanced["i1"]; !ok {
		t.Fatalf("expected last_poll advanced for i1")
	}
}

func TestPollOnceDisabledNoop(t *testing.T) {
	store := &fakeStore{settings: Settings{Enabled: false}}
	q := &recordingQueuer{}
	svc := NewService(store, &fakeHistory{}, fakeResolver{}, q, allowSuppressor{}, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("disabled autoscan should enqueue nothing, got %d", len(q.enqueued))
	}
}

func TestPollOnceSourceFailureDoesNotAdvance(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, PollIntervalMinutes: 10, DebounceSeconds: 60},
		sources:  []Source{{IntegrationID: "i1", BaseURL: "http://x", APIKeyRef: "k", Enabled: true}},
	}
	hist := &fakeHistory{err: context.DeadlineExceeded}
	q := &recordingQueuer{}
	svc := NewService(store, hist, fakeResolver{}, q, allowSuppressor{}, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce should not propagate per-source error: %v", err)
	}
	if _, ok := store.advanced["i1"]; ok {
		t.Fatalf("last_poll must NOT advance on source failure")
	}
}

func TestPollOnceReleasesClaimOnEnqueueFailure(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, PollIntervalMinutes: 10, DebounceSeconds: 60},
		sources:  []Source{{IntegrationID: "i1", BaseURL: "http://x", APIKeyRef: "k", Enabled: true}},
	}
	hist := &fakeHistory{paths: map[string][]string{"http://x": {"/mnt/media/Show/S01/E01.mkv"}}}
	sup := &recordingSuppressor{}
	svc := NewService(store, hist, fakeResolver{}, failingQueuer{}, sup, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(sup.claimed) != 1 || len(sup.released) != 1 || sup.released[0] != sup.claimed[0] {
		t.Fatalf("expected claimed folder to be released, claimed=%v released=%v", sup.claimed, sup.released)
	}
	if _, ok := store.advanced["i1"]; ok {
		t.Fatalf("last_poll must NOT advance when enqueue fails")
	}
}
