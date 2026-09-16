package naming

import "testing"

func TestParseVariantHints_PlexFolderEditionTag(t *testing.T) {
	hints := ParseVariantHints(
		"/movies/Movie (1982) {edition-Final Cut}/Movie (1982) - 2160p.mkv",
		"movies",
	)
	if hints == nil {
		t.Fatal("expected hints")
	}
	if got, want := hints.EditionKey, "final_cut"; got != want {
		t.Fatalf("EditionKey = %q, want %q", got, want)
	}
	if got, want := hints.EditionSource, "plex_tag_folder"; got != want {
		t.Fatalf("EditionSource = %q, want %q", got, want)
	}
}

func TestParseVariantHints_HeuristicExtendedDirectorsCutFanEdit(t *testing.T) {
	hints := ParseVariantHints(
		"/movies/Movie (1982)/Movie (1982) Extended Directors Cut Fan Edit 1080p.mkv",
		"movies",
	)
	if hints == nil {
		t.Fatal("expected hints")
	}
	if got, want := hints.EditionKey, "extended_director_cut_fan_edit"; got != want {
		t.Fatalf("EditionKey = %q, want %q", got, want)
	}
	if got, want := hints.EditionSource, "heuristic_file"; got != want {
		t.Fatalf("EditionSource = %q, want %q", got, want)
	}
}

func TestParseVariantHints_ComposesUnratedDirectorCutFromReleaseStem(t *testing.T) {
	hints := ParseVariantHints(
		"/movies/alt-cuts/1080p/Borderland (2007)/Borderland.2007.Unrated.Directors.Cut.BluRay.1080p.DTS-HD.MA.5.1.AVC.REMUX-FraMeSToR.mkv",
		"movies",
	)
	if hints == nil {
		t.Fatal("expected hints")
	}
	if got, want := hints.EditionKey, "unrated_director_cut"; got != want {
		t.Fatalf("EditionKey = %q, want %q", got, want)
	}
	if got, want := hints.EditionSource, "heuristic_file"; got != want {
		t.Fatalf("EditionSource = %q, want %q", got, want)
	}
}

func TestParseVariantHints_DoesNotParseChristmasEditionTitleAsEdition(t *testing.T) {
	hints := ParseVariantHints(
		"/movies/The Christmas Edition (1941)/The Christmas Edition (1941) 720p HDTV x264.mkv",
		"movies",
	)
	if hints == nil {
		t.Fatal("expected hints")
	}
	if hints.EditionKey != "" {
		t.Fatalf("EditionKey = %q, want empty", hints.EditionKey)
	}
}

func TestParseVariantHints_DoesNotParseFinalCutTitleWordsAsEdition(t *testing.T) {
	hints := ParseVariantHints(
		"/movies/Urban Legends Final Cut (2000)/Urban Legends Final Cut (2000) 1080p BluRay x264.mkv",
		"movies",
	)
	if hints == nil {
		t.Fatal("expected hints")
	}
	if hints.EditionKey != "" {
		t.Fatalf("EditionKey = %q, want empty", hints.EditionKey)
	}
}

func TestParseVariantHints_JusticeIsGrayEdition(t *testing.T) {
	hints := ParseVariantHints(
		"/movies/Zack Snyders Justice League Justice Is Gray (2021)/Zack.Snyders.Justice.League.Justice.Is.Gray.2021.2160p.HMAX.WEB-DL.DDP5.1.Atmos.HDR.HEVC-TOMMY.mkv",
		"movies",
	)
	if hints == nil {
		t.Fatal("expected hints")
	}
	if got, want := hints.EditionKey, "justice_is_gray"; got != want {
		t.Fatalf("EditionKey = %q, want %q", got, want)
	}
	if got, want := hints.EditionSource, "heuristic_file"; got != want {
		t.Fatalf("EditionSource = %q, want %q", got, want)
	}
}

func TestStripComparisonSafeEditionSuffix_DoesNotAlterDistinctGreyTitle(t *testing.T) {
	if got, want := StripComparisonSafeEditionSuffix("Fifty Shades of Grey"), "Fifty Shades of Grey"; got != want {
		t.Fatalf("StripComparisonSafeEditionSuffix() = %q, want %q", got, want)
	}
}

func TestParseVariantHints_FourDigitMultiEpisodeRange(t *testing.T) {
	hints := ParseVariantHints(
		"/tv/One Piece/Season 23/One Piece S23E1162-E1163 - Wano Country.mkv",
		"series",
	)
	if hints == nil {
		t.Fatal("expected hints")
	}
	if got, want := hints.PresentationKind, "multi_episode"; got != want {
		t.Fatalf("PresentationKind = %q, want %q", got, want)
	}
	if got, want := hints.MultiEpisodeStart, 1162; got != want {
		t.Fatalf("MultiEpisodeStart = %d, want %d", got, want)
	}
	if got, want := hints.MultiEpisodeEnd, 1163; got != want {
		t.Fatalf("MultiEpisodeEnd = %d, want %d", got, want)
	}
}
