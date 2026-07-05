package notifications

import (
	"fmt"
	"strings"
)

// Discord embed metadata helpers shared by the per-profile webhook/DM
// builders and the server-channel builders. Everything here is pure and may
// only ever emit public provider origins (TMDB, IMDb, TVDB and their image
// CDNs) — never the user's own server origin (docs/superpowers/plans/
// notifications/04, "Server URL leakage").

// discordOverviewLimit clips overviews well below Discord's 4096-char
// description cap: a full synopsis crowds the embed; a teaser reads better.
const discordOverviewLimit = 350

// Media-type discriminator values shared by catalog rows (media_items.type)
// and request payloads.
const (
	mediaTypeMovie     = "movie"
	mediaTypeSeries    = "series"
	mediaTypeAudiobook = "audiobook"
	mediaTypeEbook     = "ebook"
)

// providerIDs carries the external database identifiers an embed can link to.
// MediaType distinguishes the movie/series URL forms ("movie" | "series").
type providerIDs struct {
	MediaType string
	IMDB      string
	TMDB      string
	TVDB      string
}

func (ids providerIDs) tmdbURL() string {
	if ids.TMDB == "" {
		return ""
	}
	kind := mediaTypeMovie
	if ids.MediaType == mediaTypeSeries {
		kind = "tv"
	}
	return "https://www.themoviedb.org/" + kind + "/" + ids.TMDB
}

func (ids providerIDs) imdbURL() string {
	if ids.IMDB == "" {
		return ""
	}
	return "https://www.imdb.com/title/" + ids.IMDB + "/"
}

func (ids providerIDs) tvdbURL() string {
	if ids.TVDB == "" {
		return ""
	}
	kind := mediaTypeMovie
	if ids.MediaType == mediaTypeSeries {
		kind = mediaTypeSeries
	}
	return "https://thetvdb.com/dereferrer/" + kind + "/" + ids.TVDB
}

// titleURL picks the embed title's click-through link, preferring TMDB (the
// richest public page for both movies and series).
func (ids providerIDs) titleURL() string {
	for _, url := range []string{ids.tmdbURL(), ids.imdbURL(), ids.tvdbURL()} {
		if url != "" {
			return url
		}
	}
	return ""
}

// linkLine renders the external database links as one markdown line:
// "[TMDB](…) • [IMDb](…) • [TVDB](…)". Empty when no IDs are known.
func (ids providerIDs) linkLine() string {
	links := make([]string, 0, 3)
	if url := ids.tmdbURL(); url != "" {
		links = append(links, "[TMDB]("+url+")")
	}
	if url := ids.imdbURL(); url != "" {
		links = append(links, "[IMDb]("+url+")")
	}
	if url := ids.tvdbURL(); url != "" {
		links = append(links, "[TVDB]("+url+")")
	}
	return strings.Join(links, " • ")
}

// publicArtworkURL maps a stored artwork path to a public provider CDN URL,
// or "" when no such URL exists. Plugin-scheme paths mirror the plugins'
// own resolvers; verbatim http(s) paths are provider-supplied external URLs.
// Locally cached artwork (bare storage keys) deliberately yields "" so embeds
// never name the server's storage origin.
func publicArtworkURL(path string) string {
	switch {
	case strings.HasPrefix(path, "tmdb://"):
		// tmdb://poster/abc.jpg → CDN file abc.jpg; w500 suits thumbnails.
		if _, file, ok := strings.Cut(strings.TrimPrefix(path, "tmdb://"), "/"); ok && file != "" {
			return "https://image.tmdb.org/t/p/w500/" + file
		}
		return ""
	case strings.HasPrefix(path, "tvdb://"):
		return "https://artworks.thetvdb.com/" + strings.TrimPrefix(path, "tvdb://")
	case strings.HasPrefix(path, "http://"), strings.HasPrefix(path, "https://"):
		return path
	default:
		return ""
	}
}

// embedPosterURL resolves the best public poster URL for an embed: the
// stored poster path when it is provider-origin, else the provider source
// path preserved by image caching (empty when neither resolves publicly).
func embedPosterURL(posterPath, posterSourcePath string) string {
	if url := publicArtworkURL(posterPath); url != "" {
		return url
	}
	return publicArtworkURL(posterSourcePath)
}

// tmdbRawImageURL renders a raw TMDB image path ("/abc.jpg", as stored on
// media requests) as a public CDN URL.
func tmdbRawImageURL(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "https://image.tmdb.org/t/p/w500" + path
}

// ratingLabel renders "★ 8.4 IMDb", preferring IMDb's score; "" when neither
// rating is known.
func ratingLabel(imdb, tmdb float64) string {
	switch {
	case imdb > 0:
		return fmt.Sprintf("★ %.1f IMDb", imdb)
	case tmdb > 0:
		return fmt.Sprintf("★ %.1f TMDB", tmdb)
	default:
		return ""
	}
}

// genresLabel renders up to three genres as a comma-separated line.
func genresLabel(genres []string) string {
	kept := make([]string, 0, 3)
	for _, genre := range genres {
		if genre = strings.TrimSpace(genre); genre != "" {
			kept = append(kept, genre)
		}
		if len(kept) == 3 {
			break
		}
	}
	return strings.Join(kept, ", ")
}

// overviewSnippet clips an overview to the embed teaser length, ending on a
// word boundary so the cut reads naturally.
func overviewSnippet(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= discordOverviewLimit {
		return text
	}
	clipped := strings.TrimSuffix(truncateWithEllipsis(text, discordOverviewLimit), "…")
	if at := strings.LastIndexByte(clipped, ' '); at > discordOverviewLimit/2 {
		clipped = clipped[:at]
	}
	return strings.TrimRight(clipped, " ,.;:") + "…"
}

// titleWithYear appends the release year when known: "Dune (2021)".
func titleWithYear(title string, year int) string {
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

// embedDescription joins the overview teaser and the provider link line.
func embedDescription(overview string, ids providerIDs) string {
	parts := make([]string, 0, 2)
	if snippet := overviewSnippet(overview); snippet != "" {
		parts = append(parts, snippet)
	}
	if links := ids.linkLine(); links != "" {
		parts = append(parts, links)
	}
	return strings.Join(parts, "\n\n")
}
