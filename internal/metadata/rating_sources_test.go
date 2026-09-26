package metadata

import (
	"context"
	"math"
	"reflect"
	"sync"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
)

func ratingsStructWithSources(t *testing.T, sources map[string]*structpb.Value) *structpb.Struct {
	t.Helper()
	return &structpb.Struct{Fields: map[string]*structpb.Value{
		"imdb":    structpb.NewNumberValue(8.1),
		"sources": structpb.NewStructValue(&structpb.Struct{Fields: sources}),
	}}
}

func sourceEntry(fields map[string]*structpb.Value) *structpb.Value {
	return structpb.NewStructValue(&structpb.Struct{Fields: fields})
}

func TestRatingSourcesFromStruct(t *testing.T) {
	number := structpb.NewNumberValue
	ratings := ratingsStructWithSources(t, map[string]*structpb.Value{
		"imdb":       sourceEntry(map[string]*structpb.Value{"score": number(81), "votes": number(673852)}),
		"Metacritic": sourceEntry(map[string]*structpb.Value{"score": number(87.5), "votes": number(21)}),
		"rogerebert": sourceEntry(map[string]*structpb.Value{"score": number(100)}),
		"mdblist":    sourceEntry(map[string]*structpb.Value{"score": number(0)}),
		// Dropped sources.
		"netflix":         sourceEntry(map[string]*structpb.Value{"score": number(50)}),
		"tmdb":            sourceEntry(map[string]*structpb.Value{"score": number(101)}),
		"rt_critic":       sourceEntry(map[string]*structpb.Value{"score": number(-1)}),
		"rt_audience":     sourceEntry(map[string]*structpb.Value{"score": number(math.NaN())}),
		"trakt":           sourceEntry(map[string]*structpb.Value{"score": number(math.Inf(1))}),
		"letterboxd":      sourceEntry(map[string]*structpb.Value{"score": structpb.NewStringValue("80")}),
		"myanimelist":     sourceEntry(map[string]*structpb.Value{"votes": number(10)}),
		"metacritic_user": number(75),
	})

	got := ratingSourcesFromStruct(ratings, "mdblist")
	want := map[string]RatingSource{
		models.RatingSourceIMDB:       {Score: 81, Votes: 673852, Provider: "mdblist"},
		models.RatingSourceMetacritic: {Score: 87.5, Votes: 21, Provider: "mdblist"},
		models.RatingSourceRogerEbert: {Score: 100, Provider: "mdblist"},
		models.RatingSourceMDBList:    {Score: 0, Provider: "mdblist"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ratingSourcesFromStruct() = %+v, want %+v", got, want)
	}

	// The flat keys still map to the typed columns; "sources" is not one.
	if flat := ratingsFromStruct(ratings); flat.IMDB != 8.1 || flat.TMDB != 0 {
		t.Fatalf("ratingsFromStruct() = %+v, want only IMDB 8.1", flat)
	}
}

func TestRatingSourcesFromStructDropsMalformedVotesOnly(t *testing.T) {
	number := structpb.NewNumberValue
	cases := map[string]*structpb.Value{
		"negative":   number(-5),
		"fractional": number(12.5),
		"string":     structpb.NewStringValue("12"),
		"huge":       number(math.Inf(1)),
		"nan":        number(math.NaN()),
	}
	for name, votes := range cases {
		t.Run(name, func(t *testing.T) {
			ratings := ratingsStructWithSources(t, map[string]*structpb.Value{
				"imdb": sourceEntry(map[string]*structpb.Value{"score": number(70), "votes": votes}),
			})
			got := ratingSourcesFromStruct(ratings, "mdblist")
			want := map[string]RatingSource{models.RatingSourceIMDB: {Score: 70, Provider: "mdblist"}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ratingSourcesFromStruct() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestRatingSourcesFromStructWithoutSources(t *testing.T) {
	cases := map[string]*structpb.Struct{
		"nil struct":     nil,
		"flat keys only": {Fields: map[string]*structpb.Value{"imdb": structpb.NewNumberValue(7)}},
		"sources not an object": {Fields: map[string]*structpb.Value{
			"sources": structpb.NewListValue(&structpb.ListValue{}),
		}},
		"no valid source": {Fields: map[string]*structpb.Value{
			"sources": sourceEntry(map[string]*structpb.Value{"unknown": sourceEntry(map[string]*structpb.Value{"score": structpb.NewNumberValue(5)})}),
		}},
	}
	for name, ratings := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ratingSourcesFromStruct(ratings, "mdblist"); got != nil {
				t.Fatalf("ratingSourcesFromStruct() = %+v, want nil", got)
			}
		})
	}
}

func TestPluginProviderGetMetadata_MapsRatingSources(t *testing.T) {
	number := structpb.NewNumberValue
	client := &fakePluginMetadataClient{
		response: &pluginv1.GetMetadataResponse{Item: &pluginv1.MetadataItem{
			ItemType: "movie",
			Ratings: ratingsStructWithSources(t, map[string]*structpb.Value{
				"letterboxd": sourceEntry(map[string]*structpb.Value{"score": number(80), "votes": number(876082)}),
			}),
		}},
	}
	provider, err := NewPluginProviderWithClientFactory(map[string]string{
		pluginInstallationIDSetting: "1",
		capabilityIDSetting:         "mdblist",
	}, func(context.Context, int, string) (pluginMetadataClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory() error = %v", err)
	}
	provider.lookupProviderIDs = []string{"imdb", "tmdb"}

	result, err := provider.GetMetadata(context.Background(), MetadataRequest{
		ProviderIDs: map[string]string{"tmdb": "578"},
		ContentType: "movie",
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	want := map[string]RatingSource{models.RatingSourceLetterboxd: {Score: 80, Votes: 876082, Provider: "mdblist"}}
	if result == nil || !reflect.DeepEqual(result.RatingSources, want) {
		t.Fatalf("RatingSources = %+v, want %+v", result, want)
	}
	if result.Ratings.IMDB != 8.1 {
		t.Fatalf("Ratings.IMDB = %v, want 8.1", result.Ratings.IMDB)
	}
}

func TestMergeRatingSources(t *testing.T) {
	stored := func() *MetadataResult {
		return &MetadataResult{RatingSources: map[string]RatingSource{
			models.RatingSourceIMDB:       {Score: 70, Votes: 100, Provider: "mdblist"},
			models.RatingSourceMetacritic: {Score: 60, Provider: "mdblist"},
		}}
	}
	incoming := &MetadataResult{RatingSources: map[string]RatingSource{
		models.RatingSourceIMDB:       {Score: 72, Votes: 150, Provider: "other"},
		models.RatingSourceLetterboxd: {Score: 80, Provider: "other"},
	}}

	t.Run("fill empty keeps stored sources and adds new ones", func(t *testing.T) {
		target := stored()
		MergeMetadata(incoming, target, nil, MergeFillEmpty)
		want := map[string]RatingSource{
			models.RatingSourceIMDB:       {Score: 70, Votes: 100, Provider: "mdblist"},
			models.RatingSourceMetacritic: {Score: 60, Provider: "mdblist"},
			models.RatingSourceLetterboxd: {Score: 80, Provider: "other"},
		}
		if !reflect.DeepEqual(target.RatingSources, want) {
			t.Fatalf("RatingSources = %+v, want %+v", target.RatingSources, want)
		}
	})

	t.Run("replace unlocked overwrites reported sources only", func(t *testing.T) {
		target := stored()
		MergeMetadata(incoming, target, nil, MergeReplaceUnlocked)
		want := map[string]RatingSource{
			models.RatingSourceIMDB:       {Score: 72, Votes: 150, Provider: "other"},
			models.RatingSourceMetacritic: {Score: 60, Provider: "mdblist"},
			models.RatingSourceLetterboxd: {Score: 80, Provider: "other"},
		}
		if !reflect.DeepEqual(target.RatingSources, want) {
			t.Fatalf("RatingSources = %+v, want %+v", target.RatingSources, want)
		}
	})

	t.Run("rating lock blocks every mode", func(t *testing.T) {
		for _, mode := range []MergeMode{MergeFillEmpty, MergeReplaceUnlocked} {
			target := stored()
			MergeMetadata(incoming, target, []MetadataField{FieldRating}, mode)
			if !reflect.DeepEqual(target.RatingSources, stored().RatingSources) {
				t.Fatalf("mode %v: locked RatingSources = %+v", mode, target.RatingSources)
			}
			global := stored()
			MergeGlobalMetadata(incoming, global, []MetadataField{FieldRating}, mode)
			if !reflect.DeepEqual(global.RatingSources, stored().RatingSources) {
				t.Fatalf("mode %v: locked global RatingSources = %+v", mode, global.RatingSources)
			}
		}
	})

	t.Run("merging into an empty target does not alias the source map", func(t *testing.T) {
		target := &MetadataResult{}
		MergeMetadata(incoming, target, nil, MergeFillEmpty)
		target.RatingSources[models.RatingSourceTrakt] = RatingSource{Score: 1}
		if _, leaked := incoming.RatingSources[models.RatingSourceTrakt]; leaked {
			t.Fatal("merge aliased the incoming map")
		}
	})
}

// ratingSourceUpsert records one write: an Upsert with its replace flag, or a
// Replace of the whole set (wholeSet).
type ratingSourceUpsert struct {
	contentID string
	sources   []models.ItemRatingSource
	replace   bool
	wholeSet  bool
}

type fakeRatingSourceRepo struct {
	mu      sync.Mutex
	upserts []ratingSourceUpsert
}

func (r *fakeRatingSourceRepo) Upsert(_ context.Context, contentID string, sources []models.ItemRatingSource, replace bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upserts = append(r.upserts, ratingSourceUpsert{contentID: contentID, sources: sources, replace: replace})
	return nil
}

func (r *fakeRatingSourceRepo) Replace(_ context.Context, contentID string, sources []models.ItemRatingSource) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upserts = append(r.upserts, ratingSourceUpsert{contentID: contentID, sources: sources, wholeSet: true})
	return nil
}

func TestRefreshPersistsRatingSources(t *testing.T) {
	const contentID = "movie:tmdb:100"
	votes := int64(500)
	tmdb := &remoteStubProvider{
		slug: "tmdb",
		metadata: &MetadataResult{
			HasMetadata: true, Title: "Title", ProviderIDs: map[string]string{"tmdb": "100"},
			RatingSources: map[string]RatingSource{models.RatingSourceTMDB: {Score: 76, Votes: 500, Provider: "tmdb"}},
		},
	}
	mdblist := &remoteStubProvider{
		slug: "mdblist",
		metadata: &MetadataResult{
			HasMetadata: true,
			RatingSources: map[string]RatingSource{
				models.RatingSourceTMDB:       {Score: 70, Provider: "mdblist"},
				models.RatingSourceMetacritic: {Score: 87, Provider: "mdblist"},
			},
		},
	}
	// The first provider in the chain wins a source both report.
	wantSources := []models.ItemRatingSource{
		{ContentID: contentID, Source: models.RatingSourceTMDB, Score: 76, Votes: &votes, Provider: "tmdb"},
		{ContentID: contentID, Source: models.RatingSourceMetacritic, Score: 87, Provider: "mdblist"},
	}

	cases := []struct {
		name         string
		mode         RefreshMode
		locked       []int
		wantUpsert   bool
		wantReplace  bool
		wantWholeSet bool
	}{
		{name: "scheduled refresh fills empty", mode: ModeScheduledRefresh, wantUpsert: true},
		{name: "manual refresh replaces", mode: ModeManualRefresh, wantUpsert: true, wantReplace: true},
		{name: "identify replaces the whole set", mode: ModeIdentify, wantUpsert: true, wantWholeSet: true},
		{name: "rating lock skips the write", mode: ModeManualRefresh, locked: []int{int(FieldRating)}},
		{name: "rating lock skips identify", mode: ModeIdentify, locked: []int{int(FieldRating)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			repo := &fakeRatingSourceRepo{}
			h.service.ratingSourceRepo = repo
			seedMovieItem(t, h, contentID, "Title", 2018)
			h.itemRepo.items[contentID].TmdbID = "100"
			h.itemRepo.items[contentID].LockedFields = tc.locked

			if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
				ContentID: contentID, Language: "en", Mode: tc.mode,
			}, []Provider{tmdb, mdblist}); err != nil {
				t.Fatalf("ProcessWithProviders: %v", err)
			}

			if !tc.wantUpsert {
				if len(repo.upserts) != 0 {
					t.Fatalf("upserts = %+v, want none", repo.upserts)
				}
				return
			}
			if len(repo.upserts) != 1 {
				t.Fatalf("upserts = %d, want 1", len(repo.upserts))
			}
			got := repo.upserts[0]
			if got.contentID != contentID || got.replace != tc.wantReplace || got.wholeSet != tc.wantWholeSet {
				t.Fatalf("upsert = (%q, replace=%v, wholeSet=%v), want (%q, replace=%v, wholeSet=%v)",
					got.contentID, got.replace, got.wholeSet, contentID, tc.wantReplace, tc.wantWholeSet)
			}
			if !reflect.DeepEqual(got.sources, wantSources) {
				t.Fatalf("sources = %+v, want %+v", got.sources, wantSources)
			}
		})
	}
}

func TestRefreshWithoutRatingSourcesWritesNothing(t *testing.T) {
	const contentID = "movie:tmdb:100"
	h := newTestHarness()
	repo := &fakeRatingSourceRepo{}
	h.service.ratingSourceRepo = repo
	seedMovieItem(t, h, contentID, "Title", 2018)
	h.itemRepo.items[contentID].TmdbID = "100"
	tmdb := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Title", ProviderIDs: map[string]string{"tmdb": "100"}, Ratings: Ratings{TMDB: 7.6}},
	}

	if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID: contentID, Language: "en", Mode: ModeManualRefresh,
	}, []Provider{tmdb}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if len(repo.upserts) != 0 {
		t.Fatalf("upserts = %+v, want none", repo.upserts)
	}
	if item := h.itemRepo.items[contentID]; item.RatingTMDB == nil || *item.RatingTMDB != 7.6 {
		t.Fatalf("rating_tmdb = %v, want 7.6", item.RatingTMDB)
	}
}

// Identify changes the title under the same content_id, so a match that
// reports no sources still replaces the set: the previous match's sources are
// cleared rather than left describing the item.
func TestIdentifyWithoutRatingSourcesClearsThem(t *testing.T) {
	const contentID = "movie:tmdb:100"
	h := newTestHarness()
	repo := &fakeRatingSourceRepo{}
	h.service.ratingSourceRepo = repo
	seedMovieItem(t, h, contentID, "Title", 2018)
	h.itemRepo.items[contentID].TmdbID = "100"
	tmdb := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Title", ProviderIDs: map[string]string{"tmdb": "100"}},
	}

	if _, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID: contentID, Language: "en", Mode: ModeIdentify,
	}, []Provider{tmdb}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if len(repo.upserts) != 1 {
		t.Fatalf("writes = %+v, want one Replace", repo.upserts)
	}
	if got := repo.upserts[0]; !got.wholeSet || got.contentID != contentID || len(got.sources) != 0 {
		t.Fatalf("write = %+v, want an empty Replace of %q", got, contentID)
	}
}
