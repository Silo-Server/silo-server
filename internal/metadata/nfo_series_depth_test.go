package metadata_test

// Phase D integration tests: an NFO-only series library scans into a fully
// presented tree using the real built-in NFO provider — series metadata and
// root art (Phases A+C), season names/posters from season.nfo, and episode
// titles/plots/thumbs from <basename>.nfo (Phase D). These live in an
// external test package so they can import internal/metadata/nfo (which
// imports internal/metadata) without an import cycle; the in-package
// fakes are exposed through NFOSeriesHarness (export_nfo_test.go).

import (
	"context"

	"os"
	"path/filepath"

	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

func writeFixtureFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// buildFitnessFixture lays out the P90X fitness library from the Phase D spec
// and returns (seriesRoot, episode file paths).
func buildFitnessFixture(t *testing.T) (string, []string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Fitness", "P90X")
	writeFixtureFile(t, filepath.Join(root, "tvshow.nfo"),
		`<tvshow><title>P90X</title><plot>90-day home fitness program.</plot></tvshow>`)
	writeFixtureFile(t, filepath.Join(root, "poster.jpg"), "series-poster")
	writeFixtureFile(t, filepath.Join(root, "fanart.jpg"), "series-fanart")

	s1 := filepath.Join(root, "Season 01")
	writeFixtureFile(t, filepath.Join(s1, "season.nfo"),
		`<season><title>Course A: Classic</title><plot>The classic schedule.</plot><seasonnumber>1</seasonnumber></season>`)
	writeFixtureFile(t, filepath.Join(s1, "poster.jpg"), "s1-poster")
	e1 := filepath.Join(s1, "P90X S01E01 - Chest and Back.mkv")
	writeFixtureFile(t, e1, "video")
	writeFixtureFile(t, filepath.Join(s1, "P90X S01E01 - Chest and Back.nfo"),
		`<episodedetails><title>Chest and Back</title><plot>Push and pull.</plot><season>1</season><episode>1</episode></episodedetails>`)
	writeFixtureFile(t, filepath.Join(s1, "P90X S01E01 - Chest and Back-thumb.jpg"), "e1-thumb")

	s2 := filepath.Join(root, "Season 02")
	writeFixtureFile(t, filepath.Join(s2, "season.nfo"),
		`<season><title>Course B</title><seasonnumber>2</seasonnumber></season>`)
	writeFixtureFile(t, filepath.Join(s2, "poster.jpg"), "s2-poster")
	e2 := filepath.Join(s2, "P90X S02E01 - Core.mkv")
	writeFixtureFile(t, e2, "video")
	writeFixtureFile(t, filepath.Join(s2, "P90X S02E01 - Core.nfo"),
		`<episodedetails><title>Core</title><plot>Core synergistics.</plot></episodedetails>`)
	writeFixtureFile(t, filepath.Join(s2, "P90X S02E01 - Core-thumb.jpg"), "e2-thumb")

	return root, []string{e1, e2}
}

// remoteEpisodeStubProvider is a canned remote series provider with
// season/episode support, standing in for TVDB in the mixed case.
type remoteEpisodeStubProvider struct {
	mu       sync.Mutex
	slug     string
	metadata *metadata.MetadataResult
	seasons  []metadata.SeasonResult
	episodes map[int][]metadata.EpisodeResult
	images   []metadata.RemoteImage
}

func (p *remoteEpisodeStubProvider) Slug() string       { return p.slug }
func (p *remoteEpisodeStubProvider) Name() string       { return p.slug }
func (p *remoteEpisodeStubProvider) ForTypes() []string { return []string{"movie", "series"} }

func (p *remoteEpisodeStubProvider) GetMetadata(_ context.Context, _ metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.metadata != nil {
		cp := *p.metadata
		return &cp, nil
	}
	return &metadata.MetadataResult{}, nil
}

func (p *remoteEpisodeStubProvider) GetImages(_ context.Context, _ metadata.ImageRequest) ([]metadata.RemoteImage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]metadata.RemoteImage(nil), p.images...), nil
}

func (p *remoteEpisodeStubProvider) GetSeasons(_ context.Context, _ metadata.SeasonsRequest) ([]metadata.SeasonResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]metadata.SeasonResult, len(p.seasons))
	copy(out, p.seasons)
	return out, nil
}

func (p *remoteEpisodeStubProvider) GetEpisodes(_ context.Context, req metadata.EpisodesRequest) ([]metadata.EpisodeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]metadata.EpisodeResult, len(p.episodes[req.SeasonNumber]))
	copy(out, p.episodes[req.SeasonNumber])
	return out, nil
}
