package naming

// FilenameHints contains information parsed from file/folder names.
type FilenameHints struct {
	Title       string
	Year        int
	Type        string // movie, series
	SeasonNum   int
	SeasonKnown bool // Distinguishes explicit specials (season 0) from an unspecified season.
	EpisodeNum  int
	AirDate     string // ISO date for daily/by-date episodes, e.g. 2026-04-24
	// EpisodeShowTitle is the PathContext field of the same name.
	EpisodeShowTitle string
}

// PathContext captures the classification and parsed hints for a media path
// after considering both naming heuristics and the declared library type.
type PathContext struct {
	Type                   string // movie, series
	RootPath               string
	LibraryRootPath        string // Deepest configured library path containing this file.
	Title                  string
	Year                   int
	SeasonNum              int
	SeasonKnown            bool
	EpisodeNum             int
	AirDate                string // ISO date for daily/by-date episodes, e.g. 2026-04-24
	HasEpisodePattern      bool
	HasAirDatePattern      bool
	HasSeasonStructure     bool
	HasMovieFolderEvidence bool
	// EpisodeShowTitle is the show title an episode's file name gives before
	// its episode number, without release group, year, or season label, when
	// no show folder already gives that title. It is empty when the name gives
	// none or repeats its show folder's title: "Mob Psycho 100 III" for
	// Mob Psycho 100/Mob Psycho 100 III - 05.mkv, empty for
	// Mob Psycho 100/Mob Psycho 100 - 05.mkv.
	EpisodeShowTitle string
}

// FolderIDHints contains external IDs parsed from folder names.
type FolderIDHints struct {
	TmdbID string
	ImdbID string
	TvdbID string
}

// CanonicalRoot describes the deduplicated content root for a media file.
// For series content this is the show folder; for movies it is the containing
// movie folder when one can be inferred, or a synthetic root derived from the
// filename stem for true loose files.
type CanonicalRoot struct {
	RootPath string // Absolute path to the canonical root directory (or synthetic path for true loose files).
	Type     string // "series" or "movie".
}

// VariantHints captures edition and presentation metadata parsed from a path.
type VariantHints struct {
	EditionRaw            string
	EditionKey            string
	EditionSource         string
	EditionConfidence     *float64
	PresentationKind      string
	PresentationGroupKey  string
	PresentationPartIndex int
	MultiEpisodeStart     int
	MultiEpisodeEnd       int
}
