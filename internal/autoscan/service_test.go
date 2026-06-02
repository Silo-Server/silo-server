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

func (f *fakeHistory) ChangedPaths(_ context.Context, baseURL, _ string, _ time.Time) ([]string, error) {
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

func (allowSuppressor) ShouldScan(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (allowSuppressor) Release(context.Context, string) error { return nil }

type recordingSuppressor struct {
	claimed  []string
	released []string
}

func (s *recordingSuppressor) ShouldScan(_ context.Context, key string, _ time.Duration) (bool, error) {
	s.claimed = append(s.claimed, key)
	return true, nil
}
func (s *recordingSuppressor) Release(_ context.Context, key string) error {
	s.released = append(s.released, key)
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

func TestPollOnceScansDistinctPathsUnderSameFolder(t *testing.T) {
	// Two imported subtrees resolve to the SAME folder ID (7) but DIFFERENT
	// target paths. Keying suppression on folder ID alone would drop the second;
	// keying on (folder, path) must scan both.
	store := &fakeStore{
		settings: Settings{Enabled: true, PollIntervalMinutes: 10, DebounceSeconds: 60},
		sources: []Source{{
			IntegrationID: "i1", Kind: "sonarr", BaseURL: "http://sonarr", APIKeyRef: "k", Enabled: true,
		}},
	}
	hist := &fakeHistory{paths: map[string][]string{
		"http://sonarr": {
			"/mnt/media/ShowA/S01/E01.mkv",
			"/mnt/media/ShowB/S01/E01.mkv",
		},
	}}
	q := &recordingQueuer{}
	sup := &recordingSuppressor{}
	svc := NewService(store, hist, fakeResolver{}, q, sup, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 2 {
		t.Fatalf("expected 2 enqueued targets (distinct paths), got %d: %+v", len(q.enqueued), q.enqueued)
	}
	if len(sup.claimed) != 2 {
		t.Fatalf("expected 2 claimed keys, got %d: %v", len(sup.claimed), sup.claimed)
	}
	if sup.claimed[0] == sup.claimed[1] {
		t.Fatalf("expected distinct claimed keys, both were %q", sup.claimed[0])
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

func (f *fakeStore) GetSource(context.Context, string) (*Source, error) { return nil, nil }

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

type fakeRootFolders struct {
	paths []string
	err   error
}

func (f fakeRootFolders) RootFolders(context.Context, string, string) ([]string, error) {
	return f.paths, f.err
}

type fakeFolderLister struct{ paths []string }

func (f fakeFolderLister) ListFolderPaths(context.Context) ([]string, error) { return f.paths, nil }

type sourceGetterStore struct {
	fakeStore
	src *Source
}

func (s *sourceGetterStore) GetSource(context.Context, string) (*Source, error) { return s.src, nil }

func TestSuggestRewritesService(t *testing.T) {
	store := &sourceGetterStore{src: &Source{IntegrationID: "i1", Kind: "sonarr", BaseURL: "http://x", APIKeyRef: "k"}}
	svc := NewService(store, &fakeHistory{}, fakeResolver{}, &recordingQueuer{}, allowSuppressor{}, nil)
	svc.SetRewriteResolvers(
		fakeRootFolders{paths: []string{"/mnt/happy/storage2/tvshows1", "/data/Movies"}},
		fakeFolderLister{paths: []string{"/mnt/media/happy/storage2/tvshows1"}},
	)
	got, err := svc.SuggestRewrites(context.Background(), "i1")
	if err != nil {
		t.Fatalf("SuggestRewrites: %v", err)
	}
	if len(got.Proposed) != 1 || got.Proposed[0].To != "/mnt/media/happy/storage2/tvshows1" {
		t.Fatalf("proposed=%+v", got.Proposed)
	}
	if len(got.Unmatched) != 1 || got.Unmatched[0] != "/data/Movies" {
		t.Fatalf("unmatched=%+v", got.Unmatched)
	}
}
