package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/collage"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TMDBCollectionEntry is a lightweight TMDB preset result used by the collection sync.
type TMDBCollectionEntry struct {
	ID        int
	MediaType string
	Title     string
	IMDbID    string
	TVDBID    int
}

// TraktCollectionEntry is a lightweight Trakt discovery result used by collection sync.
type TraktCollectionEntry struct {
	TraktID   int
	TMDBID    int
	TVDBID    int
	IMDbID    string
	MediaType string
	Title     string
	Year      int
	Rank      int
}

// TMDBCollectionFetcher abstracts the TMDB preset API so the catalog package
// does not import the tmdb package directly.
type TMDBCollectionFetcher interface {
	GetCollectionPreset(ctx context.Context, preset, mediaType, timeWindow string, limit int) ([]TMDBCollectionEntry, error)
}

// TMDBCollectionByIDFetcher abstracts TMDB's /collection/{id} franchise/saga
// endpoint. It returns the curated, ordered list of parts in a TMDB
// collection (e.g. all MCU films in MCU collection 86311), already enriched
// with external IDs so the matcher can fall back to IMDb / TVDB when the
// local catalog doesn't carry a TMDB ID.
//
// Implementations are expected to deal with pagination internally — TMDB
// collection responses are single-page, so this is currently trivial.
type TMDBCollectionByIDFetcher interface {
	GetCollection(ctx context.Context, id int) ([]TMDBCollectionEntry, error)
}

// TMDBDiscoverParams mirrors templates.TMDBDiscoverSpec so the catalog package
// can hand discover params to the fetcher without importing the tmdb package
// directly.
type TMDBDiscoverParams struct {
	WithGenres       []int
	WithoutGenres    []int
	SortBy           string
	VoteCountGte     int
	VoteAverageGte   float64
	ReleaseDateGte   string
	ReleaseDateLte   string
	Certifications   []string
	CertificationLte string
	WithRuntimeGte   int
	WithRuntimeLte   int
	OriginalLanguage string
}

// TMDBDiscoverFetcher abstracts TMDB's `/discover/{movie,tv}` endpoint so the
// catalog package does not import the tmdb package directly.
type TMDBDiscoverFetcher interface {
	Discover(ctx context.Context, mediaType string, params TMDBDiscoverParams, limit int) ([]TMDBCollectionEntry, error)
}

// TraktCollectionFetcher abstracts the Trakt discovery API.
type TraktCollectionFetcher interface {
	GetCollectionPreset(ctx context.Context, preset, mediaType string, limit int, accessToken string) ([]TraktCollectionEntry, error)
}

// TraktAccessTokenResolver returns a profile-scoped Trakt access token for
// personalized recommendation collection sync.
type TraktAccessTokenResolver interface {
	ResolveTraktAccessToken(ctx context.Context, profileID string) (string, error)
}

// CollageGenerator generates a poster collage for a collection.
// It receives the collection ID, resolves item poster images, composes them,
// and stores the result. Returns the S3 path and thumbhash of the generated poster.
type CollageGenerator interface {
	GenerateCollectionPoster(ctx context.Context, collectionID string) error
}

var ErrLibraryCollectionSyncUnsupported = errors.New("smart collections cannot be synchronized")

type LibraryCollectionService struct {
	collections  *LibraryCollectionRepository
	items        *ItemRepository
	libraryItems *LibraryItemRepository
	httpClient   *http.Client

	// TMDBCollections is nil when TMDB is not configured.
	TMDBCollections TMDBCollectionFetcher

	// TMDBFranchises is nil when TMDB is not configured. It serves the
	// `tmdb_collection` source mode (curated franchises / sagas).
	TMDBFranchises TMDBCollectionByIDFetcher

	// TMDBDiscovers is nil when TMDB is not configured. It serves the
	// `tmdb_discover` source mode (genre matrices, decade filters, etc.).
	TMDBDiscovers TMDBDiscoverFetcher

	// TraktCollections is nil when Trakt collection discovery is not configured.
	TraktCollections TraktCollectionFetcher

	// TraktTokenResolver is required for Trakt recommended collections.
	TraktTokenResolver TraktAccessTokenResolver

	// CollageGen is nil when S3/image processing is not configured.
	CollageGen CollageGenerator
}

func NewLibraryCollectionService(
	collections *LibraryCollectionRepository,
	items *ItemRepository,
	libraryItems *LibraryItemRepository,
	httpClient *http.Client,
) *LibraryCollectionService {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &LibraryCollectionService{
		collections:  collections,
		items:        items,
		libraryItems: libraryItems,
		httpClient:   httpClient,
	}
}

// CollectionBuilders holds per-builder item limits for synced collections.
type CollectionBuilders struct {
	MDBList int `json:"mdblist,omitempty"`
}

type SyncCollectionOptions struct {
	SkipCollage bool
}

type libraryCollectionSourceConfig struct {
	Mode       string              `json:"mode"`
	Provider   string              `json:"provider,omitempty"`
	Preset     string              `json:"preset,omitempty"`
	URL        string              `json:"url,omitempty"`
	MediaType  string              `json:"media_type,omitempty"`
	TimeWindow string              `json:"time_window,omitempty"`
	ProfileID  string              `json:"profile_id,omitempty"`
	Limit      *int                `json:"limit,omitempty"`
	Builders   *CollectionBuilders `json:"builders,omitempty"`
	// CollectionID is the TMDB collection ID for the `tmdb_collection` mode.
	// Stored as a plain int (not *int) so zero round-trips as "unset" via the
	// omitempty tag — the sync path treats 0 as a placeholder sentinel.
	CollectionID int `json:"collection_id,omitempty"`

	// Discover holds the TMDB /discover parameters for the `tmdb_discover`
	// mode. The pointer lets the JSON omit the object entirely for non-
	// discover modes so existing tmdb_preset / trakt_preset / mdblist_json
	// configs round-trip unchanged.
	Discover *libraryCollectionDiscoverConfig `json:"discover,omitempty"`
}

