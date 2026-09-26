package trakt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

func TestFetchHistoryImportsEveryPage(t *testing.T) {
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/history" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		if r.URL.Query().Get("limit") != "250" {
			t.Errorf("limit = %q, want 250", r.URL.Query().Get("limit"))
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		// Short pages without pagination headers: only the empty page ends the history.
		switch page {
		case "1", "2":
			writeTraktFixture(t, w, `[
				{"type":"movie","watched_at":"2026-05-0%sT12:00:00.000Z","movie":{"title":"Movie %s","year":2020,"ids":{"tmdb":10%s}}},
				{"type":"episode","watched_at":"2026-06-0%sT12:00:00.000Z","episode":{"season":1,"number":%s,"ids":{"tvdb":50%s}},"show":{"title":"Show","year":2021,"ids":{"tvdb":300}}}
			]`, page, page, page, page, page, page)
		case "3":
			writeTraktFixture(t, w, `[]`)
		default:
			t.Errorf("unexpected page %q", page)
			http.Error(w, "unexpected page", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	rows, err := NewProvider(server.Client(), server.URL).FetchHistory(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1", "2", "3", "1", "2", "3"}; !reflect.DeepEqual(pages, want) {
		t.Fatalf("pages = %v, want %v", pages, want)
	}
	want := []struct {
		key, kind string
		watchedAt time.Time
	}{
		{"tmdb:101", historyimport.KindMovie, time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)},
		{"tvdb:501", historyimport.KindEpisode, time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)},
		{"tmdb:102", historyimport.KindMovie, time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)},
		{"tvdb:502", historyimport.KindEpisode, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %#v", len(rows), len(want), rows)
	}
	for i, exp := range want {
		row := rows[i]
		if row.ProviderItemKey != exp.key || row.Kind != exp.kind || !row.WatchedAt.Equal(exp.watchedAt) {
			t.Errorf("row %d = %#v, want key %s kind %s watched %s", i, row, exp.key, exp.kind, exp.watchedAt)
		}
	}
}

func TestFetchHistoryDoesNotReturnPartialHistoryOnLaterPageFailure(t *testing.T) {
	for _, failure := range []string{"http", "json"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("page") == "1" {
					writeTraktFixture(t, w, `[{"type":"movie","watched_at":"2026-05-01T12:00:00.000Z","movie":{"ids":{"tmdb":123}}}]`)
					return
				}
				if failure == "http" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
				} else {
					writeTraktFixture(t, w, `[{`)
				}
			}))
			defer server.Close()

			rows, err := NewProvider(server.Client(), server.URL).FetchHistory(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{})
			if err == nil || rows != nil {
				t.Fatalf("got rows=%#v, err=%v; want no partial history and an error", rows, err)
			}
		})
	}
}

