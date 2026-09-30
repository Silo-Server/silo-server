package requests

import "testing"

// The request's source survives the store, and an unset source is direct.
func TestRequestSourceRoundTripDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	ctx := t.Context()
	direct := insertLifecycleRequest(t, repo, "direct", 1, 101, StatusPending)
	if direct.Source != SourceDirect {
		t.Fatalf("unset source = %q, want direct", direct.Source)
	}
	created, err := repo.CreateRequest(ctx, CreateRequestRecord{
		ID:        "watchlist",
		Input:     CreateRequestInput{MediaType: MediaTypeMovie, TMDBID: 102, Title: "Heat", Source: SourceWatchlist},
		Status:    StatusPending,
		Outcome:   OutcomeActive,
		Requester: Viewer{UserID: 1, ProfileID: "profile"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Source != SourceWatchlist {
		t.Fatalf("created source = %q, want watchlist", created.Source)
	}
	active, err := repo.ListActiveByTMDB(ctx, MediaTypeMovie, []int{102})
	if err != nil || active[102] == nil || active[102].Source != SourceWatchlist {
		t.Fatalf("ListActiveByTMDB = %+v, %v; want the watchlist source", active[102], err)
	}
}

// The v1 settings write has no watchlist_requests field and keeps the stored
// value; the v2 conditional write sets it.
func TestWatchlistRequestsSettingDatabase(t *testing.T) {
	repo := editorTestRepository(t)
	ctx := t.Context()
	settings, err := repo.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.WatchlistRequests {
		t.Fatal("watchlist requests default off, want on")
	}
	settings.WatchlistRequests = false
	if settings, err = repo.UpdateSettingsConditional(ctx, settings, settings.Revision); err != nil {
		t.Fatalf("conditional update: %v", err)
	}
	if settings.WatchlistRequests {
		t.Fatal("conditional update did not store watchlist_requests=false")
	}
	settings.RequestsEnabled = true
	settings.WatchlistRequests = true // v1 decodes no such field; the write must ignore it
	if _, err = repo.UpdateSettings(ctx, settings); err != nil {
		t.Fatalf("v1 update: %v", err)
	}
	stored, err := repo.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.WatchlistRequests || !stored.RequestsEnabled {
		t.Fatalf("after the v1 write: %+v, want requests enabled and watchlist requests still off", stored)
	}
}
