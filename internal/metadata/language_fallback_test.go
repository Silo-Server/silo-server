package metadata

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// languageTextProvider answers each request with the text it has in the
// requested language, recording the languages it was asked for.
type languageTextProvider struct {
	slug     string
	types    []string
	items    map[string]*MetadataResult
	seasons  map[string][]SeasonResult
	episodes map[string][]EpisodeResult
	failIn   string

	mu        sync.Mutex
	requested []string
}

func (p *languageTextProvider) Slug() string       { return p.slug }
func (p *languageTextProvider) Name() string       { return p.slug }
func (p *languageTextProvider) ForTypes() []string { return p.types }

func (p *languageTextProvider) record(language string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requested = append(p.requested, language)
	if language == p.failIn {
		return errors.New("provider unavailable")
	}
	return nil
}

func (p *languageTextProvider) askedFor(language string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Contains(p.requested, language)
}

func (p *languageTextProvider) GetMetadata(_ context.Context, req MetadataRequest) (*MetadataResult, error) {
	if err := p.record(req.Language); err != nil {
		return nil, err
	}
	item := p.items[req.Language]
	if item == nil {
		return &MetadataResult{}, nil
	}
	cp := *item
	cp.HasMetadata = true
	return &cp, nil
}

func (p *languageTextProvider) GetSeasons(_ context.Context, req SeasonsRequest) ([]SeasonResult, error) {
	if err := p.record(req.Language); err != nil {
		return nil, err
	}
	return slices.Clone(p.seasons[req.Language]), nil
}

func (p *languageTextProvider) GetEpisodes(_ context.Context, req EpisodesRequest) ([]EpisodeResult, error) {
	if err := p.record(req.Language); err != nil {
		return nil, err
	}
	return slices.Clone(p.episodes[req.Language]), nil
}

// localTextProvider is the local sidecar provider, which the pipeline
// recognizes by IdentityHintProvider.
type localTextProvider struct{ languageTextProvider }

func (p *localTextProvider) IdentityHints(context.Context, SearchQuery) map[string]string { return nil }

