package naming

import "testing"

func TestJellyfinProviderAttributesAnchorIdentity(t *testing.T) {
	for _, folder := range []string{
		"Example Movie (2020) [tmdbid=12345] [imdbid=tt1234567]",
		"Example Movie (2020) {TMDB=12345} (IMDB=tt1234567)",
	} {
		path := "/movies/" + folder + "/title00.mkv"
		_, assignments := InferRootAssignments([]string{path}, "movies", 1, nil)
		group := InferGroupIdentity(path, "movies", assignments[path])
		if group.TmdbID != "12345" || group.ImdbID != "tt1234567" || group.BaseTitle != "Example Movie" || group.BaseYear != 2020 || group.State != "resolved" {
			t.Fatalf("identity for %q = %+v", folder, group)
		}
	}
	for _, name := range []string{"Example [tmdbid=0]", "Example [tvdbid=-123]", "Example [imdbid=1234567]", "Example [tmdbid=abc]"} {
		if ids := ParseFolderIDs(name); ids != nil {
			t.Fatalf("invalid provider attribute %q became trusted: %+v", name, ids)
		}
	}
}
