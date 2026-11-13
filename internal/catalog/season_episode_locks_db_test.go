package catalog

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	lockTestEditedTitle    = "Edited title"
	lockTestEditedOverview = "Edited overview"
	lockTestEditedAirDate  = "2001-02-03"
)

var lockTestProviderDate = time.Date(2020, 5, 6, 0, 0, 0, 0, time.UTC)

func (f *artworkLockFixture) providerSeason() *models.Season {
	return &models.Season{ContentID: f.season.ContentID, SeriesID: f.seriesID, SeasonNumber: 1, Title: "Provider season", Overview: "Provider overview", AirDate: &lockTestProviderDate}
}

func (f *artworkLockFixture) providerEpisode() *models.Episode {
	return &models.Episode{ContentID: f.episode.ContentID, SeriesID: f.seriesID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Provider episode", Overview: "Provider overview", AirDate: &lockTestProviderDate}
}

func lockTestEdit(locks []int) *MetadataUpdate {
	title, overview, airDate := lockTestEditedTitle, lockTestEditedOverview, lockTestEditedAirDate
	return &MetadataUpdate{Title: &title, Overview: &overview, AirDate: &airDate, LockedFields: &locks}
}

func (f *artworkLockFixture) edit(t *testing.T, locks []int) {
	t.Helper()
	if err := f.seasons.UpdateMetadata(context.Background(), f.season.ContentID, lockTestEdit(locks)); err != nil {
		t.Fatal(err)
	}
	if err := f.episodes.UpdateMetadata(context.Background(), f.episode.ContentID, lockTestEdit(locks)); err != nil {
		t.Fatal(err)
	}
}

func hasLockTestEdits(title, overview string, airDate *time.Time) bool {
	return title == lockTestEditedTitle && overview == lockTestEditedOverview &&
		airDate != nil && airDate.Format("2006-01-02") == lockTestEditedAirDate
}

// A refresh writes through the season and episode upserts with whatever it read
// earlier, so a field locked at any point before the write keeps its value, and
// an unlocked field on the same row still takes the provider's.
func TestSeasonEpisodeLockedFieldsSurviveUpsertsDB(t *testing.T) {
	ctx := context.Background()
	locks := []int{fieldNameLocked, fieldOverviewLocked, fieldReleaseDatesLocked}
	for _, tc := range []struct {
		name  string
		write func(f *artworkLockFixture) error
	}{
		{"single upsert", func(f *artworkLockFixture) error {
			return errors.Join(f.seasons.Upsert(ctx, f.providerSeason()), f.episodes.Upsert(ctx, f.providerEpisode()))
		}},
		{"bulk upsert", func(f *artworkLockFixture) error {
			return errors.Join(
				f.seasons.BulkUpsert(ctx, []*models.Season{f.providerSeason()}),
				f.episodes.BulkUpsert(ctx, f.seriesID, []*models.Episode{f.providerEpisode()}),
			)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newArtworkLockFixture(t, "locks")
			f.edit(t, locks)
			if err := tc.write(f); err != nil {
				t.Fatal(err)
			}
			_, season, episode := f.read(t)
			if !hasLockTestEdits(season.Title, season.Overview, season.AirDate) {
				t.Errorf("season = %q, %q, %v, want the locked edits", season.Title, season.Overview, season.AirDate)
			}
			if !hasLockTestEdits(episode.Title, episode.Overview, episode.AirDate) {
				t.Errorf("episode = %q, %q, %v, want the locked edits", episode.Title, episode.Overview, episode.AirDate)
			}
			if !slices.Equal(season.LockedFields, locks) || !slices.Equal(episode.LockedFields, locks) {
				t.Errorf("locks = %v, %v, want %v", season.LockedFields, episode.LockedFields, locks)
			}
		})
	}

	t.Run("an unlocked field takes the provider value", func(t *testing.T) {
		f := newArtworkLockFixture(t, "locks-partial")
		f.edit(t, []int{fieldNameLocked})
		if err := f.episodes.Upsert(ctx, f.providerEpisode()); err != nil {
			t.Fatal(err)
		}
		_, _, episode := f.read(t)
		if episode.Title != lockTestEditedTitle || episode.Overview != "Provider overview" || !episode.AirDate.Equal(lockTestProviderDate) {
			t.Errorf("episode = %q, %q, %v, want only the title kept", episode.Title, episode.Overview, episode.AirDate)
		}
	})

	t.Run("an episode's runtime can lock", func(t *testing.T) {
		f := newArtworkLockFixture(t, "locks-runtime")
		runtime, locks := 77, []int{fieldRuntimeLocked}
		if err := f.episodes.UpdateMetadata(ctx, f.episode.ContentID, &MetadataUpdate{Runtime: &runtime, LockedFields: &locks}); err != nil {
			t.Fatal(err)
		}
		if err := f.episodes.BulkUpsert(ctx, f.seriesID, []*models.Episode{f.providerEpisode()}); err != nil {
			t.Fatal(err)
		}
		_, _, episode := f.read(t)
		if episode.Runtime != runtime || !slices.Equal(episode.LockedFields, locks) {
			t.Errorf("episode runtime = %d with locks %v, want %d locked", episode.Runtime, episode.LockedFields, runtime)
		}
	})

	t.Run("details report the row's own locks", func(t *testing.T) {
		f := newArtworkLockFixture(t, "locks-detail")
		f.edit(t, []int{fieldNameLocked})
		for _, id := range []string{f.season.ContentID, f.episode.ContentID} {
			detail, err := f.details.GetItemDetail(ctx, id, AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(detail.LockedFields, []int{fieldNameLocked}) {
				t.Errorf("%s locks = %v, want the name lock", id, detail.LockedFields)
			}
		}
	})
}

// Artwork locks on the series, and a season has no runtime, so either lock on
// these rows would count without guarding anything.
func TestLockableFieldsKeepOnlyWhatARowCanHonor(t *testing.T) {
	requested := []int{fieldNameLocked, fieldRuntimeLocked, fieldImagesLocked}
	if got := lockableFields(requested, seasonLockedColumns); !slices.Equal(got, []int{fieldNameLocked}) {
		t.Errorf("season keeps %v, want only the name lock", got)
	}
	if got := lockableFields(requested, episodeLockedColumns); !slices.Equal(got, []int{fieldNameLocked, fieldRuntimeLocked}) {
		t.Errorf("episode keeps %v, want the name and runtime locks", got)
	}
}
