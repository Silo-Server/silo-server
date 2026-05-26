package abs

// ABS wire-format constants. ServerVersion must be ≥ 2.26.0 for the official
// ABS mobile app to take its JWT path; below that it falls into "old token"
// mode and rejects modern refresh-token semantics.
// Ref: /opt/audiobookshelf-app/components/connect/ServerConnectForm.vue:731
const (
	VirtualLibraryID   = "silo-audiobooks"
	VirtualLibraryName = "Audiobooks"
	VirtualFolderID    = "main"
	LibraryMediaType   = "book"
	ServerVersion      = "2.35.0"
	ServerSourceTag    = "silo"
)

// AuthorObj is the ABS-shaped author reference. ABS clients filter by id;
// some screens render only name.
type AuthorObj struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SeriesObj is the ABS-shaped series reference; Sequence is the per-book
// position string (e.g. "1", "1.5").
type SeriesObj struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Sequence string `json:"sequence,omitempty"`
}

// ChapterABS is the ABS chapter shape (start/end in seconds, float).
type ChapterABS struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

// AudioTrackMetadata is the file-level metadata block nested inside each
// AudioTrack. The mobile downloader reads filename/ext to name the local
// copy, size to budget storage, and the mtime fields for cache invalidation.
type AudioTrackMetadata struct {
	Filename    string `json:"filename"`
	Ext         string `json:"ext"`
	Path        string `json:"path"`
	RelPath     string `json:"relPath"`
	Size        int64  `json:"size"`
	MtimeMs     int64  `json:"mtimeMs"`
	CtimeMs     int64  `json:"ctimeMs"`
	BirthtimeMs int64  `json:"birthtimeMs"`
}

// AudioTrack is a single playable file as the ABS mobile client expects to
// see it. The shape is rich because the official audiobookshelf-app's Vue
// layer reads many fields off each track — ino + metadata for download URL
// construction and offline-cache decisions, bitRate / channels / codec /
// format for the "Now Playing" detail UI, embeddedCoverArt for whether to
// fall back to the item-level cover, metaTags for ID3-style display. A
// missing key on any of those code paths makes the player silently abort
// the audio load (the "spinner forever" we kept chasing before).
type AudioTrack struct {
	Index                int                 `json:"index"`
	Ino                  string              `json:"ino"`
	Metadata             *AudioTrackMetadata `json:"metadata,omitempty"`
	AddedAt              int64               `json:"addedAt,omitempty"`
	UpdatedAt            int64               `json:"updatedAt,omitempty"`
	TrackNumFromMeta     *int                `json:"trackNumFromMeta"`
	DiscNumFromMeta      *int                `json:"discNumFromMeta"`
	TrackNumFromFilename *int                `json:"trackNumFromFilename"`
	DiscNumFromFilename  *int                `json:"discNumFromFilename"`
	ManuallyVerified     bool                `json:"manuallyVerified"`
	Exclude              bool                `json:"exclude"`
	Error                *string             `json:"error"`
	Format               string              `json:"format,omitempty"`
	Duration             float64             `json:"duration"`
	BitRate              int                 `json:"bitRate,omitempty"`
	Language             *string             `json:"language"`
	Codec                string              `json:"codec,omitempty"`
	TimeBase             string              `json:"timeBase,omitempty"`
	Channels             int                 `json:"channels,omitempty"`
	ChannelLayout        string              `json:"channelLayout,omitempty"`
	Chapters             []ChapterABS        `json:"chapters,omitempty"`
	EmbeddedCoverArt     any                 `json:"embeddedCoverArt"`
	MetaTags             map[string]string   `json:"metaTags,omitempty"`
	MimeType             string              `json:"mimeType"`
	Title                string              `json:"title,omitempty"`
	StartOffset          float64             `json:"startOffset"`
	ContentURL           string              `json:"contentUrl"`
}

// Metadata is the book-level metadata block. Authors / Narrators / Series
// match the ABS spec: arrays of references (or strings for Narrators).
// Genres and Tags intentionally do NOT use omitempty — strict 3rd-party
// clients (Plappa, AudioBookShelfFully) branch on these keys being present
// (even if empty), and dropping the key sends them into degraded mode.
type Metadata struct {
	Title         string      `json:"title"`
	Authors       []AuthorObj `json:"authors"`
	Narrators     []string    `json:"narrators"`
	Series        []SeriesObj `json:"series"`
	Description   string      `json:"description,omitempty"`
	PublishedYear string      `json:"publishedYear,omitempty"`
	ISBN          string      `json:"isbn,omitempty"`
	Publisher     string      `json:"publisher,omitempty"`
	Genres        []string    `json:"genres"`
	Tags          []string    `json:"tags"`
	// Explicit is a content-warning flag the Kotlin BookMetadata declares
	// as non-nullable Boolean. Always emit (default false). silo does not
	// track per-item explicit metadata today; surface it when scanner-side
	// support lands.
	Explicit bool `json:"explicit"`
}

