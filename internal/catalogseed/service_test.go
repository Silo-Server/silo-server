package catalogseed

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"testing/iotest"
)

func TestCatalogSeedSearchUpsertIDsIncludesChangedItemsAndEmbeddings(t *testing.T) {
	itemStates := map[string]bool{
		"movie-1":  true,
		"movie-2":  false,
		"series-1": true,
	}
	embeddings := []EmbeddingRecord{
		{MediaItemID: " movie-3 "},
		{MediaItemID: "movie-1"},
		{MediaItemID: ""},
	}
	files := []FileRecord{
		{ContentID: " movie-4 "},
		{ContentID: ""},
	}
	links := []LibraryLinkRecord{
		{ContentID: "movie-5"},
		{ContentID: "movie-2"},
	}

	got := catalogSeedSearchUpsertIDs(itemStates, embeddings, files, links)
	want := []string{"movie-1", "movie-2", "movie-3", "movie-4", "movie-5", "series-1"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalogSeedSearchUpsertIDs = %#v, want %#v", got, want)
	}
}

func TestEmbeddingsForModelKeepsOnlyThatModel(t *testing.T) {
	records := []EmbeddingRecord{
		{MediaItemID: "movie-1", Model: "nomic-embed-text"},
		{MediaItemID: "movie-2", Model: "gemini-embedding-001"},
		{MediaItemID: "movie-3", Model: "nomic-embed-text"},
		{MediaItemID: "movie-4", Model: ""},
	}
	got := embeddingsForModel(records, "nomic-embed-text")
	if len(got) != 2 || got[0].MediaItemID != "movie-1" || got[1].MediaItemID != "movie-3" {
		t.Fatalf("embeddingsForModel = %#v", got)
	}
	if got := embeddingsForModel(records, ""); len(got) != 0 {
		t.Fatalf("an unknown model kept %#v", got)
	}
}

