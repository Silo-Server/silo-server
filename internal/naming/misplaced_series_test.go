package naming

import "testing"

func TestIsMisplacedSeriesFile(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		// Misplaced TV show in a movie library: Season dir + SxxExx file.
		{"bleach supercut", `/mnt/unionfs/movies/anime-dub/Bleach Supercuts/Season 01/Bleach Supercuts S01E01 - Ichigo.mkv`, true},
		{"bleach last ep", `/mnt/unionfs/movies/anime-dub/Bleach Supercuts/Season 01/Bleach Supercuts S01E34- Goodbye.mkv`, true},
		{"specials dir", `/mnt/unionfs/movies/anime/Some Show/Specials/Some Show S00E02 - OVA.mkv`, true},
		{"extras dir", `/mnt/unionfs/movies/anime/Some Show/Extras/Some Show S00E03 - Bonus.mkv`, true},
		{"lowercase season", `/x/movies/Show/season 2/Show s02e05.mkv`, true},

		// Legit movies whose release filenames merely contain an SxxExx substring
		// but sit in a proper "Title (Year)/" folder — must NOT be flagged.
		{"fired up", `/mnt/unionfs/movies/alt-cuts/1080p/Fired Up! (2009)/101.Dalmatian.Street.S01E09-E10.Perfect.Match.1080p.mkv`, false},
		{"puppet master", `/mnt/unionfs/movies/alt-cuts/1080p/Puppet Master (1989)/Transformers.Armada.S01E43.Puppet.1080p.mkv`, false},
		{"k seven", `/mnt/unionfs/movies/anime/K Seven Stories Movie 1 RB Blaze (2018) {tmdb-483452}/K (2012) - S00E05 - R-B Blaze.mkv`, false},

		// Ordinary movie, no episode pattern at all.
		{"plain movie", `/mnt/unionfs/movies/00s/Heat (1995) {tmdb-949}/Heat (1995).mkv`, false},
		// Episode pattern but no season/specials directory (loose dump) — not our
		// case here; left to the existing ambiguity heuristic, so NOT flagged.
		{"loose episode no season dir", `/mnt/unionfs/movies/anime/Whatever S01E02.mkv`, false},
	}
	for _, tc := range cases {
		if got := IsMisplacedSeriesFile(tc.path); got != tc.want {
			t.Errorf("%s: IsMisplacedSeriesFile(%q) = %v, want %v", tc.name, tc.path, got, tc.want)
		}
	}
}