// libraryCollectionDiscoverConfig persists TMDB /discover filters in the
// collection's source_config JSON. Field names follow the TMDBDiscoverSpec
// JSON contract so the on-disk shape matches the template spec.
type libraryCollectionDiscoverConfig struct {
	WithGenres       []int    `json:"with_genres,omitempty"`
	WithoutGenres    []int    `json:"without_genres,omitempty"`
	SortBy           string   `json:"sort_by"`
	VoteCountGte     int      `json:"vote_count_gte,omitempty"`
	VoteAverageGte   float64  `json:"vote_average_gte,omitempty"`
	ReleaseDateGte   string   `json:"release_date_gte,omitempty"`
	ReleaseDateLte   string   `json:"release_date_lte,omitempty"`
	Certifications   []string `json:"certifications,omitempty"`
	CertificationLte string   `json:"certification_lte,omitempty"`
	WithRuntimeGte   int      `json:"with_runtime_gte,omitempty"`
	WithRuntimeLte   int      `json:"with_runtime_lte,omitempty"`
	OriginalLanguage string   `json:"original_language,omitempty"`
}

type mdblistEntry struct {
	ID          int    `json:"id"`
	Rank        int    `json:"rank"`
	TVDBID      *int   `json:"tvdbid"`
	IMDbID      string `json:"imdb_id"`
	MediaType   string `json:"mediatype"`
	Title       string `json:"title"`
	ReleaseYear int    `json:"release_year"`
}

func (s *LibraryCollectionService) SyncCollection(ctx context.Context, collectionID string) (*models.LibraryCollectionSyncRun, error) {
	return s.SyncCollectionWithOptions(ctx, collectionID, SyncCollectionOptions{})
}

func (s *LibraryCollectionService) SyncCollectionWithOptions(ctx context.Context, collectionID string, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	collection, err := s.collections.GetByID(ctx, collectionID)
	if err != nil {
		return nil, err
	}
	if IsLiveQueryType(collection.CollectionType) {
		return nil, ErrLibraryCollectionSyncUnsupported
	}

	var source libraryCollectionSourceConfig
	if err := json.Unmarshal(collection.SourceConfig, &source); err != nil {
		return nil, fmt.Errorf("parsing collection source config: %w", err)
	}

	switch source.Mode {
	case "smart":
		return nil, ErrLibraryCollectionSyncUnsupported
	case "mdblist_json":
		return s.syncMDBListCollection(ctx, collection, collectionutil.MDBListURLCandidates(source.URL, collection.SourceURL), source.Limit, opts)
	case "tmdb_preset":
		return s.syncTMDBPresetCollection(ctx, collection, source, opts)
	case "tmdb_collection":
		return s.syncTMDBFranchiseCollection(ctx, collection, source, opts)
	case "tmdb_discover":
		return s.syncTMDBDiscoverCollection(ctx, collection, source, opts)
	case "trakt_preset":
		return s.syncTraktPresetCollection(ctx, collection, source, opts)
	default:
		return nil, fmt.Errorf("unsupported collection sync mode: %s", source.Mode)
	}
}

