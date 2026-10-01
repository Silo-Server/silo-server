package intromarkers

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
)

// kindsRunFixture is a library with a season that has shared credits audio
// and an intro chapter, a chapter intro waiting for silence refinement, and a
// movie.
func kindsRunFixture(t *testing.T) (*Analyzer, *fakeIntroRepository, *fakeMovieSampler) {
	t.Helper()
	repo := &fakeIntroRepository{enabledLibraries: 1}
	sampler := &fakeMovieSampler{silences: []mediasample.Interval{{Start: 6597.5, End: 6598.5}}}
	analyzer := &Analyzer{
		repo: repo, extractor: &fakeFingerprintExtractor{}, movieSampler: sampler, config: DefaultConfig("ffmpeg"),
		node: "node-a", logger: slog.New(slog.DiscardHandler),
	}
	season := cachedCreditsSeason(t, analyzer, repo, 3)
	season[0].Chapters = []models.MediaChapter{
		{Index: 0, Title: "Opening", StartSeconds: 0, EndSeconds: 90},
		{Index: 1, Title: "Part 1", StartSeconds: 90, EndSeconds: 900},
	}
	repo.eligibleCandidates = season
	repo.movieCandidates = []Candidate{movieCandidate(10, 7200)}
	repo.backfillCandidates = []Candidate{{
		FileID: 50, EpisodeID: "backfill", DurationSeconds: 1500,
		IntroStart: floatPtr(0), IntroEnd: floatPtr(90), IntroMarkersSource: strPtr(models.MarkerSourceScanner),
		IntroMarkersAlgorithm: strPtr(ChapterAlgorithm),
		Chapters:              []models.MediaChapter{{Index: 0, Title: "Opening", StartSeconds: 0, EndSeconds: 90}},
	}}
	return analyzer, repo, sampler
}

