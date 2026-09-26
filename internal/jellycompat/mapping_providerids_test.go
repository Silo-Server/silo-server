package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

func assertProviderIDs(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ProviderIds = %v, want %v", got, want)
	}
	for key, wantValue := range want {
		gotValue, ok := got[key]
		if !ok {
			t.Fatalf("ProviderIds missing %q: got %v", key, got)
		}
		if gotValue != wantValue {
			t.Fatalf("ProviderIds[%q] = %q, want %q", key, gotValue, wantValue)
		}
	}
}

func TestItemFromListPopulatesProviderIDsWhenRequested(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	dto := m.itemFromList(upstreamListItem{
		ContentID: "movie-1",
		Type:      "movie",
		Title:     "The Hobbit: The Battle of the Five Armies",
		ImdbID:    "tt1170358",
		TmdbID:    "122917",
		TvdbID:    "279121",
	}, false, nil, map[string]bool{"providerids": true})

	assertProviderIDs(t, dto.ProviderIDs, map[string]string{
		"Imdb": "tt1170358",
		"Tmdb": "122917",
		"Tvdb": "279121",
	})
}

// Fields is opt-in: a client that did not ask for ProviderIds must not start
// receiving them, otherwise every list response grows for no reason.
func TestItemFromListOmitsProviderIDsWhenNotRequested(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	dto := m.itemFromList(upstreamListItem{
		ContentID: "movie-1",
		Type:      "movie",
		Title:     "The Hobbit: The Battle of the Five Armies",
		ImdbID:    "tt1170358",
		TmdbID:    "122917",
	}, false, nil, map[string]bool{"overview": true})

	if dto.ProviderIDs != nil {
		t.Fatalf("ProviderIds = %v, want nil when the field was not requested", dto.ProviderIDs)
	}
}

func TestItemFromDetailPopulatesProviderIDs(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	dto := m.itemFromDetail(upstreamItemDetail{
		ContentID: "series-1",
		Type:      "series",
		Title:     "Snowpiercer",
		ImdbID:    "tt6156584",
		TmdbID:    "79680",
		TvdbID:    "328487",
	}, false, nil)

	assertProviderIDs(t, dto.ProviderIDs, map[string]string{
		"Imdb": "tt6156584",
		"Tmdb": "79680",
		"Tvdb": "328487",
	})
}

func TestEpisodeFromUpstreamPopulatesProviderIDs(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	dto := m.episodeFromUpstream(upstreamEpisode{
		ContentID:     "episode-1",
		SeasonNumber:  1,
		EpisodeNumber: 3,
		Title:         "Access Is Power",
		ImdbID:        "tt9119530",
		TmdbID:        "2137581",
	}, false, nil)

	assertProviderIDs(t, dto.ProviderIDs, map[string]string{
		"Imdb": "tt9119530",
		"Tmdb": "2137581",
	})
}

// media_items.tmdb_id holds TMDB's "id-slug" URL form often enough that the raw
// column value is not a usable provider id. See catalog.NormalizeTMDBID.
func TestProviderIDsStripTMDBSlug(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	dto := m.itemFromDetail(upstreamItemDetail{
		ContentID: "series-2",
		Type:      "series",
		Title:     "Disney's Adventures of the Gummi Bears",
		TmdbID:    "1931-disney-s-adventures-of-the-gummi-bears",
	}, false, nil)

	assertProviderIDs(t, dto.ProviderIDs, map[string]string{"Tmdb": "1931"})
}

// An absent id must be absent from the map, not present as "". Clients treat a
// present key as a real id and will look it up.
func TestProviderIDsOmitEmptyIdentifiers(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	dto := m.itemFromDetail(upstreamItemDetail{
		ContentID: "movie-2",
		Type:      "movie",
		Title:     "Unmatched Movie",
		ImdbID:    "  ",
	}, false, nil)

	if len(dto.ProviderIDs) != 0 {
		t.Fatalf("ProviderIds = %v, want empty", dto.ProviderIDs)
	}
}

func TestMediaItemToListItemCarriesExternalIDs(t *testing.T) {
	item := mediaItemToListItem(&models.MediaItem{
		ContentID: "movie-1",
		Type:      "movie",
		Title:     "The Hobbit: The Battle of the Five Armies",
		ImdbID:    "tt1170358",
		TmdbID:    "122917",
		TvdbID:    "279121",
	})

	if item.ImdbID != "tt1170358" || item.TmdbID != "122917" || item.TvdbID != "279121" {
		t.Fatalf("external ids not carried: imdb=%q tmdb=%q tvdb=%q", item.ImdbID, item.TmdbID, item.TvdbID)
	}
}

func TestModelEpisodeToUpstreamCarriesExternalIDs(t *testing.T) {
	ep := modelEpisodeToUpstream(&models.Episode{
		ContentID:     "episode-1",
		SeasonNumber:  1,
		EpisodeNumber: 3,
		Title:         "Access Is Power",
		ImdbID:        "tt9119530",
		TmdbID:        "2137581",
		TvdbID:        "7563411",
	}, "series-1")

	if ep.ImdbID != "tt9119530" || ep.TmdbID != "2137581" || ep.TvdbID != "7563411" {
		t.Fatalf("external ids not carried: imdb=%q tmdb=%q tvdb=%q", ep.ImdbID, ep.TmdbID, ep.TvdbID)
	}
}
