package naming

import "testing"

func TestNameSeparatorsPreserveDottedAcronyms(t *testing.T) {
	for _, tt := range []struct{ name, want string }{
		{"S.H.O.W.S01", "S.H.O.W S01"},
		{"The.Show.P.I.S01", "The Show P.I S01"},
		{"Marvel's.Agents.of.S.H.I.E.L.D.", "Marvel's Agents of S.H.I.E.L.D."},
		{"Marvel's Agents of S.H.I.E.L.D.", "Marvel's Agents of S.H.I.E.L.D."},
		{"The.Show.S.H.O.W", "The Show S.H.O.W"},
		{"Mr. Robot", "Mr. Robot"},
		{"Mr.Robot", "Mr Robot"},
		{"Mr._Robot", "Mr. Robot"},
		{"S.H.O.W.1080p.H.265.DDP5.1", "S.H.O.W 1080p H 265 DDP5 1"},
		{"Movie.2020.1080p.DTS.HD.MA.5.1", "Movie 2020 1080p DTS HD MA 5 1"},
		{"The.Show.A.B.and.C.D", "The Show A.B and C.D"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeNameSeparators(tt.name); got != tt.want {
				t.Fatalf("normalizeNameSeparators(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestEpisodeSeriesIdentityPreservesDottedAcronyms(t *testing.T) {
	for _, tt := range []struct{ filename, title string }{
		{"S.H.O.W.S01E02.mkv", "S.H.O.W"},
		{"The.Show.P.I.S01E02.mkv", "The Show P.I"},
		{"Marvel's.Agents.of.S.H.I.E.L.D.S01E02.mkv", "Marvel's Agents of S.H.I.E.L.D"},
		{"The.Show.S.H.O.W.S01E02.1080p.H.265.DDP5.1.mkv", "The Show S.H.O.W"},
	} {
		t.Run(tt.filename, func(t *testing.T) {
			hints := ParseFilename("/tv/Incoming/"+tt.filename, "series", "/tv/Incoming")
			if hints.Title != tt.title || hints.SeasonNum != 1 || hints.EpisodeNum != 2 {
				t.Fatalf("parsed hints = %+v, want %q S01E02", hints, tt.title)
			}
		})
	}
}

func TestMovieIdentityPreservesDottedAcronymBeforeReleaseMetadata(t *testing.T) {
	hints := ParseFilename("/movies/loose/S.W.A.T.2003.1080p.H.264.DTS.HD.MA.5.1.mkv", "movies")
	if hints.Title != "S.W.A.T" || hints.Year != 2003 {
		t.Fatalf("parsed hints = %+v, want S.W.A.T (2003)", hints)
	}
}
