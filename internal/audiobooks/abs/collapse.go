package abs

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// CollapsedAudiobook is one entry of a collapsed library page: a book, and
// when it stands for its series, the IDs of the series' matching books.
type CollapsedAudiobook struct {
	Item *models.MediaItem
	// SeriesID and SeriesName are the series' key and the name the series
	// list shows for it; SeriesBookIDs are its matching books.
	SeriesID      string
	SeriesName    string
	SeriesBookIDs []string
}

// collapsedLibraryItem marks it as the entry standing for its series on a
// collapseseries=1 page, listing the series' matching books, as real ABS
// does. A book outside any series passes through.
func collapsedLibraryItem(it LibraryItem, e CollapsedAudiobook) LibraryItem {
	if len(e.SeriesBookIDs) == 0 {
		return it
	}
	it.CollapsedSeries = &CollapsedSeriesV1{
		ID:               e.SeriesID,
		Name:             e.SeriesName,
		NameIgnorePrefix: stripLeadingArticle(e.SeriesName),
		NumBooks:         len(e.SeriesBookIDs),
		LibraryItemIDs:   e.SeriesBookIDs,
	}
	return it
}

// stripLeadingArticle produces the "ignore-prefix" sort label real ABS
// emits for series — "The Stormlight Archive" sorts under S, not T.
func stripLeadingArticle(name string) string {
	lower := strings.ToLower(name)
	for _, prefix := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(name[len(prefix):])
		}
	}
	return name
}