func (s *LibraryCollectionService) syncMDBListCollection(ctx context.Context, collection *models.LibraryCollection, listURLs []string, limit *int, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	startedAt := syncTimestamp()

	if len(listURLs) == 0 {
		return nil, fmt.Errorf("mdblist sync: url is required")
	}
	entries, err := collectionutil.FetchMDBListWithFallback(listURLs, func(listURL string) ([]mdblistEntry, error) {
		return s.fetchMDBListEntries(ctx, listURL)
	})
	if err != nil {
		return nil, err
	}

	// Trim the entry list to the same fetch-multiplier bound used for TMDB and
	// Trakt sources before building the external-ID batches. MDBList lists can
	// return hundreds of entries; without this, the two GetByExternalIDs IN
	// arrays balloon to the full list size even when the user's limit is small.
	if fetchLimit := collectionutil.SourceFetchLimit(limit); fetchLimit > 0 && len(entries) > fetchLimit {
		entries = entries[:fetchLimit]
	}

	// Pre-fetch all external-ID lookups grouped by item type (movie vs series)
	// in two batched queries instead of up to 3×N GetByExternalID calls
	// (audit 2026-05-01 §3.7).
	var movieBatch, seriesBatch ExternalIDBatch
	for _, entry := range entries {
		itemType := mdbListEntryItemType(entry)
		if entry.ID > 0 {
			idStr := fmt.Sprintf("%d", entry.ID)
			if itemType == "movie" {
				movieBatch.TMDBIDs = append(movieBatch.TMDBIDs, idStr)
			} else {
				seriesBatch.TMDBIDs = append(seriesBatch.TMDBIDs, idStr)
			}
		}
		if entry.IMDbID != "" {
			if itemType == "movie" {
				movieBatch.IMDbIDs = append(movieBatch.IMDbIDs, entry.IMDbID)
			} else {
				seriesBatch.IMDbIDs = append(seriesBatch.IMDbIDs, entry.IMDbID)
			}
		}
		if entry.TVDBID != nil && *entry.TVDBID > 0 && itemType != "movie" {
			seriesBatch.TVDBIDs = append(seriesBatch.TVDBIDs, fmt.Sprintf("%d", *entry.TVDBID))
		}
	}

	movieLookup, err := s.items.GetByExternalIDs(ctx, movieBatch, "movie")
	if err != nil {
		return nil, err
	}
	seriesLookup, err := s.items.GetByExternalIDs(ctx, seriesBatch, "series")
	if err != nil {
		return nil, err
	}

	// First pass: collect ALL candidate content_ids per entry in priority
	// order. The legacy resolveMDBListEntry walked every external-ID hit and
	// returned the first library-resident match, so we must keep the full
	// candidate list (not just the highest-priority hit) for the membership
	// check below.
	type resolvedEntry struct {
		entryIndex int
		candidates []string
		sourceRank int
	}
	resolved := make([]resolvedEntry, 0, len(entries))
	candidateIDs := make([]string, 0, len(entries))
	candidateSet := make(map[string]struct{}, len(entries))
	matchedItems := make([]LibraryCollectionItemInput, 0, len(entries))
	warnings := make([]string, 0)

	for index, entry := range entries {
		itemType := mdbListEntryItemType(entry)
		var lookup *ExternalIDLookup
		if itemType == "movie" {
			lookup = movieLookup
		} else {
			lookup = seriesLookup
		}
		candidates := pickCandidatesByPriority(lookup, entry, itemType)
		if len(candidates) == 0 {
			continue
		}
		sourceRank := entry.Rank
		if sourceRank <= 0 {
			sourceRank = index + 1
		}
		resolved = append(resolved, resolvedEntry{entryIndex: index, candidates: candidates, sourceRank: sourceRank})
		for _, candidate := range candidates {
			if _, exists := candidateSet[candidate]; !exists {
				candidateSet[candidate] = struct{}{}
				candidateIDs = append(candidateIDs, candidate)
			}
		}
	}

	// Single batched library membership query covering EVERY candidate across
	// all entries (preserves the libraryID filter from the legacy
	// resolveMDBListEntry).
	libraryMembers, err := s.libraryItems.GetItemsInFolders(ctx, candidateIDs, collection.LibraryIDs)
	if err != nil {
		return nil, err
	}

	resolvedByIndex := make(map[int]resolvedEntry, len(resolved))
	for _, r := range resolved {
		resolvedByIndex[r.entryIndex] = r
	}

	scannedEntries := 0
	limitReached := false
	for index, entry := range entries {
		scannedEntries = index + 1
		r, ok := resolvedByIndex[index]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("No match in libraries %v for %s", collection.LibraryIDs, entry.Title))
			continue
		}
		var chosen string
		for _, candidate := range r.candidates {
			if libraryMembers[candidate] {
				chosen = candidate
				break
			}
		}
		if chosen == "" {
			warnings = append(warnings, fmt.Sprintf("No match in libraries %v for %s", collection.LibraryIDs, entry.Title))
			continue
		}
		matchedItems = append(matchedItems, LibraryCollectionItemInput{
			MediaItemID: chosen,
			Position:    len(matchedItems),
			SourceRank:  r.sourceRank,
		})
		if collectionutil.ItemLimitReached(len(matchedItems), limit) {
			limitReached = true
			break
		}
	}

	if err := s.collections.ReplaceItems(ctx, collection.ID, matchedItems); err != nil {
		return nil, err
	}

	status := "success"
	if len(warnings) > 0 {
		status = "warning"
	}
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return nil, fmt.Errorf("marshaling sync warnings: %w", err)
	}

	// Report the full source size as the denominator so operators can tell
	// "limit reached" apart from "perfect coverage of a small source"; the
	// extra clause exposes how many entries were actually scanned before the
	// break.
	message := fmt.Sprintf("Matched %d of %d entries", len(matchedItems), len(entries))
	if limitReached {
		message = fmt.Sprintf("%s (item limit reached after %d scanned)", message, scannedEntries)
	}

	completedAt := syncTimestamp()
	run, err := s.collections.RecordSyncRun(ctx, RecordLibraryCollectionSyncRunInput{
		CollectionID:   collection.ID,
		Status:         status,
		Message:        message,
		ItemsAdded:     len(matchedItems),
		ItemsRemoved:   0,
		ItemsMatched:   len(matchedItems),
		ItemsUnmatched: scannedEntries - len(matchedItems),
		Warnings:       warningsJSON,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
	})
	if err != nil {
		return nil, err
	}

	if !opts.SkipCollage {
		s.maybeGenerateCollage(ctx, collection.ID)
	}

	return run, nil
}

