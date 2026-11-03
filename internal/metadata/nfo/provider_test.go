package nfo

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestFindNFO_ExtensionlessFilePathUsesBasenameMatchedNFO(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Blade Runner (1982)")
	want := filepath.Join(dir, "Blade Runner (1982).nfo")
	writeFile(t, filePath, "video")
	writeFile(t, want, "<movie><title>File</title></movie>")

	if got, _, _ := NewProvider().newLookup(context.Background()).findItemNFO([]string{filePath}, nil, ""); got != want {
		t.Fatalf("findItemNFO(extensionless file path) = %q, want %q", got, want)
	}
}

// A tvshow.nfo sitting next to a movie file must not inject series data into
// a movie item at top priority: GetMetadata carries the same ContentType
// guard Search has.
func TestGetMetadata_TVShowNFOBesideMovieFileDoesNotInject(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Home Movie (2021).mkv")
	writeFile(t, filePath, "video")
	nfoXML := `<tvshow><title>Some Series</title><uniqueid type="tvdb">123</uniqueid></tvshow>`
	writeFile(t, filepath.Join(dir, "tvshow.nfo"), nfoXML)

	p := NewProvider()
	result, err := p.GetMetadata(context.Background(), metadata.MetadataRequest{
		ContentType:               "movie",
		FilePath:                  filePath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if result.HasMetadata {
		t.Fatalf("GetMetadata() = %#v, want no metadata (series NFO must not inject into a movie)", result)
	}
}

// GetMetadata surfaces the full Phase-B field set from a curated movie NFO.
func TestGetMetadata_FullMovieFieldSet(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Blade Runner (1982).mkv")
	writeFile(t, filePath, "video")
	writeFile(t, filepath.Join(dir, "movie.nfo"), fullMovieNFO)

	p := NewProvider()
	result, err := p.GetMetadata(context.Background(), metadata.MetadataRequest{
		ContentType:               "movie",
		FilePath:                  filePath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if !result.HasMetadata {
		t.Fatal("HasMetadata = false, want true")
	}
	if result.Title != "Blade Runner" || result.Year != 1982 {
		t.Errorf("title/year = %q/%d", result.Title, result.Year)
	}
	if result.OriginalTitle != "Blade Runner: The Original" {
		t.Errorf("OriginalTitle = %q", result.OriginalTitle)
	}
	if result.Tagline != "Man has made his match" {
		t.Errorf("Tagline = %q", result.Tagline)
	}
	if result.Runtime != 117 {
		t.Errorf("Runtime = %d", result.Runtime)
	}
	if result.ReleaseDate != "1982-06-25" {
		t.Errorf("ReleaseDate = %q", result.ReleaseDate)
	}
	if result.ContentRating != "R" {
		t.Errorf("ContentRating = %q", result.ContentRating)
	}
	if want := []string{"Science Fiction", "Thriller"}; !reflect.DeepEqual(result.Genres, want) {
		t.Errorf("Genres = %#v, want %#v", result.Genres, want)
	}
	if want := []string{"Warner Bros.", "The Ladd Company"}; !reflect.DeepEqual(result.Studios, want) {
		t.Errorf("Studios = %#v, want %#v", result.Studios, want)
	}
	if want := []string{"United States", "Hong Kong"}; !reflect.DeepEqual(result.Countries, want) {
		t.Errorf("Countries = %#v, want %#v", result.Countries, want)
	}
	if want := []string{"dystopia", "neo-noir"}; !reflect.DeepEqual(result.Keywords, want) {
		t.Errorf("Keywords = %#v, want %#v", result.Keywords, want)
	}
	if result.Ratings.IMDB != 8.1 || result.Ratings.TMDB != 7.9 || result.Ratings.RTCritic != 89 || result.Ratings.RTAudience != 91 {
		t.Errorf("Ratings = %#v", result.Ratings)
	}
	// IMDb and TMDB fill the rating columns above; their rows, which hold the
	// vote counts discovery ranks by, stay the remote providers'.
	wantSources := map[string]metadata.RatingSource{
		models.RatingSourceRTCritic:   {Score: 89, Provider: "nfo"},
		models.RatingSourceRTAudience: {Score: 91, Provider: "nfo"},
	}
	if !reflect.DeepEqual(result.RatingSources, wantSources) {
		t.Errorf("RatingSources = %#v, want %#v", result.RatingSources, wantSources)
	}
	if result.SortTitle != "Blade Runner 1" {
		t.Errorf("SortTitle = %q, want Blade Runner 1", result.SortTitle)
	}
	wantPeople := []models.ItemPerson{
		{Person: models.Person{Name: "Harrison Ford"}, Kind: models.PersonKindActor, Character: "Rick Deckard", SortOrder: 0, LocalPhotoSource: "nfo+https://example.com/ford.jpg"},
		{Person: models.Person{Name: "Rutger Hauer"}, Kind: models.PersonKindActor, Character: "Roy Batty", SortOrder: 1},
		{Person: models.Person{Name: "Ridley Scott"}, Kind: models.PersonKindDirector, SortOrder: 0},
		{Person: models.Person{Name: "Hampton Fancher"}, Kind: models.PersonKindWriter, SortOrder: 0},
		{Person: models.Person{Name: "David Peoples"}, Kind: models.PersonKindWriter, SortOrder: 1},
	}
	if !reflect.DeepEqual(result.People, wantPeople) {
		t.Errorf("People = %#v, want %#v", result.People, wantPeople)
	}
	if result.ProviderIDs["imdb"] != "tt0083658" || result.ProviderIDs["tmdb"] != "78" {
		t.Errorf("ProviderIDs = %#v", result.ProviderIDs)
	}
	if result.FirstAirDate != "" {
		t.Errorf("FirstAirDate = %q, want empty for a movie", result.FirstAirDate)
	}
}

// A minimal NFO must not emit placeholder collections: empty slices stay nil
// so MergeFillEmpty's early-return lets remote providers fill them.
func TestGetMetadata_MinimalNFOEmitsNoPlaceholders(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Home Movie (2021).mkv")
	writeFile(t, filePath, "video")
	writeFile(t, filepath.Join(dir, "movie.nfo"), `<movie><title>Home Movie</title></movie>`)

	p := NewProvider()
	result, err := p.GetMetadata(context.Background(), metadata.MetadataRequest{
		ContentType:               "movie",
		FilePath:                  filePath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if result.Genres != nil || result.Studios != nil || result.Countries != nil || result.Keywords != nil || result.People != nil {
		t.Errorf("placeholder collections emitted: %#v", result)
	}
	if result.Runtime != 0 || result.ReleaseDate != "" || result.ContentRating != "" {
		t.Errorf("placeholder scalars emitted: %#v", result)
	}
}
