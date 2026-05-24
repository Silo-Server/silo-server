package abs

import "strings"

// CollapseBySeries folds a flat list of LibraryItems into a deduplicated
// list where every item belonging to a series is represented by a single
// entry carrying a CollapsedSeriesV1 block listing every book in that
// series. Items with no series pass through unchanged.
//
// The representative entry for a series is the first item in source
// order — same convention real ABS uses. The series itself is keyed by
// the first series.ID on each book (real ABS books rarely belong to
// multiple series; the spec defines this as taking the first when they
// do).
//
// Stable across multiple calls: input order determines output order, so
// pagination on top of this remains deterministic.
func CollapseBySeries(items []LibraryItem) []LibraryItem {
	if len(items) == 0 {
		return items
	}
	// Index series → representative slot (+ tracked ID list).
	seriesSlot := make(map[string]int, len(items))
	out := make([]LibraryItem, 0, len(items))

	for _, it := range items {
		series := primarySeries(it)
		if series.ID == "" && series.Name == "" {
			// Not part of a series — pass through.
			out = append(out, it)
			continue
		}
		key := seriesKey(series)
		if slot, ok := seriesSlot[key]; ok {
			out[slot].CollapsedSeries.NumBooks++
			out[slot].CollapsedSeries.LibraryItemIDs = append(
				out[slot].CollapsedSeries.LibraryItemIDs, it.ID)
			continue
		}
		// First sighting — clone the item and attach a fresh
		// CollapsedSeriesV1 with this id as the seed.
		rep := it
		rep.CollapsedSeries = &CollapsedSeriesV1{
			ID:               series.ID,
			Name:             series.Name,
			NameIgnorePrefix: stripLeadingArticle(series.Name),
			NumBooks:         1,
			LibraryItemIDs:   []string{it.ID},
		}
		seriesSlot[key] = len(out)
		out = append(out, rep)
	}
	return out
}

// primarySeries returns the first series ref on a LibraryItem, or a
// zero-value SeriesObj when the book is unaffiliated.
func primarySeries(it LibraryItem) SeriesObj {
	for _, s := range it.Media.Metadata.Series {
		if s.ID != "" || s.Name != "" {
			return s
		}
	}
	return SeriesObj{}
}

// seriesKey prefers the id (stable identifier); falls back to the name
// when the id is empty (legacy / synthesised metadata).
func seriesKey(s SeriesObj) string {
	if s.ID != "" {
		return "id:" + s.ID
	}
	return "name:" + s.Name
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