func (s *LibraryCollectionService) syncTMDBPresetCollection(ctx context.Context, collection *models.LibraryCollection, cfg libraryCollectionSourceConfig, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	startedAt := syncTimestamp()

	if s.TMDBCollections == nil {
		return nil, fmt.Errorf("TMDB preset sync requires configured TMDB access")
	}

	preset := cfg.Preset
	mediaType := cfg.MediaType
	timeWindow := cfg.TimeWindow
	if timeWindow == "" && preset == "trending" {
		timeWindow = "day"
	}
	if mediaType == "" {
		switch preset {
		case "trending":
			mediaType = "all"
		case "popular", "top_rated", "now_playing", "upcoming":
			mediaType = "movie"
		case "airing_today", "on_the_air":
			mediaType = "tv"
		}
	}

	fetchLimit := collectionutil.SourceFetchLimit(cfg.Limit)
	results, err := s.TMDBCollections.GetCollectionPreset(ctx, preset, mediaType, timeWindow, fetchLimit)
	if err != nil {
		return nil, fmt.Errorf("fetching TMDB preset: %w", err)
	}

	slog.Info("TMDB preset sync: fetched results",
		"collection_id", collection.ID,
		"preset", preset,
		"media_type", mediaType,
		"time_window", timeWindow,
		"count", len(results),
	)

	matchedItems := make([]LibraryCollectionItemInput, 0, len(results))
	seenContentIDs := make(map[string]int, len(results))
	warnings := make([]string, 0)
	unmatchedCount := 0
	duplicateCount := 0
	scannedEntries := 0
	limitReached := false

	for i, entry := range results {
		scannedEntries = i + 1
		item, err := s.resolveTMDBEntry(ctx, collection.LibraryIDs, entry)
		if err != nil {
			return nil, err
		}
		if item == nil {
			slog.Debug("TMDB preset sync: no match",
				"rank", i+1,
				"title", entry.Title,
				"type", entry.MediaType,
				"tmdb_id", entry.ID,
				"imdb_id", entry.IMDbID,
				"tvdb_id", entry.TVDBID,
			)
			unmatchedCount++
			warnings = append(warnings, fmt.Sprintf("No match in libraries %v for %s", collection.LibraryIDs, entry.Title))
			continue
		}
		if firstRank, exists := seenContentIDs[item.ContentID]; exists {
			slog.Debug("TMDB preset sync: duplicate match skipped",
				"rank", i+1,
				"title", entry.Title,
				"type", entry.MediaType,
				"tmdb_id", entry.ID,
				"imdb_id", entry.IMDbID,
				"tvdb_id", entry.TVDBID,
				"content_id", item.ContentID,
				"first_rank", firstRank,
			)
			duplicateCount++
			warnings = append(warnings, fmt.Sprintf("Duplicate TMDB entry for %s matched existing item %s", entry.Title, item.ContentID))
			continue
		}
		seenContentIDs[item.ContentID] = i + 1

		slog.Debug("TMDB preset sync: matched",
			"rank", i+1,
			"title", entry.Title,
			"type", entry.MediaType,
			"tmdb_id", entry.ID,
			"imdb_id", entry.IMDbID,
			"tvdb_id", entry.TVDBID,
			"content_id", item.ContentID,
		)
		matchedItems = append(matchedItems, LibraryCollectionItemInput{
			MediaItemID: item.ContentID,
			Position:    len(matchedItems),
			SourceRank:  i + 1,
		})
		if collectionutil.ItemLimitReached(len(matchedItems), cfg.Limit) {
			limitReached = true
			break
		}
	}

	slog.Info("TMDB preset sync: complete",
		"collection_id", collection.ID,
		"preset", preset,
		"matched", len(matchedItems),
		"unmatched", unmatchedCount,
		"duplicates", duplicateCount,
		"scanned", scannedEntries,
		"total", len(results),
	)

	if err := s.collections.ReplaceItems(ctx, collection.ID, matchedItems); err != nil {
		return nil, err
	}

	status := "success"
	if len(warnings) > 0 {
		status = "warning"
	}
	message := fmt.Sprintf("Matched %d of %d entries", len(matchedItems), len(results))
	if limitReached {
		message = fmt.Sprintf("%s (item limit reached after %d scanned)", message, scannedEntries)
	}
	if duplicateCount > 0 {
		message = fmt.Sprintf("%s (%d duplicates skipped)", message, duplicateCount)
	}
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return nil, fmt.Errorf("marshaling sync warnings: %w", err)
	}

	completedAt := syncTimestamp()
	run, err := s.collections.RecordSyncRun(ctx, RecordLibraryCollectionSyncRunInput{
		CollectionID:   collection.ID,
		Status:         status,
		Message:        message,
		ItemsAdded:     len(matchedItems),
		ItemsRemoved:   0,
		ItemsMatched:   len(matchedItems),
		ItemsUnmatched: unmatchedCount,
		Warnings:       warningsJSON,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
	})
	if err != nil {
		return nil, err
	}

	if !opts.SkipCollage {
		s.maybeGenerateCollage(ctx, collection.ID)
	}

	return run, nil
}

// syncTMDBFranchiseCollection populates a collection from TMDB's curated
// /collection/{id} franchise/saga endpoint. Items are written in TMDB's
// returned order (typically chronological by release date) without any
// re-sorting; the matcher resolves each part against the local library via
// TMDB / IMDb / TVDB IDs.
//
// A configured CollectionID of 0 is treated as a placeholder sentinel — the
// generic "TMDB Franchise" catalog template ships with collection_id=0 so
// admins can edit it after apply. Sync records a failed run with a clear
// message rather than fetching collection 0 (which TMDB does not have).
// validateTMDBFranchiseConfig returns a non-empty admin-facing failure message
// when the collection_id field is unsuitable for sync (0 = placeholder, < 0 =
// programmer error). Extracted as a pure helper so the message format can be
// unit-tested without spinning up the full sync stack.
func validateTMDBFranchiseConfig(collectionID int) string {
	switch {
	case collectionID == 0:
		return "TMDB franchise template requires a collection_id — edit the collection's source config and supply a real TMDB collection ID"
	case collectionID < 0:
		return fmt.Sprintf("TMDB collection_id must be > 0 (got %d)", collectionID)
	default:
		return ""
	}
}

