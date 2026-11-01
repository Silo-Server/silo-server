package emailalerts

import "time"

// SampleEntries is placeholder content for previews when the catalog has
// nothing new to show. Artwork is left empty so the preview shows the layout without it.
func SampleEntries() []Entry {
	now := time.Now().UTC()
	ep := func(n int) *int { return &n }
	at := func(d time.Duration) time.Time { return now.Add(-d) }
	series := &Detail{Card: Card{
		ContentID: "series:sample-harbour-lights", Type: "series", Title: "Harbour Lights", Year: 2024,
		Overview: "In a fog-bound fishing town, a newly arrived harbourmaster uncovers a decades-old disappearance that the locals would rather stay buried.",
		Genres:   []string{"Drama", "Mystery"}, Networks: []string{"BBC One"}, RatingTMDB: 8.1, ShowStatus: "Returning Series",
	}, SeasonCount: 2}
	return []Entry{
		{
			Card: Card{
				ContentID: "episode:sample-1", Type: "episode", Title: "The Lantern Room", AddedAt: at(20 * time.Minute),
				Overview: "Mara finally gets inside the lighthouse, but what she finds in the lantern room changes everything she thought she knew about the night of the storm.",
				SeriesID: series.ContentID, SeriesTitle: series.Title, SeasonNumber: ep(2), EpisodeNumber: ep(3),
				ReleaseDate: now.AddDate(0, 0, -1).Format("2006-01-02"), Runtime: 58,
			},
			Library: LibraryRef{ID: "tv", Name: "TV Shows"},
			Series:  series,
		},
		{
			Card: Card{
				ContentID: "episode:sample-2", Type: "episode", Title: "Low Tide", AddedAt: at(21 * time.Minute),
				Overview: "A body washes up on the morning tide, and the town closes ranks.",
				SeriesID: series.ContentID, SeriesTitle: series.Title, SeasonNumber: ep(2), EpisodeNumber: ep(4),
				ReleaseDate: now.Format("2006-01-02"), Runtime: 54,
			},
			Library: LibraryRef{ID: "tv", Name: "TV Shows"},
			Series:  series,
		},
		{
			Card: Card{
				ContentID: "movie:sample-3", Type: "movie", Title: "The Long Way North", Year: 2025, AddedAt: at(45 * time.Minute),
				Overview: "Two estranged sisters drive their late father's vintage camper from Cornwall to Cape Wrath to scatter his ashes, and discover he left them one last puzzle along the way.",
				Genres:   []string{"Comedy", "Drama"}, Runtime: 112, ContentRating: "12A", RatingTMDB: 7.4,
			},
			Library: LibraryRef{ID: "movies", Name: "Movies"},
			Movie:   &Detail{Tagline: "Some journeys take a lifetime.", Cast: []CastMember{{Name: "Ada Lark"}, {Name: "June Tamsin"}}},
		},
	}
}
