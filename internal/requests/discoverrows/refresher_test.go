package discoverrows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

type fakeLayout []Row

func (f fakeLayout) DiscoverLayoutRows(context.Context) ([]Row, error) { return f, nil }

// fakeSnapshots is an in-memory snapshot store with the lease rules that
// matter here: a claim fails while another is live, and a save needs the
// caller's claim.
type fakeSnapshots struct {
	rows      map[string]*Snapshot
	claims    map[string]time.Time
	stealNext bool
	removed   []string
}

func newFakeSnapshots() *fakeSnapshots {
	return &fakeSnapshots{rows: map[string]*Snapshot{}, claims: map[string]time.Time{}}
}

func (f *fakeSnapshots) Get(_ context.Context, key string) (Snapshot, bool, error) {
	s, ok := f.rows[key]
	if !ok {
		return Snapshot{}, false, nil
	}
	return *s, true, nil
}

func (f *fakeSnapshots) TryClaimRefresh(_ context.Context, key string, _ time.Duration) (time.Time, bool, error) {
	if s, ok := f.rows[key]; ok && s.LastStatus == snapshotRefreshing {
		return time.Time{}, false, nil
	}
	at := time.Now()
	if f.rows[key] == nil {
		f.rows[key] = &Snapshot{Key: key}
	}
	f.rows[key].LastStatus, f.rows[key].LastAttemptAt = snapshotRefreshing, &at
	f.claims[key] = at
	return at, true, nil
}

func (f *fakeSnapshots) SaveSuccess(_ context.Context, key string, cards []Card, status string, claimAt, refreshedAt time.Time) error {
	if f.stealNext || !f.claims[key].Equal(claimAt) {
		return ErrLeaseLost
	}
	s := f.rows[key]
	s.Cards, s.LastStatus, s.RefreshedAt = cards, status, &refreshedAt
	return nil
}

func (f *fakeSnapshots) RecordFailure(_ context.Context, key, message string, claimAt time.Time) error {
	if !f.claims[key].Equal(claimAt) {
		return ErrLeaseLost
	}
	f.rows[key].LastStatus, f.rows[key].LastError = snapshotError, message
	return nil
}

func (f *fakeSnapshots) DeleteUnreferenced(_ context.Context, keep []string, _ time.Duration) (int64, error) {
	var n int64
	for key := range f.rows {
		found := false
		for _, k := range keep {
			found = found || k == key
		}
		if !found {
			delete(f.rows, key)
			f.removed = append(f.removed, key)
			n++
		}
	}
	return n, nil
}

type fakeListTMDB struct {
	cardErr map[int]error
	calls   int
}

func (f *fakeListTMDB) GetListMedia(context.Context, int, int) ([]tmdb.MediaResult, error) {
	return nil, errors.New("not used")
}

func (f *fakeListTMDB) MediaCard(_ context.Context, mediaType string, id int) (*tmdb.MediaResult, error) {
	f.calls++
	if err := f.cardErr[id]; err != nil {
		return nil, err
	}
	return &tmdb.MediaResult{ID: id, MediaType: mediaType, Title: fmt.Sprintf("Title %d", id), PosterPath: "/p.jpg"}, nil
}

func (f *fakeListTMDB) FindByExternalIDCached(_ context.Context, _ string, imdb string) ([]tmdb.MediaResult, error) {
	return []tmdb.MediaResult{{ID: 77, MediaType: "series", Title: "Found by " + imdb}, {ID: 78, MediaType: "movie", Title: "Wrong type"}}, nil
}

