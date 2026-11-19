package requests

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/requests/discoverrows"
)

// SetRecommendations wires where recommended rows read each viewer's ranked
// titles. Without it (or with outside-title recommendations off) those rows
// have no titles.
func (s *Service) SetRecommendations(r discoverrows.Recommendations) { s.recommendations = r }

// A personal row (a source implementing discoverrows.PersonalSource) is read
// from stored data only: no TMDB call, cold caches included. Its titles are
// filtered whole, before paging, so its totals and pages are exact:
//
//   - The viewer's rating ceiling applies to the stored US certification
//     with access.RatingAllowed, failing closed on a missing or unrecognized
//     one, as filterPageByCeiling does with the certifications it fetches.
//   - Titles the library has are dropped, matched by the stored TMDB, TVDB
//     and IMDb IDs, without the TMDB external-ID calls lookupAvailable makes.
//   - Request state and follows are overlaid on the page shown, as on other
//     rows.

// personalTitles returns the viewer's titles of a personal row that pass its
// rating ceiling and that the library does not have.
func (s *Service) personalTitles(ctx context.Context, viewer Viewer, source discoverrows.PersonalSource, ceiling string) ([]discoverrows.PersonalTitle, error) {
	titles, err := source.Titles(ctx, discoverrows.Viewer{UserID: viewer.UserID, ProfileID: viewer.ProfileID, Library: viewer.Library})
	if err != nil {
		return nil, err
	}
	allowed := make([]discoverrows.PersonalTitle, 0, len(titles))
	candidatesByType := map[MediaType][]PresenceCandidate{}
	for _, t := range titles {
		mediaType, err := normalizeMediaType(MediaType(t.Card.MediaType))
		if err != nil || t.Card.ID <= 0 {
			continue
		}
		if ceiling != "" && !access.RatingAllowed(t.USCertification, ceiling) {
			continue
		}
		allowed = append(allowed, t)
		candidatesByType[mediaType] = append(candidatesByType[mediaType], storedPresenceCandidate(t))
	}
	owned := map[MediaType]map[int]PresenceMatch{}
	for mediaType, candidates := range candidatesByType {
		if owned[mediaType], err = s.lookupPresence(ctx, mediaType, candidates); err != nil {
			return nil, err
		}
	}
	out := allowed[:0]
	for _, t := range allowed {
		mediaType, _ := normalizeMediaType(MediaType(t.Card.MediaType))
		if !owned[mediaType][t.Card.ID].Available {
			out = append(out, t)
		}
	}
	return out, nil
}

// storedPresenceCandidate is a personal title's library lookup with the IDs
// stored for it; lookupAvailable would fetch the TVDB and IMDb IDs from TMDB.
func storedPresenceCandidate(t discoverrows.PersonalTitle) PresenceCandidate {
	c := PresenceCandidate{TMDBID: t.Card.ID, IMDbID: t.IMDbID}
	if t.TVDBID > 0 {
		tvdbID := t.TVDBID
		c.TVDBID = &tvdbID
	}
	return c
}

// noneAvailable is the library lookup for titles already checked against
// the library.
func noneAvailable(context.Context, MediaType, []int) (map[int]PresenceMatch, error) {
	return map[int]PresenceMatch{}, nil
}

// personalRowPage returns titles [from, from+want) of a personal row with the
// viewer's request state, and how many titles the row has in all.
func (s *Service) personalRowPage(ctx context.Context, viewer Viewer, source discoverrows.PersonalSource, ceiling string, from, want int) ([]MediaResult, int, error) {
	titles, err := s.personalTitles(ctx, viewer, source, ceiling)
	if err != nil {
		return nil, 0, err
	}
	from = min(max(from, 0), len(titles))
	shown := titles[from:min(from+max(want, 0), len(titles))]
	raw := &tmdb.MediaPage{Results: make([]tmdb.MediaResult, 0, len(shown))}
	reasons := map[certKey][]discoverrows.Reason{}
	for _, t := range shown {
		raw.Results = append(raw.Results, t.Card)
		mediaType, _ := normalizeMediaType(MediaType(t.Card.MediaType))
		reasons[certKey{mediaType: mediaType, id: t.Card.ID}] = t.Reasons
	}
	page, err := s.overlayViewerState(ctx, viewer, raw, noneAvailable)
	if err != nil {
		return nil, 0, err
	}
	for i := range page.Results {
		r := &page.Results[i]
		r.Reasons = reasons[certKey{mediaType: r.MediaType, id: r.TMDBID}]
	}
	return page.Results, len(titles), nil
}

// personalPageCount is how many see all pages a personal row of total titles
// has.
func personalPageCount(total int) int {
	return (total + discoverrows.PageSize - 1) / discoverrows.PageSize
}

// firstPersonalRowPage renders a personal row for the Discover page: at most
// its limit of titles. A trimmed row has no cursor, as on other rows; a row
// shown whole over several pages points past its last page.
func (s *Service) firstPersonalRowPage(ctx context.Context, viewer Viewer, row discoverrows.Row, source discoverrows.PersonalSource, ceiling string) (*DiscoverySection, error) {
	results, total, err := s.personalRowPage(ctx, viewer, source, ceiling, 0, row.Limit)
	if err != nil {
		return nil, err
	}
	pages := personalPageCount(total)
	nextPage := 0
	if len(results) == total && pages > 1 {
		nextPage = pages + 1
	}
	return discoverySectionOf(row, &MediaPage{Page: 1, TotalPages: pages, TotalResults: total, Results: results}, nextPage), nil
}

// personalSeeAllPage is one see all page of a personal row: PageSize titles
// a page, page+1 following.
func (s *Service) personalSeeAllPage(ctx context.Context, viewer Viewer, row discoverrows.Row, source discoverrows.PersonalSource, ceiling string, page int) (*DiscoverySection, error) {
	page = max(page, 1)
	results, total, err := s.personalRowPage(ctx, viewer, source, ceiling, (page-1)*discoverrows.PageSize, discoverrows.PageSize)
	if err != nil {
		return nil, err
	}
	return discoverySectionOf(row, &MediaPage{Page: page, TotalPages: personalPageCount(total), TotalResults: total, Results: results}, 0), nil
}