func (s *LibraryCollectionService) syncTMDBFranchiseCollection(ctx context.Context, collection *models.LibraryCollection, cfg libraryCollectionSourceConfig, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	startedAt := syncTimestamp()

	if reason := validateTMDBFranchiseConfig(cfg.CollectionID); reason != "" {
		return s.recordFailedCollectionSync(ctx, collection.ID, startedAt, reason)
	}
	if s.TMDBFranchises == nil {
		return nil, fmt.Errorf("TMDB franchise sync requires configured TMDB access")
	}

	results, err := s.TMDBFranchises.GetCollection(ctx, cfg.CollectionID)
	if err != nil {
		return nil, fmt.Errorf("fetching TMDB collection: %w", err)
	}

	slog.Info("TMDB franchise sync: fetched results",
		"collection_id", collection.ID,
		"tmdb_collection_id", cfg.CollectionID,
		"count", len(results),
	)

	matchedItems := make([]LibraryCollectionItemInput, 0, len(results))
	seenContentIDs := make(map[string]int, len(results))
	warnings := make([]string, 0)
	unmatchedCount := 0
	duplicateCount := 0
	scannedEntries := 0
	limitReached := false

	for i, entry := range results {
		scannedEntries = i + 1
		item, err := s.resolveTMDBEntry(ctx, collection.LibraryIDs, entry)
		if err != nil {
			return nil, err
		}
		if item == nil {
			slog.Debug("TMDB franchise sync: no match",
				"rank", i+1,
				"title", entry.Title,
				"tmdb_id", entry.ID,
				"imdb_id", entry.IMDbID,
			)
			unmatchedCount++
			warnings = append(warnings, fmt.Sprintf("No match in libraries %v for %s", collection.LibraryIDs, entry.Title))
			continue
		}
		if firstRank, exists := seenContentIDs[item.ContentID]; exists {
			slog.Debug("TMDB franchise sync: duplicate match skipped",
				"rank", i+1,
				"title", entry.Title,
				"content_id", item.ContentID,
				"first_rank", firstRank,
			)
			duplicateCount++
			warnings = append(warnings, fmt.Sprintf("Duplicate TMDB entry for %s matched existing item %s", entry.Title, item.ContentID))
			continue
		}
		seenContentIDs[item.ContentID] = i + 1

		slog.Debug("TMDB franchise sync: matched",
			"rank", i+1,
			"title", entry.Title,
			"tmdb_id", entry.ID,
			"content_id", item.ContentID,
		)
		matchedItems = append(matchedItems, LibraryCollectionItemInput{
			MediaItemID: item.ContentID,
			Position:    len(matchedItems),
			SourceRank:  i + 1,
		})
		if collectionutil.ItemLimitReached(len(matchedItems), cfg.Limit) {
			limitReached = true
			break
		}
	}

	slog.Info("TMDB franchise sync: complete",
		"collection_id", collection.ID,
		"tmdb_collection_id", cfg.CollectionID,
		"matched", len(matchedItems),
		"unmatched", unmatchedCount,
		"duplicates", duplicateCount,
		"scanned", scannedEntries,
		"total", len(results),
	)

	if err := s.collections.ReplaceItems(ctx, collection.ID, matchedItems); err != nil {
		return nil, err
	}

	status := "success"
	if len(warnings) > 0 {
		status = "warning"
	}
	message := fmt.Sprintf("Matched %d of %d entries", len(matchedItems), len(results))
	if limitReached {
		message = fmt.Sprintf("%s (item limit reached after %d scanned)", message, scannedEntries)
	}
	if duplicateCount > 0 {
		message = fmt.Sprintf("%s (%d duplicates skipped)", message, duplicateCount)
	}
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return nil, fmt.Errorf("marshaling sync warnings: %w", err)
	}

	completedAt := syncTimestamp()
	run, err := s.collections.RecordSyncRun(ctx, RecordLibraryCollectionSyncRunInput{
		CollectionID:   collection.ID,
		Status:         status,
		Message:        message,
		ItemsAdded:     len(matchedItems),
		ItemsRemoved:   0,
		ItemsMatched:   len(matchedItems),
		ItemsUnmatched: unmatchedCount,
		Warnings:       warningsJSON,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
	})
	if err != nil {
		return nil, err
	}

	if !opts.SkipCollage {
		s.maybeGenerateCollage(ctx, collection.ID)
	}

	return run, nil
}

// validateTMDBDiscoverConfig returns a non-empty admin-facing failure message
// when the source_config is unsuitable for the discover sync path. Extracted
// as a pure helper so the message format can be unit-tested without spinning
// up the full sync stack.
//
// Returns the normalized media_type alongside the failure message: when the
// config is valid, the second return is the canonical media_type to use.
func validateTMDBDiscoverConfig(cfg libraryCollectionSourceConfig) (string, string) {
	if cfg.Discover == nil {
		return "TMDB discover sync requires a discover spec — edit the collection's source config", ""
	}
	mediaType := strings.TrimSpace(cfg.MediaType)
	if mediaType == "" {
		// Default to "movie" if unset so older configs without an explicit
		// media_type still run; discover collections must pick one or the
		// other on the TMDB side.
		mediaType = "movie"
	}
	if mediaType != "movie" && mediaType != "tv" {
		return fmt.Sprintf("TMDB discover sync: unsupported media_type %q", mediaType), ""
	}
	if strings.TrimSpace(cfg.Discover.SortBy) == "" {
		return "TMDB discover sync: sort_by is required", ""
	}
	return "", mediaType
}