func TestRefresherKeepsTheLastGoodListAndRetriesOnSchedule(t *testing.T) {
	row := Row{ID: "r_list", Type: TypeMDBList, Title: "List", Enabled: true, List: &ListSpec{URL: "https://mdblist.com/lists/u/l"}}
	key := SourceKey(row)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-13 * time.Hour)
	good := []Card{{ID: 1, MediaType: "movie", Title: "Kept"}}

	tests := []struct {
		name string
		// stored is the snapshot before the run; nil means none yet.
		stored  *Snapshot
		entries []MDBListEntry
		fetch   error
		cardErr map[int]error
		steal   bool
		backOff bool
		want    string
		// wantCards is the stored copy after the run.
		wantCards []Card
		wantCalls int
	}{
		{
			name:    "first fetch, in list order, IMDb fallback picks the entry's type, unknown types, 404s and repeats skipped",
			entries: []MDBListEntry{{ID: "5", MediaType: "movie"}, {MediaType: "show", IMDbID: "tt1"}, {ID: "9", MediaType: "person"}, {ID: "6", MediaType: "show"}, {ID: "5", MediaType: "movie"}},
			cardErr: map[int]error{6: fmt.Errorf("gone: %w", tmdb.ErrNotFound)},
			want:    outcomeRefreshed,
			wantCards: []Card{
				{ID: 5, MediaType: "movie", Title: "Title 5", PosterPath: "/p.jpg"},
				{ID: 77, MediaType: "series", Title: "Found by tt1"},
			},
			// Movie 5 is listed twice and looked up once.
			wantCalls: 2,
		},
		{name: "fresh copy is not fetched", stored: &Snapshot{Cards: good, LastStatus: snapshotOK, RefreshedAt: new(now.Add(-time.Hour))}, want: outcomeFresh, wantCards: good},
		{name: "download failure keeps the copy", stored: &Snapshot{Cards: good, LastStatus: snapshotOK, RefreshedAt: &stale}, fetch: errors.New("mdblist down"), want: outcomeFailed, wantCards: good},
		{name: "card failure other than 404 keeps the copy", stored: &Snapshot{Cards: good, LastStatus: snapshotOK, RefreshedAt: &stale}, entries: []MDBListEntry{{ID: "5", MediaType: "movie"}}, cardErr: map[int]error{5: errors.New("tmdb 500")}, want: outcomeFailed, wantCards: good, wantCalls: 1},
		{name: "empty answer over a list we have is a glitch", stored: &Snapshot{Cards: good, LastStatus: snapshotOK, RefreshedAt: &stale}, want: outcomeFailed, wantCards: good},
		{name: "empty first answer is stored as empty", want: outcomeRefreshed},
		{name: "a lost lease saves nothing", stored: &Snapshot{Cards: good, LastStatus: snapshotOK, RefreshedAt: &stale}, entries: []MDBListEntry{{ID: "5", MediaType: "movie"}}, steal: true, want: outcomeSkipped, wantCards: good, wantCalls: 1},
		{name: "recent failure waits for the retry spacing", stored: &Snapshot{Cards: good, LastStatus: snapshotError, RefreshedAt: &stale, LastAttemptAt: new(now.Add(-10 * time.Minute))}, backOff: true, want: outcomeSkipped, wantCards: good},
		{name: "a save retries a recent failure", stored: &Snapshot{Cards: good, LastStatus: snapshotError, RefreshedAt: &stale, LastAttemptAt: new(now.Add(-10 * time.Minute))}, entries: []MDBListEntry{{ID: "5", MediaType: "movie"}}, want: outcomeRefreshed, wantCards: []Card{{ID: 5, MediaType: "movie", Title: "Title 5", PosterPath: "/p.jpg"}}, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshots := newFakeSnapshots()
			if tt.stored != nil {
				stored := *tt.stored
				snapshots.rows[key] = &stored
			}
			snapshots.stealNext = tt.steal
			client := &fakeListTMDB{cardErr: tt.cardErr}
			r := &Refresher{
				Layout: fakeLayout{row}, Snapshots: snapshots, TMDB: client,
				FetchMDBList: func(context.Context, string, int) ([]MDBListEntry, error) { return tt.entries, tt.fetch },
				Now:          func() time.Time { return now },
				logger:       discardLogger(),
			}
			if got := r.refresh(t.Context(), key, row, tt.backOff); got != tt.want {
				t.Fatalf("outcome = %s, want %s", got, tt.want)
			}
			got, _, _ := snapshots.Get(t.Context(), key)
			if fmt.Sprint(got.Cards) != fmt.Sprint(tt.wantCards) {
				t.Fatalf("stored cards = %+v, want %+v", got.Cards, tt.wantCards)
			}
			if client.calls != tt.wantCalls {
				t.Fatalf("card lookups = %d, want %d", client.calls, tt.wantCalls)
			}
		})
	}
}

func TestRefresherRunOnceSharesAListAndDropsUnusedOnes(t *testing.T) {
	list := func(id string, enabled bool) Row {
		return Row{ID: id, Type: TypeMDBList, Title: id, Enabled: enabled, List: &ListSpec{URL: "https://mdblist.com/lists/u/shared"}}
	}
	disabled := Row{ID: "r_off", Type: TypeTMDBList, Title: "Off", List: &ListSpec{URL: "https://www.themoviedb.org/list/42"}}
	snapshots := newFakeSnapshots()
	snapshots.rows["tmdb_list:42"] = &Snapshot{Key: "tmdb_list:42", LastStatus: snapshotOK}
	snapshots.rows["mdblist:https://mdblist.com/lists/u/gone/json"] = &Snapshot{LastStatus: snapshotOK}
	fetches := 0
	r := &Refresher{
		Layout:    fakeLayout{list("r_a", true), list("r_b", true), disabled},
		Snapshots: snapshots, TMDB: &fakeListTMDB{},
		FetchMDBList: func(context.Context, string, int) ([]MDBListEntry, error) {
			fetches++
			return []MDBListEntry{{ID: "1", MediaType: "movie"}}, nil
		},
		Now: time.Now, logger: discardLogger(),
	}
	raw, err := r.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var result RefreshResult
	_ = json.Unmarshal(raw, &result)
	if fetches != 1 || result.Lists != 1 || result.Refreshed != 1 || result.Removed != 1 {
		t.Fatalf("fetches %d, result %+v: want one shared fetch and one removal", fetches, result)
	}
	if _, kept := snapshots.rows["tmdb_list:42"]; !kept {
		t.Fatal("a disabled row's list was removed")
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
