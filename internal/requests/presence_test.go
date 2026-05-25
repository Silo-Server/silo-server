package requests

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type fakePresenceLookup struct {
	rows []catalog.ExternalIDMatchRow
	got  []catalog.ExternalIDLookupCandidate
}

func (f *fakePresenceLookup) LookupExternalIDs(_ context.Context, _ string, candidates []catalog.ExternalIDLookupCandidate) ([]catalog.ExternalIDMatchRow, error) {
	f.got = append([]catalog.ExternalIDLookupCandidate(nil), candidates...)
	return f.rows, nil
}

type fakeTMDBBackfiller struct {
	contentID string
	itemType  string
	tmdbID    int
}

func (f *fakeTMDBBackfiller) AttachTMDBID(_ context.Context, contentID, itemType string, tmdbID int) error {
	f.contentID = contentID
	f.itemType = itemType
	f.tmdbID = tmdbID
	return nil
}

func TestCatalogPresenceMatchesByTVDBAndBackfillsTMDB(t *testing.T) {
	tvdbID := 420105
	lookup := &fakePresenceLookup{rows: []catalog.ExternalIDMatchRow{{
		QueryTMDBID:     "201992",
		MediaID:         "120983767174086659",
		MatchedProvider: "tvdb",
		LibraryID:       "2",
		Title:           "The Rookie: Feds",
	}}}
	backfill := &fakeTMDBBackfiller{}
	presence := &CatalogPresence{items: lookup, tmdbBackfill: backfill}

	result, err := presence.Lookup(context.Background(), MediaTypeSeries, []PresenceCandidate{{
		TMDBID: 201992,
		TVDBID: &tvdbID,
		IMDbID: "tt18076310",
	}})
	if err != nil {
		t.Fatalf("Lookup returned error: %v", err)
	}
	if !result[201992].Available {
		t.Fatalf("available = false, want true")
	}
	if result[201992].MatchedProvider != "tvdb" {
		t.Fatalf("matched provider = %q, want tvdb", result[201992].MatchedProvider)
	}
	if backfill.contentID != "120983767174086659" || backfill.itemType != "series" || backfill.tmdbID != 201992 {
		t.Fatalf("backfill = %+v, want content 120983767174086659 series tmdb 201992", backfill)
	}
}

func TestCatalogPresenceKeepsLookupTMDBCompatibility(t *testing.T) {
	lookup := &fakePresenceLookup{rows: []catalog.ExternalIDMatchRow{{
		QueryTMDBID:     "550",
		MediaID:         "movie-1",
		MatchedProvider: "tmdb",
		LibraryID:       "1",
		Title:           "Fight Club",
	}}}
	presence := &CatalogPresence{items: lookup}

	result, err := presence.LookupTMDB(context.Background(), MediaTypeMovie, []int{550})
	if err != nil {
		t.Fatalf("LookupTMDB returned error: %v", err)
	}
	if !result[550] {
		t.Fatalf("result[550] = false, want true")
	}
	if len(lookup.got) != 1 || lookup.got[0].TMDBID != "550" {
		t.Fatalf("lookup candidates = %+v, want tmdb candidate", lookup.got)
	}
}

func TestNewCatalogPresenceIgnoresNilRepositories(t *testing.T) {
	presence := NewCatalogPresence(nil, nil)
	if presence.items != nil {
		t.Fatalf("items = %#v, want nil", presence.items)
	}
	if presence.tmdbBackfill != nil {
		t.Fatalf("tmdbBackfill = %#v, want nil", presence.tmdbBackfill)
	}
}