// TestExportWatchedDoesNotResendPlayFromLaterHistoryPage runs the real export
// reconciliation against a Trakt history that spans two pages. A play Trakt
// lists only on page 2 must count as present remotely, not be sent again.
func TestExportWatchedDoesNotResendPlayFromLaterHistoryPage(t *testing.T) {
	var sent traktHistoryPayload
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sync/history":
			page := r.URL.Query().Get("page")
			if page == "" {
				page = "1" // Trakt serves only the first page when page is omitted.
			}
			w.Header().Set("X-Pagination-Page", page)
			w.Header().Set("X-Pagination-Page-Count", "2")
			switch page {
			case "1":
				writeTraktFixture(t, w, `[{"type":"movie","watched_at":"2026-05-01T12:00:00.000Z","movie":{"ids":{"tmdb":101}}}]`)
			case "2":
				writeTraktFixture(t, w, `[{"type":"movie","watched_at":"2026-05-02T12:00:00.000Z","movie":{"ids":{"tmdb":102}}}]`)
			default:
				t.Errorf("unexpected history page %q", page)
				http.Error(w, "unexpected page", http.StatusInternalServerError)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/sync/history":
			posts++
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("decode export body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			writeTraktFixture(t, w, `{}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := completedHistoryStore{rows: []userstore.WatchHistoryEntry{
		localMoviePlay("history-1", "101", "2026-05-01T12:00:00Z"), // remote page 1
		localMoviePlay("history-2", "102", "2026-05-02T12:00:00Z"), // remote page 2
		localMoviePlay("history-3", "103", "2026-05-03T12:00:00Z"), // not on Trakt
	}}
	repo := &historyExportRepo{}
	service := watchsync.NewService(repo, watchsync.NewRegistry()).WithUserStoreProvider(staticUserStores{store: store})
	conn := watchsync.Connection{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1", AccessToken: "test-token"}

	result, err := service.ExportWatched(context.Background(), conn, watchsync.ServerConfig{}, NewProvider(server.Client(), server.URL))
	if err != nil {
		t.Fatalf("ExportWatched: %v", err)
	}
	if result.RemoteFound != 2 || result.RemotePresent != 2 || result.Queued != 1 || result.Sent != 1 {
		t.Fatalf("result = %+v, want 2 remote plays found and present, 1 queued and sent", result)
	}
	if posts != 1 || len(sent.Movies) != 1 || sent.Movies[0].IDs.TMDB != 103 || len(sent.Episodes) != 0 || len(sent.Shows) != 0 {
		t.Fatalf("posts = %d, sent = %+v; want one export of tmdb 103 only", posts, sent)
	}
	wantStatus := map[string]string{"history-1": "remote_present", "history-2": "remote_present", "history-3": "sent"}
	gotStatus := map[string]string{}
	for _, export := range repo.exports {
		gotStatus[export.HistoryID] = export.Status
	}
	if !reflect.DeepEqual(gotStatus, wantStatus) {
		t.Fatalf("export statuses = %v, want %v", gotStatus, wantStatus)
	}
}

func localMoviePlay(id, tmdbID, watchedAt string) userstore.WatchHistoryEntry {
	return userstore.WatchHistoryEntry{
		ID:              id,
		ProfileID:       "profile-1",
		MediaItemID:     "movie-" + tmdbID,
		WatchedAt:       watchedAt,
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourcePlayback,
		Identity: userstore.WatchIdentity{
			StableType:  historyimport.KindMovie,
			ProviderIDs: map[string]string{"tmdb": tmdbID},
		},
	}
}

// completedHistoryStore serves the completed history ExportWatched reads. Any
// other UserStore method panics through the nil embedded interface.
type completedHistoryStore struct {
	userstore.UserStore
	rows []userstore.WatchHistoryEntry
}

func (s completedHistoryStore) ListCompletedHistory(_ context.Context, query userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error) {
	if query.Offset > 0 {
		return nil, nil
	}
	return s.rows, nil
}

type staticUserStores struct {
	store userstore.UserStore
}

func (p staticUserStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (staticUserStores) Close() error { return nil }

// historyExportRepo keeps the history export rows of one ExportWatched run.
// Any other Repository method panics through the nil embedded interface.
type historyExportRepo struct {
	watchsync.Repository
	exports []watchsync.HistoryExport
}

func (r *historyExportRepo) UpsertHistoryExports(_ context.Context, exports []watchsync.HistoryExport) error {
	for _, export := range exports {
		export.ID = "export-" + export.HistoryID
		r.exports = append(r.exports, export)
	}
	return nil
}

func (r *historyExportRepo) ListPendingHistoryExports(_ context.Context, connectionID string, limit int) ([]watchsync.HistoryExport, error) {
	var pending []watchsync.HistoryExport
	for _, export := range r.exports {
		if export.ConnectionID == connectionID && export.Status == "pending" && len(pending) < limit {
			pending = append(pending, export)
		}
	}
	return pending, nil
}

func (r *historyExportRepo) MarkHistoryExportStatus(_ context.Context, id string, status string, _ string) error {
	for i := range r.exports {
		if r.exports[i].ID == id {
			r.exports[i].Status = status
		}
	}
	return nil
}

func (r *historyExportRepo) UpsertConnection(_ context.Context, conn watchsync.Connection) (watchsync.Connection, error) {
	return conn, nil
}