// LibraryItemMedia carries the bulk of the audiobook metadata.
//
// ABS distinguishes between audioFiles (file-level metadata) and tracks
// (the playback ordering the player iterates). For most audiobooks they're
// the same slice; we emit both because the item-detail page reads
// media.tracks.length to decide whether to render the play button, while
// card/list views read media.numTracks.
type LibraryItemMedia struct {
	Metadata   Metadata     `json:"metadata"`
	Duration   float64      `json:"duration"`
	CoverPath  string       `json:"coverPath"`
	AudioFiles []AudioTrack `json:"audioFiles"`
	Tracks     []AudioTrack `json:"tracks"`
	Chapters   []ChapterABS `json:"chapters"`
	NumTracks  int          `json:"numTracks"`
	// Tags is a book-level tag list. NEVER null on the wire — the ABS
	// Android client's Kotlin `Book.tags: List<String>` is non-nullable,
	// so Jackson throws MissingKotlinParameterException when the field
	// is absent (or null) and the entire LibraryItem fails to parse —
	// which silently breaks downloads (the downloader's apiHandler
	// callback receives null and gives up). Always emit []  in v1.
	Tags []string `json:"tags"`
}

// CollapsedSeriesV1 is the per-item annotation real ABS attaches when
// collapseseries=1. The shape is "name + count + per-book books[]"; we emit
// a stable subset since clients differ on which fields they read.
type CollapsedSeriesV1 struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	NameIgnorePrefix string   `json:"nameIgnorePrefix,omitempty"`
	NumBooks         int      `json:"numBooks"`
	LibraryItemIDs   []string `json:"libraryItemIds"`
}

// LibraryItem is the ABS-shaped audiobook summary. AddedAt / UpdatedAt are
// Unix milliseconds; some shelves on the home screen sort by these and
// clients also expect them as ints (not strings).
//
// CollapsedSeries is non-nil only on items returned with collapseseries=1.
// It folds every book in a series into a single representative entry. ABS
// clients pattern-match on the presence of this field to switch from "list
// of books" to "list of series" UI.
//
// Ino / Path / RelPath / MtimeMs / CtimeMs / BirthtimeMs mirror fields the
// real-ABS filesystem-watcher emits at the item root. The ABS Android
// client's Kotlin LibraryItem declares all of these as non-nullable —
// jackson-module-kotlin's behaviour around missing primitives is lenient
// in some configurations but throws in stricter ones, so we always emit
// them with safe defaults (ID-derived ino, empty path strings, AddedAt
// echoed across the three time fields). Costs almost nothing on the wire
// and never causes a parser failure that silently breaks downloads.
type LibraryItem struct {
	ID        string        `json:"id"`
	Ino       string        `json:"ino"`
	LibraryID string        `json:"libraryId"`
	FolderID  string        `json:"folderId"`
	Path      string        `json:"path"`
	RelPath   string        `json:"relPath"`
	MtimeMs   int64         `json:"mtimeMs"`
	CtimeMs   int64         `json:"ctimeMs"`
	BirthtimeMs int64       `json:"birthtimeMs"`
	MediaType string        `json:"mediaType"`
	// IsMissing / IsInvalid are gating fields the ABS mobile client checks
	// before rendering the play affordance. We always emit them (no omitempty)
	// so the client never sees them as undefined; the catalog we serve is by
	// definition present and valid.
	// Ref: /opt/audiobookshelf-app/pages/item/_id/index.vue:445
	IsMissing       bool               `json:"isMissing"`
	IsInvalid       bool               `json:"isInvalid"`
	Media           LibraryItemMedia   `json:"media"`
	NumTracks       int                `json:"numTracks,omitempty"`
	AddedAt         int64              `json:"addedAt"`
	UpdatedAt       int64              `json:"updatedAt"`
	CollapsedSeries *CollapsedSeriesV1 `json:"collapsedSeries,omitempty"`
}
