package scanner

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

func TestSortedVariantOwnerIDs(t *testing.T) {
	ids := map[string]struct{}{
		"episode-c": {},
		"episode-a": {},
		"episode-b": {},
	}

	got := sortedVariantOwnerIDs(ids)
	want := []string{"episode-a", "episode-b", "episode-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortedVariantOwnerIDs() = %v, want %v", got, want)
	}
}

func TestSortedVariantOwnerIDsEmpty(t *testing.T) {
	if got := sortedVariantOwnerIDs(nil); len(got) != 0 {
		t.Fatalf("sortedVariantOwnerIDs(nil) = %v, want empty", got)
	}
}

func TestVariantPartTotals(t *testing.T) {
	tests := []struct {
		name       string
		folder     *models.MediaFolder
		files      []VariantFile
		wantTotals []int
	}{
		{
			name:   "lone part number in a movie title",
			folder: &models.MediaFolder{Type: "movies", Paths: []string{"/movies"}},
			files: []VariantFile{
				{ID: 1, ContentID: "movie:1", FilePath: "/movies/Mockingjay Part 2 (2015)/The.Hunger.Games.Mockingjay.Part.2.2015.BluRay.1080p.mkv"},
			},
			wantTotals: []int{0},
		},
		{
			name:   "versions of one episode with a part number in its title",
			folder: &models.MediaFolder{Type: "series", Paths: []string{"/tv"}},
			files: []VariantFile{
				{ID: 1, EpisodeID: "episode:1", FilePath: "/tv/True Detective/Season 04/True.Detective.S04E04.Part.4.2160p.MAX.WEB-DL.mkv"},
				{ID: 2, EpisodeID: "episode:1", FilePath: "/tv/True Detective/Season 04/True.Detective.S04E04.Part.4.1080p.HMAX.WEB-DL.mkv"},
			},
			wantTotals: []int{0, 0},
		},
		{
			name:   "movie split across discs",
			folder: &models.MediaFolder{Type: "movies", Paths: []string{"/movies"}},
			files: []VariantFile{
				{ID: 1, ContentID: "movie:1", FilePath: "/movies/Movie (2010)/Movie.2010.CD1.mkv"},
				{ID: 2, ContentID: "movie:1", FilePath: "/movies/Movie (2010)/Movie.2010.CD2.mkv"},
			},
			wantTotals: []int{2, 2},
		},
		{
			name:   "episode split into parts",
			folder: &models.MediaFolder{Type: "series", Paths: []string{"/tv"}},
			files: []VariantFile{
				{ID: 1, EpisodeID: "episode:1", FilePath: "/tv/Show/Season 01/Show.S01E01.Part.1.mkv"},
				{ID: 2, EpisodeID: "episode:1", FilePath: "/tv/Show/Season 01/Show.S01E01.Part.2.mkv"},
				{ID: 3, EpisodeID: "episode:1", FilePath: "/tv/Show/Season 01/Show.S01E01.Part.3.mkv"},
			},
			wantTotals: []int{3, 3, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := newVariantCandidates(tt.files, tt.folder)
			totals := variantPartTotals(candidates)
			for i, candidate := range candidates {
				got := 0
				if groupKey, ok := variantPartGroupKey(candidate.ownerKey, candidate.hints); ok {
					got = totals[groupKey]
				}
				if got != tt.wantTotals[i] {
					t.Errorf("%s part total = %d, want %d", candidate.file.FilePath, got, tt.wantTotals[i])
				}
			}
		})
	}
}

// steadyVariantFile returns a file whose stored variant columns already match
// what finalization would write for it.
func steadyVariantFile(t *testing.T, file VariantFile, folder *models.MediaFolder, partTotal int) VariantFile {
	t.Helper()
	hints := naming.ParseVariantHints(file.FilePath, folder.Type, folder.Paths...)
	file.EditionRaw = hints.EditionRaw
	file.EditionKey = hints.EditionKey
	file.EditionConfidence = hints.EditionConfidence
	file.EditionSource = hints.EditionSource
	file.PresentationKind = hints.PresentationKind
	file.PresentationGroupKey = hints.PresentationGroupKey
	file.PresentationPartIndex = hints.PresentationPartIndex
	file.PresentationPartTotal = partTotal
	file.MultiEpisodeStart = hints.MultiEpisodeStart
	file.MultiEpisodeEnd = hints.MultiEpisodeEnd
	return file
}

