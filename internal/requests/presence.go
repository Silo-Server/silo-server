package requests

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type PresenceCandidate struct {
	TMDBID int
	TVDBID *int
	IMDbID string
}

type PresenceMatch struct {
	Available       bool
	ContentID       string
	MatchedProvider string
}

type PresenceResolver interface {
	Lookup(ctx context.Context, mediaType MediaType, candidates []PresenceCandidate) (map[int]PresenceMatch, error)
}

type presenceItemLookup interface {
	LookupExternalIDs(ctx context.Context, mediaType string, candidates []catalog.ExternalIDLookupCandidate) ([]catalog.ExternalIDMatchRow, error)
}

type tmdbBackfiller interface {
	AttachTMDBID(ctx context.Context, contentID, itemType string, tmdbID int) error
}

type CatalogPresence struct {
	items        presenceItemLookup
	tmdbBackfill tmdbBackfiller
}

func NewCatalogPresence(items *catalog.ItemRepository, providerIDs ...*catalog.ProviderIDRepository) *CatalogPresence {
	var itemLookup presenceItemLookup
	if items != nil {
		itemLookup = items
	}
	var backfill tmdbBackfiller
	if len(providerIDs) > 0 && providerIDs[0] != nil {
		backfill = providerIDs[0]
	}
	return &CatalogPresence{items: itemLookup, tmdbBackfill: backfill}
}

func (p *CatalogPresence) Lookup(ctx context.Context, mediaType MediaType, candidates []PresenceCandidate) (map[int]PresenceMatch, error) {
	out := map[int]PresenceMatch{}
	if p == nil || p.items == nil || len(candidates) == 0 {
		return out, nil
	}

	lookupCandidates := make([]catalog.ExternalIDLookupCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.TMDBID <= 0 {
			continue
		}
		row := catalog.ExternalIDLookupCandidate{
			TMDBID: strconv.Itoa(candidate.TMDBID),
			IMDbID: strings.TrimSpace(candidate.IMDbID),
		}
		if candidate.TVDBID != nil && *candidate.TVDBID > 0 {
			row.TVDBID = strconv.Itoa(*candidate.TVDBID)
		}
		lookupCandidates = append(lookupCandidates, row)
	}
	if len(lookupCandidates) == 0 {
		return out, nil
	}

	rows, err := p.items.LookupExternalIDs(ctx, string(mediaType), lookupCandidates)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		id, err := strconv.Atoi(row.QueryTMDBID)
		if err != nil || id <= 0 {
			continue
		}
		out[id] = PresenceMatch{
			Available:       true,
			ContentID:       row.MediaID,
			MatchedProvider: row.MatchedProvider,
		}
		if row.MatchedProvider != "tmdb" && p.tmdbBackfill != nil {
			if err := p.tmdbBackfill.AttachTMDBID(ctx, row.MediaID, string(mediaType), id); err != nil {
				slog.WarnContext(ctx, "requests: failed to backfill tmdb id from presence lookup", "component", "requests",
					"content_id", row.MediaID,
					"media_type", mediaType,
					"tmdb_id", id,
					"matched_provider", row.MatchedProvider,
					"error", err)
			}
		}
	}
	return out, nil
}

func (p *CatalogPresence) LookupTMDB(ctx context.Context, mediaType MediaType, tmdbIDs []int) (map[int]bool, error) {
	candidates := make([]PresenceCandidate, 0, len(tmdbIDs))
	for _, id := range tmdbIDs {
		if id > 0 {
			candidates = append(candidates, PresenceCandidate{TMDBID: id})
		}
	}
	matches, err := p.Lookup(ctx, mediaType, candidates)
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for id, match := range matches {
		out[id] = match.Available
	}
	return out, nil
}
