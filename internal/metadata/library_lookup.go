package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrLibraryMetadataIncomplete marks a lookup that produced usable partial
// progress but could not finish. A caller that receives it alongside a
// non-nil result may keep the returned metadata and episodes but must treat
// the series snapshot as partial.
var ErrLibraryMetadataIncomplete = errors.New("library metadata lookup incomplete")

// defaultLibraryLookupSeasons bounds a lookup that does not ask for a
// different budget. One season per call keeps request-time work predictable
// and lets the caller resume through NextSeason.
const defaultLibraryLookupSeasons = 1

// LibraryMetadataLookupOptions scopes one resumable lookup call.
type LibraryMetadataLookupOptions struct {
	// AfterSeason is the last season the caller already incorporated. The
	// lookup only examines seasons with a larger number, which makes repeated
	// calls advance through a long series.
	AfterSeason int
	// MaxSeasons is the maximum number of seasons this call examines. Zero or
	// a negative value means defaultLibraryLookupSeasons.
	MaxSeasons int
	// PreferredProvider restricts the lookup to one provider slug, such as
	// "tvdb" or "tmdb". It lets a request override the library chain for its
	// imported season numbering. Empty keeps the configured chain.
	PreferredProvider string
}

// LibraryMetadataLookupResult is one lookup call's output.
type LibraryMetadataLookupResult struct {
	// Metadata is the accepted provider metadata, with conflicting provider
	// responses rejected as a whole.
	Metadata *MetadataResult
	// Episodes holds every well-formed episode observation gathered by this
	// call.
	Episodes []EpisodeResult
	// Seasons is the provider season list accepted by this call, including
	// season artwork. It is separate from Episodes so virtual registration can
	// persist season rows without re-querying the provider.
	Seasons []SeasonResult
	// NextSeason is the cursor to pass back as Options.AfterSeason. It is the
	// last season this call attempted, or 0 once the traversal reached the end
	// of the season list (or nothing was left to examine).
	NextSeason int
	// Complete reports that this call reached the end of the season list with
	// every examined season free of provider failures and malformed
	// observations. It describes progress, never permission to delete catalog
	// rows: partial or failed calls leave existing data untouched.
	Complete bool
	// PosterPath and BackdropPath are the artwork selected from the
	// destination library's image providers. They preserve the caller's
	// existing artwork when discovery returns nothing usable.
	PosterPath   string
	BackdropPath string
	// PosterSourcePath and BackdropSourcePath identify the original provider
	// or local source. They are set when the selected path needs the image
	// cache to become servable.
	PosterSourcePath   string
	BackdropSourcePath string
	// Images is the raw provider artwork for post-registration cache enqueue.
	Images []RemoteImage
}