func TestVariantSiblingOwners(t *testing.T) {
	series := &models.MediaFolder{Type: "series", Paths: []string{"/tv"}}
	movies := &models.MediaFolder{Type: "movies", Paths: []string{"/movies"}}
	set := func(ids ...string) map[string]struct{} {
		out := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			out[id] = struct{}{}
		}
		return out
	}

	seriesFiles := []VariantFile{
		// A plain episode: only its files stored as parts are read, in case
		// one of them was left behind by a part that left the group.
		{ID: 1, EpisodeID: "ep-plain", ContentID: "show", FilePath: "/tv/Show/Season 01/Show.S01E01.1080p.mkv"},
		// A split episode part needs every file of the episode.
		{ID: 2, EpisodeID: "ep-split", ContentID: "show", FilePath: "/tv/Show/Season 01/Show.S01E02.Part.1.mkv"},
		// Stored as a part although its path no longer parses as one, as
		// after a parser change: its old siblings may need a new total.
		{ID: 3, EpisodeID: "ep-reparsed", ContentID: "show", FilePath: "/tv/Show/Season 01/Show.S01E03.mkv",
			PresentationKind: "split_episode", PresentationGroupKey: "show s01e03", PresentationPartIndex: 1},
		// A stored part total alone also counts.
		{ID: 4, EpisodeID: "ep-total", ContentID: "show", FilePath: "/tv/Show/Season 01/Show.S01E04.mkv", PresentationPartTotal: 2},
		// No owner: finalization leaves it alone.
		{ID: 5, FilePath: "/tv/Show/Season 01/Show.S01E05.Part.1.mkv"},
	}
	episodes, contents := variantSiblingOwners(newVariantCandidates(seriesFiles, series))
	want := VariantOwners{
		IDs:   []string{"ep-plain", "ep-reparsed", "ep-split", "ep-total"},
		Whole: set("ep-reparsed", "ep-split", "ep-total"),
	}
	if !reflect.DeepEqual(episodes, want) {
		t.Fatalf("episode owners = %+v, want %+v", episodes, want)
	}
	if len(contents.IDs) != 0 || len(contents.Whole) != 0 {
		t.Fatalf("content owners = %+v, want none", contents)
	}

	movieFiles := []VariantFile{
		{ID: 10, ContentID: "movie-plain", FilePath: "/movies/Plain (2010)/Plain.2010.1080p.mkv"},
		{ID: 11, ContentID: "movie-discs", FilePath: "/movies/Discs (2010)/Discs.2010.CD1.mkv"},
	}
	episodes, contents = variantSiblingOwners(newVariantCandidates(movieFiles, movies))
	if len(episodes.IDs) != 0 || len(episodes.Whole) != 0 {
		t.Fatalf("episode owners = %+v, want none", episodes)
	}
	want = VariantOwners{IDs: []string{"movie-discs", "movie-plain"}, Whole: set("movie-discs")}
	if !reflect.DeepEqual(contents, want) {
		t.Fatalf("content owners = %+v, want %+v", contents, want)
	}
}

func TestVariantMetadataUpdates(t *testing.T) {
	movies := &models.MediaFolder{Type: "movies", Paths: []string{"/movies"}}

	steady := steadyVariantFile(t, VariantFile{ID: 7, ContentID: "movie-steady", FilePath: "/movies/Steady (2010)/Steady.2010.1080p.mkv"}, movies, 0)
	stale := steadyVariantFile(t, VariantFile{ID: 3, ContentID: "movie-stale", FilePath: "/movies/Stale (2010)/Stale.2010.Directors.Cut.1080p.mkv"}, movies, 0)
	if stale.EditionKey == "" {
		t.Fatalf("fixture %s parses no edition", stale.FilePath)
	}
	wantStale := stale
	stale.EditionRaw, stale.EditionKey, stale.EditionSource, stale.EditionConfidence = "", "", "", nil
	imported := VariantFile{ID: 5, ContentID: "movie-import", FilePath: "/movies/Import (2010)/Import.2010.1080p.mkv",
		EditionRaw: "Theatrical", EditionKey: "theatrical", EditionSource: editionSourceImport}
	noOwner := VariantFile{ID: 6, FilePath: "/movies/Loose (2010)/Loose.2010.Directors.Cut.mkv"}
	// Two discs of one movie, the second loaded from outside the scope. Both
	// are stored without a total.
	discOne := steadyVariantFile(t, VariantFile{ID: 2, ContentID: "movie-discs", FilePath: "/movies/Discs (2010)/Discs.2010.CD1.mkv"}, movies, 0)
	discTwo := steadyVariantFile(t, VariantFile{ID: 1, ContentID: "movie-discs", FilePath: "/elsewhere/Discs (2010)/Discs.2010.CD2.mkv"}, movies, 0)

	updates := variantMetadataUpdates(newVariantCandidates([]VariantFile{steady, stale, imported, noOwner, discOne, discTwo}, movies))

	wantDiscOne, wantDiscTwo := discOne, discTwo
	wantDiscOne.PresentationPartTotal, wantDiscTwo.PresentationPartTotal = 2, 2
	want := []VariantFile{wantDiscTwo, wantDiscOne, wantStale}
	if !reflect.DeepEqual(updates, want) {
		t.Fatalf("updates =\n%+v\nwant\n%+v", updates, want)
	}

	// Writing those values back leaves nothing to do on the next pass.
	again := variantMetadataUpdates(newVariantCandidates([]VariantFile{steady, wantStale, imported, noOwner, wantDiscOne, wantDiscTwo}, movies))
	if len(again) != 0 {
		t.Fatalf("second pass updates = %+v, want none", again)
	}
}