// A Swedish library refreshing a film the provider has no Swedish text for
// stores English text before the original-language title, and only where the
// write could change the stored value.
func TestRefreshFillsMissingTextInFallbackLanguage(t *testing.T) {
	swedish := &MetadataResult{
		Title: "響け！ユーフォニアム", TitleIsFallback: true, TitleLanguage: "ja", OriginalLanguage: "ja",
		ProviderIDs: map[string]string{"tmdb": "7"},
	}
	english := &MetadataResult{
		Title: "Sound! Euphonium", Overview: "An English overview.", ProviderIDs: map[string]string{"tmdb": "7"},
	}
	englishAlias := &MetadataResult{
		Title: "響け！ユーフォニアム", TitleIsFallback: true, TitleLanguage: "ja",
		TitleAliases: []TitleAlias{{Title: "Sound! Euphonium", Language: "en", Kind: "localized"}},
		Overview:     "An English overview.", ProviderIDs: map[string]string{"tmdb": "7"},
	}
	tests := []struct {
		name           string
		fallback       string
		mode           RefreshMode
		stored         models.MediaItem
		english        *MetadataResult
		failIn         string
		wantTitle      string
		wantOverview   string
		wantEnglishAsk bool
	}{
		{
			name: "an English title alias fills the title", fallback: "en", mode: ModeManualRefresh, english: englishAlias,
			wantTitle: "Sound! Euphonium", wantOverview: "An English overview.", wantEnglishAsk: true,
		},
		{
			name: "a title marked as another language does not fill the title", fallback: "en", mode: ModeManualRefresh,
			english: &MetadataResult{
				Title: "響け！ユーフォニアム", TitleLanguage: "ja",
				Overview: "An English overview.", ProviderIDs: map[string]string{"tmdb": "7"},
			},
			wantTitle: "Film", wantOverview: "An English overview.", wantEnglishAsk: true,
		},
		{
			name: "manual refresh fills title and overview", fallback: "en", mode: ModeManualRefresh,
			wantTitle: "Sound! Euphonium", wantOverview: "An English overview.", wantEnglishAsk: true,
		},
		{
			name: "fallback off keeps the original title", fallback: "", mode: ModeManualRefresh,
			wantTitle: "響け！ユーフォニアム",
		},
		{
			name: "scheduled refresh does not ask again for text no language had", fallback: "en", mode: ModeScheduledRefresh,
			wantTitle: "Film",
		},
		{
			name: "fill-empty refresh with nothing writable fetches nothing", fallback: "en", mode: ModeScheduledRefresh,
			stored:    models.MediaItem{Overview: "Stored overview."},
			wantTitle: "Film", wantOverview: "Stored overview.",
		},
		{
			name: "locked fields are not fetched", fallback: "en", mode: ModeManualRefresh,
			stored:    models.MediaItem{Overview: "Stored overview.", LockedFields: []int{int(FieldName), int(FieldOverview)}},
			wantTitle: "Film", wantOverview: "Stored overview.",
		},
		{
			name: "failed fallback fetch keeps the stored title", fallback: "en", mode: ModeManualRefresh, failIn: "en",
			wantTitle: "Film", wantEnglishAsk: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			stored := tt.stored
			stored.DefaultMetadataLanguage = "sv"
			h := newTestFilmHarness(ctx, t, stored)
			h.service.SetFallbackLanguage(tt.fallback)
			inEnglish := english
			if tt.english != nil {
				inEnglish = tt.english
			}
			provider := &languageTextProvider{
				slug: "tmdb", types: []string{"movie"}, failIn: tt.failIn,
				items: map[string]*MetadataResult{"sv": swedish, "en": inEnglish},
			}
			if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
				ContentID: testFilmContentID, Mode: tt.mode, Language: "sv",
			}, []Provider{provider}); err != nil {
				t.Fatalf("refresh: %v", err)
			}
			got, err := h.itemRepo.GetByID(ctx, testFilmContentID)
			if err != nil {
				t.Fatalf("load item: %v", err)
			}
			if got.Title != tt.wantTitle || got.Overview != tt.wantOverview {
				t.Errorf("item = %q / %q, want %q / %q", got.Title, got.Overview, tt.wantTitle, tt.wantOverview)
			}
			if asked := provider.askedFor("en"); asked != tt.wantEnglishAsk {
				t.Errorf("asked for English = %v, want %v", asked, tt.wantEnglishAsk)
			}
		})
	}
}

// A tagline from the library's own files stays when the overview it sits
// beside is replaced: it is no part of the provider's original-language text.
func TestFallbackKeepsLocalTagline(t *testing.T) {
	ctx := context.Background()
	h := newTestFilmHarness(ctx, t, models.MediaItem{DefaultMetadataLanguage: "sv"})
	h.service.SetFallbackLanguage("en")
	nfo := &localTextProvider{languageTextProvider{
		slug: "nfo", types: []string{"movie"},
		items: map[string]*MetadataResult{"sv": {Title: "Film", Tagline: "My local tagline"}},
	}}
	remote := &languageTextProvider{
		slug: "tmdb", types: []string{"movie"},
		items: map[string]*MetadataResult{
			"sv": {Title: "Film", Overview: "日本語のあらすじ", OverviewLanguage: "ja", Tagline: "日本語", ProviderIDs: map[string]string{"tmdb": "7"}},
			"en": {Title: "Film", Overview: "An English overview.", Tagline: "English tagline", ProviderIDs: map[string]string{"tmdb": "7"}},
		},
	}
	if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: testFilmContentID, Mode: ModeManualRefresh, Language: "sv",
	}, []Provider{nfo, remote}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got, err := h.itemRepo.GetByID(ctx, testFilmContentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if got.Overview != "An English overview." || got.Tagline != "My local tagline" {
		t.Errorf("item = %q / %q, want the English overview and the local tagline", got.Overview, got.Tagline)
	}
}