func (s *LibraryCollectionService) syncTMDBDiscoverCollection(ctx context.Context, collection *models.LibraryCollection, cfg libraryCollectionSourceConfig, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	startedAt := syncTimestamp()

	if s.TMDBDiscovers == nil {
		return nil, fmt.Errorf("TMDB discover sync requires configured TMDB access")
	}
	reason, mediaType := validateTMDBDiscoverConfig(cfg)
	if reason != "" {
		return s.recordFailedCollectionSync(ctx, collection.ID, startedAt, reason)
	}

	params := TMDBDiscoverParams{
		WithGenres:       cfg.Discover.WithGenres,
		WithoutGenres:    cfg.Discover.WithoutGenres,
		SortBy:           cfg.Discover.SortBy,
		VoteCountGte:     cfg.Discover.VoteCountGte,
		VoteAverageGte:   cfg.Discover.VoteAverageGte,
		ReleaseDateGte:   cfg.Discover.ReleaseDateGte,
		ReleaseDateLte:   cfg.Discover.ReleaseDateLte,
		Certifications:   cfg.Discover.Certifications,
		CertificationLte: cfg.Discover.CertificationLte,
		WithRuntimeGte:   cfg.Discover.WithRuntimeGte,
		WithRuntimeLte:   cfg.Discover.WithRuntimeLte,
		OriginalLanguage: cfg.Discover.OriginalLanguage,
	}

	fetchLimit := collectionutil.SourceFetchLimit(cfg.Limit)
	results, err := s.TMDBDiscovers.Discover(ctx, mediaType, params, fetchLimit)
	if err != nil {
		return nil, fmt.Errorf("fetching TMDB discover: %w", err)
	}

	slog.Info("TMDB discover sync: fetched results",
		"collection_id", collection.ID,
		"media_type", mediaType,
		"sort_by", params.SortBy,
		"count", len(results),
	)

	matchedItems := make([]LibraryCollectionItemInput, 0, len(results))
	seenContentIDs := make(map[string]int, len(results))
	warnings := make([]string, 0)
	unmatchedCount := 0
	duplicateCount := 0
	scannedEntries := 0
	limitReached := false

	for i, entry := range results {
		scannedEntries = i + 1
		item, err := s.resolveTMDBEntry(ctx, collection.LibraryIDs, entry)
		if err != nil {
			return nil, err
		}
		if item == nil {
			slog.Debug("TMDB discover sync: no match",
				"rank", i+1,
				"title", entry.Title,
				"type", entry.MediaType,
				"tmdb_id", entry.ID,
				"imdb_id", entry.IMDbID,
				"tvdb_id", entry.TVDBID,
			)
			unmatchedCount++
			warnings = append(warnings, fmt.Sprintf("No match in libraries %v for %s", collection.LibraryIDs, entry.Title))
			continue
		}
		if firstRank, exists := seenContentIDs[item.ContentID]; exists {
			slog.Debug("TMDB discover sync: duplicate match skipped",
				"rank", i+1,
				"title", entry.Title,
				"content_id", item.ContentID,
				"first_rank", firstRank,
			)
			duplicateCount++
			warnings = append(warnings, fmt.Sprintf("Duplicate TMDB entry for %s matched existing item %s", entry.Title, item.ContentID))
			continue
		}
		seenContentIDs[item.ContentID] = i + 1

		matchedItems = append(matchedItems, LibraryCollectionItemInput{
			MediaItemID: item.ContentID,
			Position:    len(matchedItems),
			SourceRank:  i + 1,
		})
		if collectionutil.ItemLimitReached(len(matchedItems), cfg.Limit) {
			limitReached = true
			break
		}
	}

	slog.Info("TMDB discover sync: complete",
		"collection_id", collection.ID,
		"media_type", mediaType,
		"matched", len(matchedItems),
		"unmatched", unmatchedCount,
		"duplicates", duplicateCount,
		"scanned", scannedEntries,
		"total", len(results),
	)

	if err := s.collections.ReplaceItems(ctx, collection.ID, matchedItems); err != nil {
		return nil, err
	}

	status := "success"
	if len(warnings) > 0 {
		status = "warning"
	}
	message := fmt.Sprintf("Matched %d of %d entries", len(matchedItems), len(results))
	if limitReached {
		message = fmt.Sprintf("%s (item limit reached after %d scanned)", message, scannedEntries)
	}
	if duplicateCount > 0 {
		message = fmt.Sprintf("%s (%d duplicates skipped)", message, duplicateCount)
	}
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return nil, fmt.Errorf("marshaling sync warnings: %w", err)
	}

	completedAt := syncTimestamp()
	run, err := s.collections.RecordSyncRun(ctx, RecordLibraryCollectionSyncRunInput{
		CollectionID:   collection.ID,
		Status:         status,
		Message:        message,
		ItemsAdded:     len(matchedItems),
		ItemsRemoved:   0,
		ItemsMatched:   len(matchedItems),
		ItemsUnmatched: unmatchedCount,
		Warnings:       warningsJSON,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
	})
	if err != nil {
		return nil, err
	}

	if !opts.SkipCollage {
		s.maybeGenerateCollage(ctx, collection.ID)
	}

	return run, nil
}

