package watchsync

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// mediaTitleLookupTimeout bounds the title lookup. Titles are best effort and
// must not hold up a scrobble behind a slow database.
const mediaTitleLookupTimeout = 5 * time.Second

// MediaTitles is the catalog display identity of a local media item. Kind is
// the catalog's: movie, series, or episode. For an episode, Title is the
// episode's own, and Year, SeriesTitle and SeriesYear come from its series,
// as on the import side (an episode's year is its series' year). Providers
// that create titles they have not seen yet need these alongside the IDs.
type MediaTitles struct {
	Kind        string
	Title       string
	Year        int
	SeriesTitle string
	SeriesYear  int
}

type mediaTitleResolver interface {
	GetMediaTitles(ctx context.Context, mediaItemIDs []string) (map[string]MediaTitles, error)
}

// mediaTitles loads display titles by media item id. A lookup failure is
// logged and the events go out with their IDs alone, as they did before
// titles were sent. The lookup takes at most half of the time ctx has left,
// so the provider call that follows keeps the rest.
func (s *Service) mediaTitles(ctx context.Context, mediaItemIDs []string) map[string]MediaTitles {
	resolver, ok := s.repo.(mediaTitleResolver)
	if !ok || len(mediaItemIDs) == 0 {
		return nil
	}
	timeout := mediaTitleLookupTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline)/2)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	titles, err := resolver.GetMediaTitles(ctx, mediaItemIDs)
	if err != nil {
		slog.WarnContext(ctx, "failed to load media titles for watch provider events", "component", "watchsync", "error", err)
		return nil
	}
	return titles
}

// titlesFor returns the titles of an item whose event resolved to kind. An
// item that resolved to another kind gets none: an episode without provider
// IDs falls back to a movie event, and must not reach a provider as a movie
// named after the episode.
func titlesFor(titles map[string]MediaTitles, mediaItemID, kind string) (MediaTitles, bool) {
	found, ok := titles[mediaItemID]
	if !ok || !strings.EqualFold(strings.TrimSpace(kind), found.Kind) {
		return MediaTitles{}, false
	}
	return found, true
}

// withScrobbleTitles fills the display titles of a playback event that does
// not carry them yet.
func (s *Service) withScrobbleTitles(ctx context.Context, event ScrobbleEvent) ScrobbleEvent {
	if event.Title != "" || event.MediaItemID == "" {
		return event
	}
	return scrobbleWithTitles(event, s.mediaTitles(ctx, []string{event.MediaItemID}))
}

// scrobbleWithTitles fills event's display titles from titles when it does not
// carry them yet.
func scrobbleWithTitles(event ScrobbleEvent, titles map[string]MediaTitles) ScrobbleEvent {
	if event.Title != "" {
		return event
	}
	if found, ok := titlesFor(titles, event.MediaItemID, event.Kind); ok {
		event.Title, event.Year, event.SeriesTitle, event.SeriesYear = found.Title, found.Year, found.SeriesTitle, found.SeriesYear
	}
	return event
}

// startScrobbleTitles starts looking up event's display titles in the
// background and returns a function that waits for the result. The lookup
// keeps ctx's values but not its deadline, so a slow lookup costs the event
// its titles, never the event itself. It starts before the event joins its
// ordered dispatch queue, so lookups for queued events overlap instead of
// each waiting for the previous event's turn.
func (s *Service) startScrobbleTitles(ctx context.Context, event ScrobbleEvent) func() ScrobbleEvent {
	ctx = context.WithoutCancel(ctx)
	result := make(chan ScrobbleEvent, 1)
	go func() { result <- s.withScrobbleTitles(ctx, event) }()
	return sync.OnceValue(func() ScrobbleEvent { return <-result })
}

// titledWithin returns titled() if it finishes within half of the time left
// before ctx's deadline, and event without titles otherwise. A confirmed stop
// has already claimed its delivery and must keep its deadline for the
// provider call; the lookup itself carries on and is shared through titled.
func titledWithin(ctx context.Context, titled func() ScrobbleEvent, event ScrobbleEvent) ScrobbleEvent {
	deadline, ok := ctx.Deadline()
	if !ok {
		return titled()
	}
	result := make(chan ScrobbleEvent, 1)
	go func() { result <- titled() }()
	timer := time.NewTimer(time.Until(deadline) / 2)
	defer timer.Stop()
	select {
	case titledEvent := <-result:
		return titledEvent
	case <-timer.C:
		return event
	}
}

// withPlayTitles returns plays with the display titles of those that do not
// carry them yet filled in. The input slice is not modified.
func (s *Service) withPlayTitles(ctx context.Context, plays []LocalPlay) []LocalPlay {
	ids := make([]string, 0, len(plays))
	for _, play := range plays {
		if play.Title == "" && play.MediaItemID != "" {
			ids = append(ids, play.MediaItemID)
		}
	}
	titles := s.mediaTitles(ctx, ids)
	if len(titles) == 0 {
		return plays
	}
	out := make([]LocalPlay, len(plays))
	for i, play := range plays {
		if found, ok := titlesFor(titles, play.MediaItemID, play.Kind); ok && play.Title == "" {
			play.Title, play.Year, play.SeriesTitle, play.SeriesYear = found.Title, found.Year, found.SeriesTitle, found.SeriesYear
		}
		out[i] = play
	}
	return out
}