// Episode text a provider marks as the original language gives way to a later
// provider's text in the library language, and to fallback-language text when
// no provider has the library language. A numbered stand-in ("Avsnitt 1") is
// no title in the library language.
func TestRefreshPrefersLibraryLanguageEpisodeTextOverOriginal(t *testing.T) {
	const seriesID = "series-tvdb-1"
	original := func(n int, title string) EpisodeResult {
		return EpisodeResult{SeasonNumber: 1, EpisodeNumber: n, Title: title, TitleLanguage: "ja", Overview: "日本語のあらすじ", OverviewLanguage: "ja"}
	}
	for _, tt := range []struct {
		name     string
		fallback string
		local    []EpisodeResult
		want     map[int][2]string
	}{
		{
			name: "fallback on", fallback: "en",
			want: map[int][2]string{
				1: {"Mob and Reigen", "English overview 1."}, 2: {"Avsnitt två", "Svensk beskrivning."},
				3: {"An Invitation", "English overview 3."},
			},
		},
		{
			name: "fallback off", fallback: "",
			want: map[int][2]string{1: {"第１話", "日本語のあらすじ"}, 2: {"Avsnitt två", "Svensk beskrivning."}},
		},
		{
			name: "a numbered title from the library's files stands", fallback: "en",
			local: []EpisodeResult{{SeasonNumber: 1, EpisodeNumber: 1, Title: "Episode 1"}},
			want:  map[int][2]string{1: {"Episode 1", "English overview 1."}, 2: {"Avsnitt två", "Svensk beskrivning."}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			h := newFallbackTestHarness()
			h.service.SetFallbackLanguage(tt.fallback)
			if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
				ContentID: seriesID, Type: "series", Status: "matched", Title: "Mob Psycho 100",
				DefaultMetadataLanguage: "sv", TvdbID: "1",
				Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
			}); err != nil {
				t.Fatal(err)
			}
			// TVDB comes first and has only the original language; TMDB has
			// Swedish for the second episode.
			tvdb := &languageTextProvider{
				slug: "tvdb", types: []string{"series"},
				items:   map[string]*MetadataResult{"sv": {Title: "Mob Psycho 100", Overview: "An overview.", ProviderIDs: map[string]string{"tvdb": "1"}}},
				seasons: map[string][]SeasonResult{"sv": {{SeasonNumber: 1}}},
				episodes: map[string][]EpisodeResult{
					"sv": {original(1, "第１話"), original(2, "第２話"), original(3, "")},
					"en": {
						{SeasonNumber: 1, EpisodeNumber: 1, Title: "Mob and Reigen", Overview: "English overview 1."},
						{SeasonNumber: 1, EpisodeNumber: 2, Title: "Doubts About Youth", Overview: "English overview 2."},
						{SeasonNumber: 1, EpisodeNumber: 3, Title: "An Invitation", Overview: "English overview 3."},
					},
				},
			}
			tmdb := &languageTextProvider{
				slug: "tmdb", types: []string{"series"},
				seasons: map[string][]SeasonResult{"sv": {{SeasonNumber: 1}}},
				episodes: map[string][]EpisodeResult{"sv": {
					{SeasonNumber: 1, EpisodeNumber: 1, Title: "Avsnitt 1"},
					{SeasonNumber: 1, EpisodeNumber: 2, Title: "Avsnitt två", Overview: "Svensk beskrivning."},
				}},
			}
			nfo := &localTextProvider{languageTextProvider{
				slug: "nfo", types: []string{"series"}, episodes: map[string][]EpisodeResult{"sv": tt.local},
			}}
			if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
				ContentID: seriesID, Mode: ModeManualRefresh, Language: "sv",
			}, []Provider{nfo, tvdb, tmdb}); err != nil {
				t.Fatalf("refresh: %v", err)
			}
			for number, want := range tt.want {
				episode, err := h.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, 1, number)
				if err != nil {
					t.Fatalf("episode %d: %v", number, err)
				}
				if episode.Title != want[0] || episode.Overview != want[1] {
					t.Errorf("episode %d = %q / %q, want %q / %q", number, episode.Title, episode.Overview, want[0], want[1])
				}
			}
		})
	}
}