// FetchLibraryMetadata resolves destination-library provider metadata for a
// movie or series. Series lookups are resumable: each call examines at most
// Options.MaxSeasons seasons after Options.AfterSeason, so a long show cannot
// starve registration or lose progress when a provider fails or the context
// expires. A non-nil result returned with an error preserves the observations
// that were valid before the failure.
func (s *MetadataService) FetchLibraryMetadata(
	ctx context.Context,
	folderID int,
	item *models.MediaItem,
	opts LibraryMetadataLookupOptions,
) (*LibraryMetadataLookupResult, error) {
	if s == nil || folderID <= 0 || item == nil || (item.Type != anchoredItemTypeMovie && item.Type != anchoredItemTypeSeries) {
		return nil, errors.New("library metadata requires a destination library and movie or series identity")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	language := s.resolveFolderLanguage(ctx, folderID)
	providerIDs, err := s.resolveSeriesRefreshProviderIDs(ctx, item, folderID, language, true, false)
	if err != nil {
		return nil, err
	}
	metadata, err := s.lookupLibraryItemMetadata(ctx, folderID, item.Type, language, providerIDs, opts.PreferredProvider)
	if err != nil {
		if metadata == nil {
			return nil, err
		}
		return &LibraryMetadataLookupResult{Metadata: metadata}, err
	}
	result := &LibraryMetadataLookupResult{Metadata: metadata}
	if item.Type == anchoredItemTypeSeries {
		err = s.lookupLibrarySeriesEpisodes(ctx, folderID, language, opts, metadata.ProviderIDs, result)
	}
	// Artwork is optional enrichment: a failed image provider must not discard
	// metadata or episodes that were already accepted.
	if image, imageErr := s.discoverLibraryItemImages(ctx, folderID, item, metadata.ProviderIDs, item.Type, language); imageErr != nil {
		slog.WarnContext(ctx, "library artwork discovery failed", "component", "metadata",
			"folder_id", folderID, "error", imageErr)
	} else {
		result.PosterPath = image.PosterPath
		result.PosterSourcePath = image.PosterSourcePath
		result.BackdropPath = image.BackdropPath
		result.BackdropSourcePath = image.BackdropSourcePath
		result.Images = image.Images
	}
	if item.Type != anchoredItemTypeSeries {
		result.Complete = true
	}
	return result, err
}

// lookupLibraryItemMetadata walks the metadata chain. Each request receives a
// clone of the accumulated identity so a provider cannot mutate the trusted
// map, and each response is checked against that accumulated identity
// immediately before it is folded. A conflicting response is rejected in
// full: neither its identity nor its fields are merged.
func (s *MetadataService) lookupLibraryItemMetadata(
	ctx context.Context,
	folderID int,
	itemType string,
	language string,
	providerIDs map[string]string,
	preferredProvider string,
) (*MetadataResult, error) {
	chain, err := s.resolveLibraryProviderChain(ctx, folderID, itemType, preferredProvider)
	if err != nil {
		return nil, err
	}
	result := &MetadataResult{ProviderIDs: maps.Clone(providerIDs)}
	for _, provider := range chain {
		if err := ctx.Err(); err != nil {
			return libraryAcceptedMetadata(result), err
		}
		metadataProvider, ok := provider.(MetadataProvider)
		if !ok {
			continue
		}
		value, err := metadataProvider.GetMetadata(ctx, MetadataRequest{
			ProviderIDs: maps.Clone(result.ProviderIDs),
			ContentType: itemType,
			Language:    language,
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return libraryAcceptedMetadata(result), ctxErr
			}
			slog.WarnContext(ctx, "library metadata provider failed", "component", "metadata",
				"provider", provider.Slug(), "error", err)
			continue
		}
		if value == nil || !value.HasMetadata {
			continue
		}
		if searchResultConflictsWithTrustedIDs(result.ProviderIDs, value.ProviderIDs) {
			slog.WarnContext(ctx, "metadata: rejecting library lookup response with conflicting identity", "component", "metadata",
				"provider", provider.Slug(), "accepted_ids", result.ProviderIDs, "candidate_ids", value.ProviderIDs)
			continue
		}
		accepted := *value
		accepted.ProviderIDs = maps.Clone(value.ProviderIDs)
		_, isIdentityHinter := provider.(IdentityHintProvider)
		foldProviderResult(result, &accepted, language, provider.Slug(), isIdentityHinter, nil)
		result.HasMetadata = true
	}
	if err := ctx.Err(); err != nil {
		return libraryAcceptedMetadata(result), err
	}
	if !result.HasMetadata {
		return nil, ErrMetadataNotFound
	}
	return result, nil
}

// libraryAcceptedMetadata drops an accumulator that never accepted metadata,
// so a failed call cannot look like partial progress.
func libraryAcceptedMetadata(result *MetadataResult) *MetadataResult {
	if result == nil || !result.HasMetadata {
		return nil
	}
	return result
}

// libraryEpisodeOutcome classifies one season's episode acquisition. It keeps
// "no provider answered" apart from "every provider answered with no rows" so
// the caller can tell a configuration gap, a provider failure, and a
// legitimately empty future season apart.
type libraryEpisodeOutcome int

const (
	// libraryEpisodesNoProvider means the episode chain had no eligible
	// provider. The season's contents are unknown, not empty.
	libraryEpisodesNoProvider libraryEpisodeOutcome = iota
	// libraryEpisodesComplete means every eligible provider answered and every
	// returned observation was well formed.
	libraryEpisodesComplete
	// libraryEpisodesPartial means usable rows were kept but at least one
	// provider failed, the context expired, or a malformed observation was
	// skipped.
	libraryEpisodesPartial
)

type librarySeasonEpisodes struct {
	episodes []EpisodeResult
	outcome  libraryEpisodeOutcome
	err      error
}

// lookupLibrarySeriesEpisodes examines the sorted positive seasons after the
// caller's cursor and fills result. Failures advance the cursor past the
// affected season so a permanently bad season cannot starve later ones; a
// context expiry keeps the cursor before the interrupted season.
func (s *MetadataService) lookupLibrarySeriesEpisodes(
	ctx context.Context,
	folderID int,
	language string,
	opts LibraryMetadataLookupOptions,
	providerIDs map[string]string,
	result *LibraryMetadataLookupResult,
) error {
	seasons, err := s.lookupLibrarySeasons(ctx, folderID, language, providerIDs, opts.PreferredProvider)
	if err != nil {
		result.NextSeason = max(opts.AfterSeason, 0)
		result.Complete = false
		return err
	}
	result.Seasons = seasons
	candidates := make([]SeasonResult, 0, len(seasons))
	for _, season := range seasons {
		if season.SeasonNumber > opts.AfterSeason {
			candidates = append(candidates, season)
		}
	}
	if len(candidates) == 0 {
		result.NextSeason = 0
		result.Complete = true
		return nil
	}
	limit := opts.MaxSeasons
	if limit <= 0 {
		limit = defaultLibraryLookupSeasons
	}
	traversalComplete := len(candidates) <= limit
	batch := candidates[:min(limit, len(candidates))]

	accumulator := make(map[episodeResultKey]*EpisodeResult)
	var failures []error
	nextSeason := max(opts.AfterSeason, 0)
	for _, season := range batch {
		if err := ctx.Err(); err != nil {
			return libraryPartialSeriesResult(result, accumulator, nextSeason, err)
		}
		outcome := s.lookupLibrarySeasonEpisodes(ctx, folderID, language, providerIDs, season, opts.PreferredProvider)
		accumulateEpisodeResults(accumulator, outcome.episodes, language)
		failures = append(failures, librarySeasonFailure(season, outcome)...)
		if err := ctx.Err(); err != nil {
			return libraryPartialSeriesResult(result, accumulator, nextSeason, err)
		}
		nextSeason = season.SeasonNumber
	}

	result.Episodes = flattenEpisodeResults(accumulator)
	if traversalComplete {
		result.NextSeason = 0
		result.Complete = len(failures) == 0
	} else {
		result.NextSeason = nextSeason
		result.Complete = false
	}
	if len(failures) > 0 {
		return errors.Join(append([]error{ErrLibraryMetadataIncomplete}, failures...)...)
	}
	return nil
}

// libraryPartialSeriesResult records the observations gathered before a
// context expiry without advancing the cursor past the interrupted season.
func libraryPartialSeriesResult(
	result *LibraryMetadataLookupResult,
	accumulator map[episodeResultKey]*EpisodeResult,
	nextSeason int,
	err error,
) error {
	result.Episodes = flattenEpisodeResults(accumulator)
	result.NextSeason = nextSeason
	result.Complete = false
	return err
}

// librarySeasonFailure reports whether a season's outcome leaves the series
// snapshot incomplete. A successful empty response is only legitimate for a
// season that has not aired yet; a released or undated season that returns no
// episodes is a lookup failure, not proof that the season is empty.
func librarySeasonFailure(season SeasonResult, outcome librarySeasonEpisodes) []error {
	switch outcome.outcome {
	case libraryEpisodesNoProvider:
		return []error{fmt.Errorf("season %d: no episode provider configured: %w", season.SeasonNumber, ErrMetadataNotFound)}
	case libraryEpisodesPartial:
		return []error{outcome.err}
	default:
		if len(outcome.episodes) == 0 && !librarySeasonIsFuture(season) {
			return []error{fmt.Errorf("season %d returned no episodes: %w", season.SeasonNumber, ErrMetadataNotFound)}
		}
		return nil
	}
}

// lookupLibrarySeasons lists the chain's seasons and reports provider
// failures instead of treating a surviving provider's partial list as
// authoritative.
// resolveLibraryProviderChain applies an explicit request-level provider
// choice. A provider enabled in the chain wins directly; a provider that the
// library chain leaves disabled for this level is still resolved from the
// enabled installation list, so a request can use TVDB or TMDB independently
// of the library's default season chain.
func (s *MetadataService) resolveLibraryProviderChain(
	ctx context.Context,
	folderID int,
	contentLevel string,
	preferredProvider string,
) ([]Provider, error) {
	preferred := strings.TrimSpace(preferredProvider)
	chain, err := s.resolveChainCached(ctx, folderID, contentLevel)
	if err != nil {
		if preferred != "" {
			return nil, err
		}
		return chain, err
	}
	if preferred == "" {
		return chain, nil
	}
	if filtered := filterProviderChain(chain, preferred); len(filtered) > 0 {
		return filtered, nil
	}
	if s.chainRepo == nil {
		return nil, fmt.Errorf("preferred metadata provider %q is not enabled: %w", preferred, ErrMetadataNotFound)
	}
	all, err := resolveEnabledProvidersByPriority(
		ctx, contentLevel, s.pluginResolver, s.chainRepo.pool, s.enabledChecker, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve preferred metadata provider %q: %w", preferred, err)
	}
	if filtered := filterProviderChain(all, preferred); len(filtered) > 0 {
		return filtered, nil
	}
	return nil, fmt.Errorf("preferred metadata provider %q is not enabled: %w", preferred, ErrMetadataNotFound)
}

func filterProviderChain(chain []Provider, preferredProvider string) []Provider {
	preferredProvider = strings.ToLower(strings.TrimSpace(preferredProvider))
	if preferredProvider == "" {
		return chain
	}
	out := make([]Provider, 0, len(chain))
	for _, provider := range chain {
		if strings.ToLower(provider.Slug()) == preferredProvider {
			out = append(out, provider)
		}
	}
	return out
}

func (s *MetadataService) lookupLibrarySeasons(
	ctx context.Context,
	folderID int,
	language string,
	providerIDs map[string]string,
	preferredProvider string,
) ([]SeasonResult, error) {
	chain, err := s.resolveLibraryProviderChain(ctx, folderID, "season", preferredProvider)
	if err != nil {
		return nil, err
	}
	accumulator := make(map[int]*SeasonResult)
	eligible, succeeded := 0, 0
	var failures []error
	for _, provider := range chain {
		episodeProvider, ok := provider.(EpisodeProvider)
		if !ok {
			continue
		}
		if _, local := provider.(IdentityHintProvider); local {
			continue
		}
		eligible++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seasons, err := episodeProvider.GetSeasons(ctx, SeasonsRequest{
			ProviderIDs: maps.Clone(providerIDs),
			ContentType: matchContentTypeSeries,
			Language:    language,
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			slog.WarnContext(ctx, "metadata: library season provider error", "component", "metadata",
				"provider", provider.Slug(), "error", err)
			failures = append(failures, fmt.Errorf("%s seasons: %w", provider.Slug(), err))
			continue
		}
		succeeded++
		accumulateSeasonResults(accumulator, seasons, language)
	}
	if succeeded == 0 {
		if eligible == 0 {
			return nil, fmt.Errorf("no season provider configured: %w", ErrLibraryMetadataIncomplete)
		}
		return nil, errors.Join(append([]error{ErrLibraryMetadataIncomplete}, failures...)...)
	}
	if len(failures) > 0 {
		return nil, errors.Join(append([]error{ErrLibraryMetadataIncomplete}, failures...)...)
	}
	return flattenSeasonResults(accumulator), nil
}

// lookupLibrarySeasonEpisodes fetches one season's episodes from every
// eligible provider and keeps well-formed observations even when a sibling
// provider fails or returns a malformed row.
func (s *MetadataService) lookupLibrarySeasonEpisodes(
	ctx context.Context,
	folderID int,
	language string,
	providerIDs map[string]string,
	season SeasonResult,
	preferredProvider string,
) librarySeasonEpisodes {
	chain, err := s.resolveLibraryProviderChain(ctx, folderID, "episode", preferredProvider)
	if err != nil {
		return librarySeasonEpisodes{outcome: libraryEpisodesPartial, err: err}
	}
	accumulator := make(map[episodeResultKey]*EpisodeResult)
	eligible, succeeded, malformed := 0, 0, 0
	var failures []error
	for _, provider := range chain {
		episodeProvider, ok := provider.(EpisodeProvider)
		if !ok {
			continue
		}
		if _, local := provider.(IdentityHintProvider); local {
			continue
		}
		eligible++
		if err := ctx.Err(); err != nil {
			return librarySeasonEpisodeFailure(accumulator, err)
		}
		episodes, err := episodeProvider.GetEpisodes(ctx, EpisodesRequest{
			ProviderIDs:  maps.Clone(providerIDs),
			SeasonNumber: season.SeasonNumber,
			Language:     language,
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return librarySeasonEpisodeFailure(accumulator, ctxErr)
			}
			slog.WarnContext(ctx, "metadata: library episode provider error", "component", "metadata",
				"provider", provider.Slug(), "season", season.SeasonNumber, "error", err)
			failures = append(failures, fmt.Errorf("%s season %d: %w", provider.Slug(), season.SeasonNumber, err))
			continue
		}
		succeeded++
		for _, episode := range episodes {
			if !validLibraryEpisodeObservation(season.SeasonNumber, episode) {
				malformed++
				continue
			}
			accumulateEpisodeResults(accumulator, []EpisodeResult{episode}, language)
		}
	}
	if eligible == 0 {
		return librarySeasonEpisodes{outcome: libraryEpisodesNoProvider}
	}
	if succeeded == 0 {
		return librarySeasonEpisodeFailure(accumulator, failures...)
	}
	if len(failures) > 0 || malformed > 0 {
		if len(failures) == 0 {
			failures = append(failures, fmt.Errorf("season %d: skipped %d malformed episode observations",
				season.SeasonNumber, malformed))
		}
		return librarySeasonEpisodeFailure(accumulator, failures...)
	}
	return librarySeasonEpisodes{episodes: flattenEpisodeResults(accumulator), outcome: libraryEpisodesComplete}
}

func librarySeasonEpisodeFailure(accumulator map[episodeResultKey]*EpisodeResult, errs ...error) librarySeasonEpisodes {
	if len(errs) == 0 {
		errs = []error{ErrLibraryMetadataIncomplete}
	}
	return librarySeasonEpisodes{
		episodes: flattenEpisodeResults(accumulator),
		outcome:  libraryEpisodesPartial,
		err:      errors.Join(errs...),
	}
}

// validLibraryEpisodeObservation rejects rows that cannot describe the
// requested season. Callers keep valid siblings and mark the season partial.
func validLibraryEpisodeObservation(seasonNumber int, episode EpisodeResult) bool {
	if episode.SeasonNumber != seasonNumber || episode.EpisodeNumber <= 0 {
		return false
	}
	if strings.TrimSpace(episode.Title) == "" {
		return false
	}
	return validLibraryEpisodeAirDate(episode.AirDate)
}

func validLibraryEpisodeAirDate(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	if _, err := time.Parse(time.DateOnly, value); err == nil {
		return true
	}
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func librarySeasonIsFuture(season SeasonResult) bool {
	airDate, err := time.Parse(time.DateOnly, strings.TrimSpace(season.AirDate))
	return err == nil && airDate.After(time.Now())
}