func (s *LibraryCollectionService) syncTraktPresetCollection(ctx context.Context, collection *models.LibraryCollection, cfg libraryCollectionSourceConfig, opts SyncCollectionOptions) (*models.LibraryCollectionSyncRun, error) {
	startedAt := syncTimestamp()

	if s.TraktCollections == nil {
		return nil, fmt.Errorf("Trakt preset sync requires configured Trakt access")
	}

	preset := strings.TrimSpace(cfg.Preset)
	mediaType := strings.TrimSpace(cfg.MediaType)
	if mediaType == "" {
		mediaType = "movie"
	}
	if preset != "trending" && preset != "popular" && preset != "recommended" {
		return nil, fmt.Errorf("unsupported Trakt preset: %s", preset)
	}
	if mediaType != "movie" && mediaType != "tv" {
		return nil, fmt.Errorf("unsupported Trakt media type: %s", mediaType)
	}

	accessToken := ""
	if preset == "recommended" {
		profileID := strings.TrimSpace(cfg.ProfileID)
		if profileID == "" {
			return s.recordFailedCollectionSync(ctx, collection.ID, startedAt, "Trakt recommendations require a profile")
		}
		if s.TraktTokenResolver == nil {
			slog.Error("Trakt recommendations: token resolver not configured",
				"collection_id", collection.ID,
				"profile_id", profileID,
			)
			return s.recordFailedCollectionSync(ctx, collection.ID, startedAt, "Trakt recommendations: server is not configured for Trakt sync")
		}
		token, err := s.TraktTokenResolver.ResolveTraktAccessToken(ctx, profileID)
		if err != nil {
			slog.Error("Trakt recommendations: failed to resolve access token",
				"collection_id", collection.ID,
				"profile_id", profileID,
				"error", err,
			)
			return s.recordFailedCollectionSync(ctx, collection.ID, startedAt, fmt.Sprintf("Trakt recommendations: failed to resolve access token: %v", err))
		}
		accessToken = token
	}

	fetchLimit := collectionutil.SourceFetchLimit(cfg.Limit)
	results, err := s.TraktCollections.GetCollectionPreset(ctx, preset, mediaType, fetchLimit, accessToken)
	if err != nil {
		return nil, fmt.Errorf("fetching Trakt preset: %w", err)
	}

	slog.Info("Trakt preset sync: fetched results",
		"collection_id", collection.ID,
		"preset", preset,
		"media_type", mediaType,
		"count", len(results),
	)

	matchedItems := make([]LibraryCollectionItemInput, 0, len(results))
	seenContentIDs := make(map[string]int, len(results))
	warnings := make([]string, 0)
	unmatchedCount := 0
	duplicateCount := 0
	scannedEntries := 0
	limitReached := false

	for i, entry := range results {
		scannedEntries = i + 1
		item, err := s.resolveTraktEntry(ctx, collection.LibraryIDs, entry)
		if err != nil {
			return nil, err
		}
		if item == nil {
			unmatchedCount++
			warnings = append(warnings, fmt.Sprintf("No match in libraries %v for %s", collection.LibraryIDs, entry.Title))
			continue
		}
		if firstRank, exists := seenContentIDs[item.ContentID]; exists {
			duplicateCount++
			warnings = append(warnings, fmt.Sprintf("Duplicate Trakt entry for %s matched existing item %s", entry.Title, item.ContentID))
			slog.Info("Trakt preset sync: duplicate match skipped",
				"rank", i+1,
				"title", entry.Title,
				"content_id", item.ContentID,
				"first_rank", firstRank,
			)
			continue
		}
		seenContentIDs[item.ContentID] = i + 1
		sourceRank := entry.Rank
		if sourceRank <= 0 {
			sourceRank = i + 1
		}
		matchedItems = append(matchedItems, LibraryCollectionItemInput{
			MediaItemID: item.ContentID,
			Position:    len(matchedItems),
			SourceRank:  sourceRank,
		})
		if collectionutil.ItemLimitReached(len(matchedItems), cfg.Limit) {
			limitReached = true
			break
		}
	}

	if err := s.collections.ReplaceItems(ctx, collection.ID, matchedItems); err != nil {
		return nil, err
	}

	status := "success"
	if len(warnings) > 0 {
		status = "warning"
	}
	message := fmt.Sprintf("Matched %d of %d entries", len(matchedItems), len(results))
	if limitReached {
		message = fmt.Sprintf("%s (item limit reached after %d scanned)", message, scannedEntries)
	}
	if duplicateCount > 0 {
		message = fmt.Sprintf("%s (%d duplicates skipped)", message, duplicateCount)
	}
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return nil, fmt.Errorf("marshaling sync warnings: %w", err)
	}

	completedAt := syncTimestamp()
	run, err := s.collections.RecordSyncRun(ctx, RecordLibraryCollectionSyncRunInput{
		CollectionID:   collection.ID,
		Status:         status,
		Message:        message,
		ItemsAdded:     len(matchedItems),
		ItemsRemoved:   0,
		ItemsMatched:   len(matchedItems),
		ItemsUnmatched: unmatchedCount,
		Warnings:       warningsJSON,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
	})
	if err != nil {
		return nil, err
	}

	if !opts.SkipCollage {
		s.maybeGenerateCollage(ctx, collection.ID)
	}
	return run, nil
}

func (s *LibraryCollectionService) recordFailedCollectionSync(ctx context.Context, collectionID string, startedAt time.Time, message string) (*models.LibraryCollectionSyncRun, error) {
	warningsJSON, err := json.Marshal([]string{message})
	if err != nil {
		return nil, fmt.Errorf("marshaling sync warnings: %w", err)
	}
	return s.collections.RecordSyncRun(ctx, RecordLibraryCollectionSyncRunInput{
		CollectionID: collectionID,
		Status:       "failed",
		Message:      message,
		Warnings:     warningsJSON,
		StartedAt:    startedAt,
		CompletedAt:  syncTimestamp(),
	})
}

// resolveTMDBEntry finds a media item in the library matching a TMDB preset entry.
// It tries TMDB ID, IMDb ID, and TVDB ID (for TV shows) to maximize match rate.
func (s *LibraryCollectionService) resolveTMDBEntry(ctx context.Context, libraryIDs []int, entry TMDBCollectionEntry) (*models.MediaItem, error) {
	itemType := "movie"
	if entry.MediaType == "tv" {
		itemType = "series"
	}

	tmdbID := ""
	if entry.ID > 0 {
		tmdbID = fmt.Sprintf("%d", entry.ID)
	}
	tvdbID := ""
	if entry.TVDBID > 0 {
		tvdbID = fmt.Sprintf("%d", entry.TVDBID)
	}

	item, err := s.items.GetByExternalID(ctx, tmdbID, entry.IMDbID, tvdbID, itemType)
	if err != nil {
		if !errors.Is(err, ErrItemNotFound) {
			return nil, err
		}
		return nil, nil
	}

	membership, err := s.libraryItems.GetItemsInFolders(ctx, []string{item.ContentID}, libraryIDs)
	if err != nil {
		return nil, err
	}
	if !membership[item.ContentID] {
		return nil, nil
	}

	return item, nil
}