func TestRewriteFileRecordKeepsSidecarsInsideTheLibrary(t *testing.T) {
	rewrites := normalizeRewrites([]PathRewrite{{From: "/old/movies", To: "/media/movies"}})
	folderPaths := map[int][]string{7: {"/media/movies"}}

	tests := []struct {
		name     string
		sidecar  string
		wantPath string
		wantErr  bool
	}{
		{name: "beside the video", sidecar: "/old/movies/Film/Subs/Film.en.ass", wantPath: "/media/movies/Film/Subs/Film.en.ass"},
		{name: "outside the library", sidecar: "/etc/silo/config.ass", wantErr: true},
		{name: "climbs out of the library", sidecar: "/old/movies/Film/../../../etc/passwd", wantErr: true},
		{name: "sibling folder sharing the root's name", sidecar: "/media/movies-private/Film.en.srt", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := FileRecord{
				MediaFolderID:     7,
				FilePath:          "/old/movies/Film/Film.mkv",
				ExternalSubtitles: []ExternalSubtitleRecord{{Path: tt.sidecar, Format: "ass"}},
			}
			got, err := rewriteFileRecord(file, rewrites, folderPaths)
			if tt.wantErr {
				var unmatched *UnmatchedRootsError
				if !errors.As(err, &unmatched) {
					t.Fatalf("rewriteFileRecord error = %v, want *UnmatchedRootsError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("rewriteFileRecord: %v", err)
			}
			if got.ExternalSubtitles[0].Path != tt.wantPath {
				t.Fatalf("sidecar path = %q, want %q", got.ExternalSubtitles[0].Path, tt.wantPath)
			}
		})
	}
}

// decodeBundle walks the bundle by hand to avoid buffering its JSON, so it
// must decode exactly what json.Unmarshal would, and a section added to
// Bundle must not be skipped silently.
func TestDecodeBundleMatchesUnmarshal(t *testing.T) {
	ten := 10
	bundle := Bundle{
		Manifest:     Manifest{FormatVersion: CurrentBundleVersion, SchemaVersion: 3, TMDBRatingSources: true},
		Libraries:    []LibraryRecord{{ExportedID: 1, Paths: []string{"/movies"}, Type: "movies", Name: "Movies"}},
		Items:        []ItemRecord{{ContentID: "movie-1", Type: "movie", Title: "Film", Genres: []string{"Drama"}, AdvisoryAge: &ten}},
		People:       []PersonRecord{{ContentID: "movie-1", PersonID: 7, Name: "Actor"}},
		Embeddings:   []EmbeddingRecord{{MediaItemID: "movie-1", Embedding: []float32{0.25, -1}, Model: "m"}},
		Seasons:      []SeasonRecord{{ContentID: "season-1", SeriesID: "series-1", SeasonNumber: 1}},
		Episodes:     []EpisodeRecord{{ContentID: "episode-1", SeriesID: "series-1", SeasonID: "season-1"}},
		Files:        []FileRecord{{ContentID: "movie-1", MediaFolderID: 1, FilePath: "/movies/Film.mkv", AudioTracks: []AudioTrackRecord{{Codec: "aac"}}}},
		LibraryLinks: []LibraryLinkRecord{{ContentID: "movie-1", MediaFolderID: 1}},
	}
	fields := reflect.ValueOf(bundle)
	for i := range fields.NumField() {
		if fields.Field(i).IsZero() {
			t.Fatalf("fixture leaves Bundle.%s empty; give it a record so the decoder is checked for it", fields.Type().Field(i).Name)
		}
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"exported":           string(raw),
		"null and unknown":   `{"items":null,"extra":{"nested":[1,2]},"files":[],"manifest":{"format_version":2}}` + "\n",
		"keys in other case": `{"Manifest":{"format_version":2},"ITEMS":[{"content_id":"a"}]}`,
		"null bundle":        `null`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			var want Bundle
			if err := json.Unmarshal([]byte(doc), &want); err != nil {
				t.Fatal(err)
			}
			got, err := decodeBundle(bytes.NewReader(gzipBytes(t, doc)))
			if err != nil {
				t.Fatalf("decodeBundle: %v", err)
			}
			if !reflect.DeepEqual(*got, want) {
				t.Fatalf("decodeBundle = %+v, want %+v", *got, want)
			}
		})
	}

	for name, doc := range map[string]string{
		"trailing data":  `{"items":[]} {}`,
		"truncated":      `{"items":[{"content_id":"a"}`,
		"section object": `{"items":{}}`,
		"not an object":  `[1]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeBundle(bytes.NewReader(gzipBytes(t, doc))); !errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("decodeBundle(%q) error = %v, want ErrInvalidBundle", doc, err)
			}
		})
	}
}

// A storage read cut off partway is the source's fault, not the bundle's, so
// the import reports it as a source failure rather than an invalid bundle.
func TestDecodeBundleReportsSourceFailure(t *testing.T) {
	data := gzipBytes(t, `{"items":[{"content_id":"a"},{"content_id":"b"}]}`)
	cutOff := errors.New("connection reset")
	for name, prefix := range map[string][]byte{"in the header": data[:4], "in the body": data[:len(data)-8]} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeBundle(io.MultiReader(bytes.NewReader(prefix), iotest.ErrReader(cutOff)))
			if !errors.Is(err, ErrReadingSource) || !errors.Is(err, cutOff) || errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("decodeBundle error = %v, want ErrReadingSource wrapping the read error", err)
			}
		})
	}
}

func gzipBytes(t *testing.T, doc string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The locked_fields columns don't take NULL, so a bundle's explicit empty list
// must stay an empty list.
func TestCloneIntsNeverReturnsNil(t *testing.T) {
	for _, values := range [][]int{nil, {}} {
		if cloneInts(values) == nil {
			t.Errorf("cloneInts(%#v) = nil", values)
		}
	}
}
