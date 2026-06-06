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
type MinifiedLibraryItem struct {
	ID        string        `json:"id"`
	LibraryID string        `json:"libraryId"`
	FolderID  string        `json:"folderId"`
	MediaType string        `json:"mediaType"`
	Media     minifiedMedia `json:"media"`
	NumTracks int           `json:"numTracks,omitempty"`
	AddedAt   int64         `json:"addedAt"`
	UpdatedAt int64         `json:"updatedAt"`
}

type minifiedMedia struct {
	Metadata  minifiedMetadata `json:"metadata"`
	Duration  float64          `json:"duration"`
	CoverPath string           `json:"coverPath"`
}

type minifiedMetadata struct {
	Title          string   `json:"title"`
	AuthorName     string   `json:"authorName"`
	AuthorNameLF   string   `json:"authorNameLF"`
	SeriesName     string   `json:"seriesName,omitempty"`
	SeriesSequence string   `json:"seriesSequence,omitempty"`
	Narrators      []string `json:"narrators,omitempty"`
	PublishedYear  string   `json:"publishedYear,omitempty"`
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
	seriesName, seriesSeq := "", ""
	if len(m.Series) > 0 {
		s := m.Series[0]
		seriesName = s.Name
		seriesSeq = s.Sequence
	}
	return MinifiedLibraryItem{
		ID:        item.ID,
		LibraryID: item.LibraryID,
		FolderID:  item.FolderID,
		MediaType: item.MediaType,
		Media: minifiedMedia{
			Metadata: minifiedMetadata{
				Title:          m.Title,
				AuthorName:     strings.Join(names, ", "),
				AuthorNameLF:   strings.Join(lfNames, " & "),
				SeriesName:     seriesName,
				SeriesSequence: seriesSeq,
				Narrators:      m.Narrators,
				PublishedYear:  m.PublishedYear,
			},
			Duration:  item.Media.Duration,
			CoverPath: item.Media.CoverPath,
		},
		NumTracks: item.NumTracks,
		AddedAt:   item.AddedAt,
		UpdatedAt: item.UpdatedAt,
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
