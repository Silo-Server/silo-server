package abs

import "strings"

// MinifiedLibraryItem mirrors real-ABS's `minified=1` response shape. The
// big-ticket changes from the full LibraryItem: media.metadata.authors and
// media.metadata.series arrays are omitted; the metadata block grows flat
// authorName / authorNameLF / seriesName / seriesSequence fields built from
// the same source data; chapters and audioFiles are dropped.
//
// Real ABS clients sniff for these fields by name. Emitting the full shape
// for a minified request works (clients ignore extra fields) but is wasteful
// on the wire when a client is paging through hundreds of items. Emitting
// the minified shape for a full request silently breaks detail pages.
// Keys mirror real ABS LibraryItem.toOldJSONMinified. numFiles/size are
// minified-only; libraryFiles/lastScan/scanVersion (full-only) are absent.
type MinifiedLibraryItem struct {
	ID               string             `json:"id"`
	Ino              string             `json:"ino"`
	OldLibraryItemID *string            `json:"oldLibraryItemId"`
	LibraryID        string             `json:"libraryId"`
	FolderID         string             `json:"folderId"`
	Path             string             `json:"path"`
	RelPath          string             `json:"relPath"`
	IsFile           bool               `json:"isFile"`
	MtimeMs          int64              `json:"mtimeMs"`
	CtimeMs          int64              `json:"ctimeMs"`
	BirthtimeMs      int64              `json:"birthtimeMs"`
	AddedAt          int64              `json:"addedAt"`
	UpdatedAt        int64              `json:"updatedAt"`
	IsMissing        bool               `json:"isMissing"`
	IsInvalid        bool               `json:"isInvalid"`
	MediaType        string             `json:"mediaType"`
	Media            minifiedMedia      `json:"media"`
	NumFiles         int                `json:"numFiles"`
	Size             int64              `json:"size"`
	CollapsedSeries  *CollapsedSeriesV1 `json:"collapsedSeries,omitempty"`
}

// Keys mirror real ABS Book.toOldJSONMinified: numeric summaries instead of
// audioFiles/chapters/tracks. numTracks/numAudioFiles drive whether strict
// clients (Plappa) show the item at all — an item reporting 0 audio files is
// dropped, so they are always >= 1 in the browse projection.
type minifiedMedia struct {
	ID            string           `json:"id"`
	Metadata      minifiedMetadata `json:"metadata"`
	CoverPath     string           `json:"coverPath"`
	Tags          []string         `json:"tags"`
	NumTracks     int              `json:"numTracks"`
	NumAudioFiles int              `json:"numAudioFiles"`
	NumChapters   int              `json:"numChapters"`
	Duration      float64          `json:"duration"`
	Size          int64            `json:"size"`
	EbookFormat   *string          `json:"ebookFormat"`
}

// Keys mirror real ABS Book.oldMetadataToJSONMinified — flat author/series
// strings, no authors[]/series[] arrays. Nullable-in-ABS string fields are
// emitted as "" (safe: the clients decode them as String?), never dropped.
type minifiedMetadata struct {
	Title             string   `json:"title"`
	TitleIgnorePrefix string   `json:"titleIgnorePrefix"`
	Subtitle          string   `json:"subtitle"`
	AuthorName        string   `json:"authorName"`
	AuthorNameLF      string   `json:"authorNameLF"`
	NarratorName      string   `json:"narratorName"`
	SeriesName        string   `json:"seriesName"`
	Genres            []string `json:"genres"`
	PublishedYear     string   `json:"publishedYear"`
	PublishedDate     string   `json:"publishedDate"`
	Publisher         string   `json:"publisher"`
	Description       string   `json:"description"`
	ISBN              string   `json:"isbn"`
	ASIN              string   `json:"asin"`
	Language          string   `json:"language"`
	Explicit          bool     `json:"explicit"`
	Abridged          bool     `json:"abridged"`
}

// Minify projects a LibraryItem onto the minified shape. The original item
// is not mutated. authorName joins authors with ", "; authorNameLF inverts
// the standard "First Last" → "Last, First" form when there's a space, and
// joins multiple authors with " & " (matching the convention real ABS uses
// for shelf sort labels).
func Minify(item LibraryItem) MinifiedLibraryItem {
	m := item.Media.Metadata
	names := make([]string, 0, len(m.Authors))
	lfNames := make([]string, 0, len(m.Authors))
	for _, a := range m.Authors {
		if a.Name == "" {
			continue
		}
		names = append(names, a.Name)
		lfNames = append(lfNames, lastFirst(a.Name))
	}
	seriesName := ""
	if len(m.Series) > 0 {
		seriesName = m.Series[0].Name
		if seq := m.Series[0].Sequence; seq != "" {
			seriesName += " #" + seq
		}
	}
	genres := m.Genres
	if genres == nil {
		genres = []string{}
	}
	tags := item.Media.Tags
	if tags == nil {
		tags = []string{}
	}
	// Every audiobook has at least one audio file; report >= 1 so strict
	// clients (Plappa) don't drop the item. Exact counts come from item-detail.
	numTracks := item.Media.NumTracks
	if numTracks < 1 {
		numTracks = 1
	}
	return MinifiedLibraryItem{
		ID:          item.ID,
		Ino:         item.Ino,
		LibraryID:   item.LibraryID,
		FolderID:    item.FolderID,
		Path:        item.Path,
		RelPath:     item.RelPath,
		IsFile:      item.IsFile,
		MtimeMs:     item.MtimeMs,
		CtimeMs:     item.CtimeMs,
		BirthtimeMs: item.BirthtimeMs,
		AddedAt:     item.AddedAt,
		UpdatedAt:   item.UpdatedAt,
		IsMissing:   item.IsMissing,
		IsInvalid:   item.IsInvalid,
		MediaType:   item.MediaType,
		Media: minifiedMedia{
			ID: item.Media.ID,
			Metadata: minifiedMetadata{
				Title:             m.Title,
				TitleIgnorePrefix: titleIgnorePrefix(m.Title),
				AuthorName:        strings.Join(names, ", "),
				AuthorNameLF:      strings.Join(lfNames, ", "),
				NarratorName:      strings.Join(m.Narrators, ", "),
				SeriesName:        seriesName,
				Genres:            genres,
				PublishedYear:     m.PublishedYear,
				Publisher:         m.Publisher,
				Description:       m.Description,
				ISBN:              m.ISBN,
				Language:          "en",
				Explicit:          m.Explicit,
			},
			CoverPath:     item.Media.CoverPath,
			Tags:          tags,
			NumTracks:     numTracks,
			NumAudioFiles: numTracks,
			NumChapters:   len(item.Media.Chapters),
			Duration:      item.Media.Duration,
			Size:          0,
		},
		NumFiles:        numTracks,
		Size:            0,
		CollapsedSeries: item.CollapsedSeries,
	}
}

// lastFirst flips a "First Middle Last" name into "Last, First Middle". For
// single-token names it returns them unchanged. Empty inputs return empty.
func lastFirst(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	last := strings.LastIndexByte(name, ' ')
	if last <= 0 || last == len(name)-1 {
		return name
	}
	return name[last+1:] + ", " + name[:last]
}