// resolveTraktEntry finds a local item matching a Trakt discovery entry.
func (s *LibraryCollectionService) resolveTraktEntry(ctx context.Context, libraryIDs []int, entry TraktCollectionEntry) (*models.MediaItem, error) {
	itemType := "movie"
	if entry.MediaType == "tv" {
		itemType = "series"
	}
	batch := ExternalIDBatch{}
	if entry.TMDBID > 0 {
		batch.TMDBIDs = []string{fmt.Sprintf("%d", entry.TMDBID)}
	}
	if entry.IMDbID != "" {
		batch.IMDbIDs = []string{entry.IMDbID}
	}
	if entry.TVDBID > 0 && itemType == "series" {
		batch.TVDBIDs = []string{fmt.Sprintf("%d", entry.TVDBID)}
	}
	lookup, err := s.items.GetByExternalIDs(ctx, batch, itemType)
	if err != nil {
		return nil, err
	}
	candidates := traktCandidatesByPriority(lookup, entry, itemType)
	if len(candidates) == 0 {
		return nil, nil
	}

	membership, err := s.libraryItems.GetItemsInFolders(ctx, candidates, libraryIDs)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if membership[candidate] {
			return s.items.GetByID(ctx, candidate)
		}
	}

	return nil, nil
}

func traktCandidatesByPriority(lookup *ExternalIDLookup, entry TraktCollectionEntry, itemType string) []string {
	if lookup == nil {
		return nil
	}
	var candidates []string
	seen := make(map[string]struct{}, 3)
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		candidates = append(candidates, id)
	}
	if itemType == "series" && entry.TVDBID > 0 {
		add(lookup.ByTVDB[fmt.Sprintf("%d", entry.TVDBID)])
	}
	if entry.TMDBID > 0 {
		add(lookup.ByTMDB[fmt.Sprintf("%d", entry.TMDBID)])
	}
	if entry.IMDbID != "" {
		add(lookup.ByIMDb[entry.IMDbID])
	}
	return candidates
}

func (s *LibraryCollectionService) fetchMDBListEntries(ctx context.Context, listURL string) ([]mdblistEntry, error) {
	listURL = collectionutil.NormalizeMDBListURL(listURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating mdblist request: %w", err)
	}

	res, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching mdblist list: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("mdblist request failed with status %d", res.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("reading mdblist response: %w", err)
	}

	var entries []mdblistEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("parsing mdblist response: %w", err)
	}
	return entries, nil
}

// mdbListEntryItemType normalizes an MDBList entry's media_type field to the
// internal "movie"/"series" item type taxonomy.
func mdbListEntryItemType(entry mdblistEntry) string {
	switch strings.ToLower(entry.MediaType) {
	case "show", "tv", "series":
		return "series"
	default:
		return "movie"
	}
}

// pickCandidatesByPriority returns all matching content_ids in priority order
// (highest first), deduped. The caller resolves library membership against
// this list and picks the first hit — matching the legacy resolveMDBListEntry
// fallback semantic where any library-resident candidate wins regardless of
// which external ID resolved it.
//
// Priority order: for series TVDB > TMDB > IMDb, for movies TMDB > IMDb.
func pickCandidatesByPriority(lookup *ExternalIDLookup, entry mdblistEntry, itemType string) []string {
	if lookup == nil {
		return nil
	}
	var candidates []string
	seen := make(map[string]bool, 3)
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		candidates = append(candidates, id)
	}
	if itemType == "series" && entry.TVDBID != nil && *entry.TVDBID > 0 {
		add(lookup.ByTVDB[fmt.Sprintf("%d", *entry.TVDBID)])
	}
	if entry.ID > 0 {
		add(lookup.ByTMDB[fmt.Sprintf("%d", entry.ID)])
	}
	if entry.IMDbID != "" {
		add(lookup.ByIMDb[entry.IMDbID])
	}
	return candidates
}

func slugifyCollectionTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	title = strings.ReplaceAll(title, "'", "")
	var builder strings.Builder
	lastHyphen := false
	for _, r := range title {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			builder.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

// maybeGenerateCollage triggers poster collage generation for a collection
// if no admin-uploaded poster exists. Errors are logged but never propagated
// so that sync operations are not blocked by collage failures.
func (s *LibraryCollectionService) maybeGenerateCollage(ctx context.Context, collectionID string) {
	if s.CollageGen == nil {
		return
	}

	collection, err := s.collections.GetByID(ctx, collectionID)
	if err != nil {
		slog.Warn("collage: failed to load collection", "collection_id", collectionID, "error", err)
		return
	}

	// Skip if an admin-uploaded poster exists.
	if collection.PosterURL != "" && !collection.PosterAutoGenerated {
		return
	}

	if err := s.CollageGen.GenerateCollectionPoster(ctx, collectionID); err != nil {
		if errors.Is(err, collage.ErrNotEnoughImages) {
			slog.Debug("collage: not enough images", "collection_id", collectionID)
		} else {
			slog.Warn("collage: poster generation failed", "collection_id", collectionID, "error", err)
		}
	}
}

func syncTimestamp() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}