func TestRunCreditsOnlySkipsIntroWork(t *testing.T) {
	analyzer, repo, sampler := kindsRunFixture(t)

	summary, err := analyzer.Run(context.Background(), EpisodeMarkerKinds{Credits: true}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.ChapterMarkersWritten != 0 || summary.SeasonGroupsConsidered != 0 || summary.SilenceBackfillConsidered != 0 {
		t.Fatalf("summary %+v, want no intro chapters, intro groups, or silence backfill", summary)
	}
	if intros := patchesOfKind(repo.patches, kindIntro); len(intros) != 0 {
		t.Fatalf("intro patches %+v, want none with intro detection off", intros)
	}
	if summary.CreditsAudioMarkersWritten != 3 || summary.MoviesConsidered != 1 || sampler.tailCount() != 1 {
		t.Fatalf("summary %+v with %d movie tails, want episode and movie credits detected", summary, sampler.tailCount())
	}
}

func TestRunIntrosOnlySkipsCreditsAndMovies(t *testing.T) {
	analyzer, repo, sampler := kindsRunFixture(t)

	var last string
	summary, err := analyzer.Run(context.Background(), EpisodeMarkerKinds{Intro: true}, func(_ float64, message string) {
		last = message
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The season's intro chapter and the backfilled chapter intro.
	if summary.ChapterMarkersWritten != 2 || summary.SilenceBackfillConsidered != 1 || summary.SeasonGroupsConsidered != 1 {
		t.Fatalf("summary %+v, want the intro chapters, the silence backfill, and the intro season group", summary)
	}
	if summary.CreditsSeasonGroupsConsidered != 0 || summary.CreditsAudioMarkersWritten != 0 || summary.CreditsChapterMarkersWritten != 0 {
		t.Fatalf("summary %+v, want no episode credits work", summary)
	}
	if summary.MoviesConsidered != 0 || sampler.tailCount() != 0 {
		t.Fatalf("summary %+v with %d movie tails, want movies skipped with credits detection off", summary, sampler.tailCount())
	}
	if credits := patchesOfKind(repo.patches, kindCredits); len(credits) != 0 {
		t.Fatalf("credits patches %+v, want none with credits detection off", credits)
	}
	if last != "Intro detection completed; credits detection is turned off" {
		t.Fatalf("last progress message %q", last)
	}
}

// The episode-only pass of a server without Chromaprint narrows to the kinds
// selected, as Run does.
func TestRunEpisodesCreditsOnlySkipsIntroWork(t *testing.T) {
	analyzer, repo, sampler := kindsRunFixture(t)

	summary, err := analyzer.RunEpisodes(context.Background(), EpisodeMarkerKinds{Credits: true}, nil)
	if err != nil {
		t.Fatalf("RunEpisodes: %v", err)
	}
	if summary.ChapterMarkersWritten != 0 || summary.SeasonGroupsConsidered != 0 || summary.SilenceBackfillConsidered != 0 {
		t.Fatalf("summary %+v, want no intro chapters, intro groups, or silence backfill", summary)
	}
	if intros := patchesOfKind(repo.patches, kindIntro); len(intros) != 0 {
		t.Fatalf("intro patches %+v, want none with intro detection off", intros)
	}
	if summary.CreditsAudioMarkersWritten != 3 || summary.MoviesConsidered != 0 || sampler.tailCount() != 0 {
		t.Fatalf("summary %+v with %d movie tails, want episode credits and no movies", summary, sampler.tailCount())
	}
}

// The movie-only pass of a server without Chromaprint checks movies for
// credits, so it runs with intros off and does nothing with credits off.
func TestRunMoviesFollowsCreditsKind(t *testing.T) {
	t.Run("intros off", func(t *testing.T) {
		analyzer, repo, sampler := kindsRunFixture(t)
		summary, err := analyzer.RunMovies(context.Background(), EpisodeMarkerKinds{Credits: true}, nil)
		if err != nil {
			t.Fatalf("RunMovies: %v", err)
		}
		if summary.MoviesConsidered != 1 || summary.FilesConsidered != 0 || len(patchesOfKind(repo.patches, kindIntro)) != 0 || sampler.tailCount() == 0 {
			t.Fatalf("summary %+v with %d movie tails, want the movie checked and no episodes", summary, sampler.tailCount())
		}
	})
	t.Run("credits off", func(t *testing.T) {
		analyzer, repo, sampler := kindsRunFixture(t)
		var last string
		summary, err := analyzer.RunMovies(context.Background(), EpisodeMarkerKinds{Intro: true}, func(_ float64, message string) {
			last = message
		})
		if err != nil {
			t.Fatalf("RunMovies: %v", err)
		}
		if summary.LibrariesScanned != 0 || summary.MoviesConsidered != 0 || len(repo.patches) != 0 || sampler.tailCount() != 0 {
			t.Fatalf("summary %+v with patches %+v, want no analysis", summary, repo.patches)
		}
		if last != "Credits detection is turned off" {
			t.Fatalf("last progress = %q", last)
		}
	})
}

func TestRunWithNoKindsDoesNothing(t *testing.T) {
	analyzer, repo, sampler := kindsRunFixture(t)

	summary, err := analyzer.Run(context.Background(), EpisodeMarkerKinds{}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.FilesConsidered != 0 || summary.MoviesConsidered != 0 || len(repo.patches) != 0 || sampler.tailCount() != 0 {
		t.Fatalf("summary %+v with patches %+v, want no analysis", summary, repo.patches)
	}
}

type fakeSettings map[string]string

func (f fakeSettings) Get(_ context.Context, key string) (string, error) {
	if value, ok := f[key]; ok && value == "error" {
		return "", errors.New("settings unavailable")
	}
	return f[key], nil
}

func (f fakeSettings) GetMany(ctx context.Context, keys ...string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		value, err := f.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if _, ok := f[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

// getOnlySettings reads one key at a time.
type getOnlySettings map[string]string

func (f getOnlySettings) Get(_ context.Context, key string) (string, error) { return f[key], nil }

// tornSettings answers a save from intros-only to credits-only that lands
// between two single-key reads: Get sees intros before the save and credits
// after it, while GetMany sees the saved pair.
type tornSettings struct{}

func (tornSettings) Get(_ context.Context, key string) (string, error) {
	return map[string]string{markers.SettingDetectIntros: "true", markers.SettingDetectCredits: "true"}[key], nil
}

func (tornSettings) GetMany(context.Context, ...string) (map[string]string, error) {
	return map[string]string{markers.SettingDetectIntros: "false", markers.SettingDetectCredits: "true"}, nil
}

var _ snapshotSettingsReader = (*catalog.EncryptedSettingsRepo)(nil)

func TestEnabledMarkerKinds(t *testing.T) {
	cases := []struct {
		name     string
		settings SettingsReader
		want     EpisodeMarkerKinds
		wantErr  bool
	}{
		{name: "nil reader", settings: nil, want: allMarkerKinds},
		{name: "unset", settings: fakeSettings{}, want: allMarkerKinds},
		{name: "intros off", settings: fakeSettings{markers.SettingDetectIntros: "false"}, want: EpisodeMarkerKinds{Credits: true}},
		{name: "credits off", settings: fakeSettings{markers.SettingDetectCredits: "false"}, want: EpisodeMarkerKinds{Intro: true}},
		{name: "both off", settings: fakeSettings{markers.SettingDetectIntros: "false", markers.SettingDetectCredits: "false"}},
		{name: "read error", settings: fakeSettings{markers.SettingDetectCredits: "error"}, wantErr: true},
		{name: "reads both in one snapshot", settings: tornSettings{}, want: EpisodeMarkerKinds{Credits: true}},
		{name: "no snapshot reads", settings: getOnlySettings{}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EnabledMarkerKinds(context.Background(), tc.settings)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("kinds = %+v, want %+v", got, tc.want)
			}
		})
	}
}
