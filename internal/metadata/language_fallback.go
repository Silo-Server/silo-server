package metadata

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/titleutil"
)

// A refresh asks providers for text in the library's metadata language. When
// a provider has none it returns the original-language text instead: a title
// marked TitleIsFallback, an overview or episode text marked with another
// language, a numbered stand-in episode title, or nothing at all. With a
// fallback language set (metadata.fallback_language, English by default), a
// refresh that writes the item's base row fills those gaps with text fetched
// in the fallback language, and only text neither language has stays in the
// original language.
//
// Only base-row (canonical) writes are filled. A localization row left empty
// falls back to the base row when read, and AI translation keeps treating it
// as missing, so foreign text is dropped from localization writes instead.

// fallbackFetchBudget caps the fallback fetches of one refresh, so a slow
// provider cannot use up the time the refresh needs to persist.
const fallbackFetchBudget = 30 * time.Second

// SetFallbackLanguage sets the language refreshes fall back to before the
// original language; empty turns the fallback off.
func (s *MetadataService) SetFallbackLanguage(language string) {
	s.fallbackLanguage.Store(baseMetadataLanguage(language))
}

// configuredFallbackLanguage is metadata.fallback_language, empty when off.
func (s *MetadataService) configuredFallbackLanguage() string {
	language, _ := s.fallbackLanguage.Load().(string)
	return language
}

// fallbackLanguageFor is the language a refresh in requested fetches gaps in,
// or empty when the fallback is off or is the requested language itself.
func (s *MetadataService) fallbackLanguageFor(requested string) string {
	fallback := s.configuredFallbackLanguage()
	requested = baseMetadataLanguage(requested)
	if fallback == "" || requested == "" || fallback == requested {
		return ""
	}
	return fallback
}

// isForeignText reports whether a provider marked text as being in a language
// other than requested. Unmarked text counts as the requested language.
func isForeignText(text, language, requested string) bool {
	language, requested = baseMetadataLanguage(language), baseMetadataLanguage(requested)
	return strings.TrimSpace(text) != "" && language != "" && requested != "" && language != requested
}

// itemTitleMissing reports whether the item has no title in requested: none,
// a provider's fallback title, or one marked as another language.
func itemTitleMissing(item *MetadataResult, requested string) bool {
	return item.TitleIsFallback || textMissing(item.Title, item.TitleLanguage, requested)
}

// textMissing reports whether text is empty or marked as another language.
func textMissing(text, language, requested string) bool {
	return strings.TrimSpace(text) == "" || isForeignText(text, language, requested)
}

// preferRequestedLanguageText folds one provider's text into the accumulated
// value. It fills an empty value like a fill-empty merge, and also replaces
// text marked foreign with text in the requested language, so a provider
// earlier in the chain that only had the original language does not hide a
// later provider's translation. It reports whether it replaced foreign text.
func preferRequestedLanguageText(current, currentLanguage *string, next, nextLanguage, requested string) bool {
	if strings.TrimSpace(next) == "" {
		return false
	}
	if strings.TrimSpace(*current) == "" {
		*current, *currentLanguage = next, nextLanguage
		return false
	}
	if isForeignText(*current, *currentLanguage, requested) && !isForeignText(next, nextLanguage, requested) {
		*current, *currentLanguage = next, nextLanguage
		return true
	}
	return false
}

// episodeTitleMissing reports whether an episode has no title in requested:
// it has none, its title is marked as another language, or it is a numbered
// stand-in such as "Avsnitt 3" that did not come from the library's own files.
func episodeTitleMissing(episode EpisodeResult, requested string) bool {
	return textMissing(episode.Title, episode.TitleLanguage, requested) ||
		(!episode.titleLocal && titleutil.IsNumberedEpisodeTitle(episode.Title, episode.EpisodeNumber))
}

// foldEpisodeTitle folds one provider's episode title into the accumulated one
// like preferRequestedLanguageText, except that a numbered stand-in only fills
// an empty title and gives way to a later provider's real title in the
// requested language. A title from the library's own files never gives way.
func foldEpisodeTitle(current *EpisodeResult, next EpisodeResult, requested string) {
	take := func() {
		current.Title, current.TitleLanguage, current.titleLocal = next.Title, next.TitleLanguage, next.titleLocal
	}
	nextForeign := isForeignText(next.Title, next.TitleLanguage, requested)
	switch {
	case strings.TrimSpace(next.Title) == "":
	case strings.TrimSpace(current.Title) == "":
		take()
	case current.titleLocal, titleutil.IsNumberedEpisodeTitle(next.Title, next.EpisodeNumber):
	case !nextForeign && titleutil.IsNumberedEpisodeTitle(current.Title, current.EpisodeNumber),
		!nextForeign && isForeignText(current.Title, current.TitleLanguage, requested):
		take()
	}
}

// markLocalEpisodeTitles marks episodes from the library's own files (the
// local sidecar provider), whose titles stand even when they look numbered.
func markLocalEpisodeTitles(episodes []EpisodeResult) {
	for i := range episodes {
		episodes[i].titleLocal = true
	}
}

// fallbackPolicy decides, per refresh mode, which gaps are worth a fallback
// fetch and what a gap the fetch could not fill keeps.
type fallbackPolicy struct {
	// refetchStored fetches even where the field has stored text: the
	// refresh replaces stored text (manual refresh, Identify).
	refetchStored bool
	// refetchEmpty fetches an empty value again although its row is already
	// stored without one. A scheduled refresh skips it: text no language had
	// last time rarely appears, and asking on every pass costs provider calls.
	refetchEmpty bool
	// keepStored keeps real stored text, rather than the original-language
	// text, for a gap the fetch could not fill. Not for an initial match or
	// Identify, whose stored text may belong to another title.
	keepStored bool
}

func fallbackPolicyFor(mode RefreshMode) fallbackPolicy {
	switch mode {
	case ModeManualRefresh:
		return fallbackPolicy{refetchStored: true, refetchEmpty: true, keepStored: true}
	case ModeIdentify:
		return fallbackPolicy{refetchStored: true, refetchEmpty: true}
	case ModeScheduledRefresh:
		return fallbackPolicy{keepStored: true}
	default:
		return fallbackPolicy{refetchEmpty: true}
	}
}

// fetches reports whether a field missing in the requested language is worth
// fetching. stored is the field's real stored text, empty when there is none,
// and storedRow whether the field's row is stored at all.
func (p fallbackPolicy) fetches(missing bool, current, stored string, storedRow bool) bool {
	switch {
	case !missing:
		return false
	case !p.refetchStored && stored != "":
		return false
	case !p.refetchEmpty && storedRow && strings.TrimSpace(current) == "":
		return false
	default:
		return true
	}
}

// realStoredText is stored text that is more than a stand-in: not empty, not
// "TBA", and not a numbered "Episode 3" or "Season 3" that the scanner or a
// provider wrote for want of a title.
func realStoredText(text string, number int) string {
	text = strings.TrimSpace(text)
	if titleutil.IsProvisionalEpisodeTitle(text) || titleutil.IsNumberedEpisodeTitle(text, number) {
		return ""
	}
	return text
}

// fallbackSources are the provider chains a refresh fetched with.
type fallbackSources struct {
	item, season, episode []Provider
}

// fallbackText is what a fallback-language fetch found for the gaps a refresh
// left. It is fetched before persistence takes its locks and applied once
// persistence confirms the write targets the item's base row. The text
// belongs to the provider IDs it was fetched with, so it applies when
// persistence moves the item to another content ID too.
type fallbackText struct {
	language string
	policy   fallbackPolicy
	// fillEmpty is set when the write merges fill-empty: any stored text,
	// even a stand-in, stays, so a gap with stored text is not worth a fetch.
	fillEmpty bool
	// item is nil when the item had no gap to fill.
	item     *MetadataResult
	seasons  map[int]SeasonResult
	episodes map[episodeResultKey]EpisodeResult
	// childrenOf is the series whose stored children storedSeasons and
	// storedEpisodes hold.
	childrenOf     string
	storedSeasons  map[int]SeasonResult
	storedEpisodes map[episodeResultKey]EpisodeResult
}

// canonicalMetadataLanguage decides which language the item's base row is in
// after this write, and whether the write targets the base row (true) or the
// req.Language localization row. A write that would adopt req.Language while
// both title and overview are locked does not: nothing would be rewritten,
// and restamping would mislabel the old text.
func canonicalMetadataLanguage(req ProcessRequest, existing *models.MediaItem, locked []MetadataField) (language string, canonical bool) {
	adopt := req.AdoptLanguage && (!isFieldLocked(locked, FieldName) || !isFieldLocked(locked, FieldOverview))
	language = strings.TrimSpace(req.Language)
	if !adopt && existing != nil && strings.TrimSpace(existing.DefaultMetadataLanguage) != "" {
		language = strings.TrimSpace(existing.DefaultMetadataLanguage)
	}
	if language == "" {
		language = "en"
	}
	return language, existing == nil || strings.EqualFold(req.Language, language)
}

// childGaps lists the seasons and the episodes whose text is missing in
// requested.
func childGaps(seasons []SeasonResult, episodes []EpisodeResult, requested string) (seasonGaps []SeasonResult, episodeGaps []EpisodeResult) {
	for _, season := range seasons {
		if textMissing(season.Title, season.TitleLanguage, requested) || textMissing(season.Overview, season.OverviewLanguage, requested) {
			seasonGaps = append(seasonGaps, season)
		}
	}
	for _, episode := range episodes {
		if episodeTitleMissing(episode, requested) || textMissing(episode.Overview, episode.OverviewLanguage, requested) {
			episodeGaps = append(episodeGaps, episode)
		}
	}
	return seasonGaps, episodeGaps
}

// blockingStoredText is the stored text that keeps a fetch from being worth
// it: any text under a fill-empty merge, which never replaces it, and real
// text otherwise, which a stand-in from the provider would only overwrite.
func (f *fallbackText) blockingStoredText(text string, number int) string {
	if f.fillEmpty {
		return strings.TrimSpace(text)
	}
	return realStoredText(text, number)
}

// fetchedChildGaps narrows the child gaps to those the policy fetches: any
// season, and the seasons whose episodes need one.
func (f *fallbackText) fetchedChildGaps(seasonGaps []SeasonResult, episodeGaps []EpisodeResult, requested string) (seasonGap bool, episodeSeasons []int) {
	for _, season := range seasonGaps {
		stored, storedRow := f.storedSeasons[season.SeasonNumber]
		if f.policy.fetches(textMissing(season.Title, season.TitleLanguage, requested), season.Title,
			f.blockingStoredText(stored.Title, season.SeasonNumber), storedRow) ||
			f.policy.fetches(textMissing(season.Overview, season.OverviewLanguage, requested), season.Overview,
				strings.TrimSpace(stored.Overview), storedRow) {
			seasonGap = true
			break
		}
	}
	for _, episode := range episodeGaps {
		if slices.Contains(episodeSeasons, episode.SeasonNumber) {
			continue
		}
		stored, storedRow := f.storedEpisodes[episodeResultKey{seasonNumber: episode.SeasonNumber, episodeNumber: episode.EpisodeNumber}]
		if f.policy.fetches(episodeTitleMissing(episode, requested), episode.Title,
			f.blockingStoredText(stored.Title, episode.EpisodeNumber), storedRow) ||
			f.policy.fetches(textMissing(episode.Overview, episode.OverviewLanguage, requested), episode.Overview,
				strings.TrimSpace(stored.Overview), storedRow) {
			episodeSeasons = append(episodeSeasons, episode.SeasonNumber)
		}
	}
	return seasonGap, episodeSeasons
}

// fetchFallbackText fetches, in the fallback language, the text this refresh
// is missing in req.Language. It returns nil when there is nothing to fetch:
// the fallback is off, nothing is missing, or the write will not reach the
// base row. Fetch failures only leave gaps unfilled.
func (s *MetadataService) fetchFallbackText(
	ctx context.Context,
	req ProcessRequest,
	contentType string,
	providerIDs map[string]string,
	item *MetadataResult,
	seasons []SeasonResult,
	episodes []EpisodeResult,
	sources fallbackSources,
) *fallbackText {
	language := s.fallbackLanguageFor(req.Language)
	if language == "" || req.writesFieldsOnly() || item == nil {
		return nil
	}
	titleMissing := itemTitleMissing(item, req.Language)
	overviewMissing := textMissing(item.Overview, item.OverviewLanguage, req.Language)
	seasonGaps, episodeGaps := childGaps(seasons, episodes, req.Language)
	if !titleMissing && !overviewMissing && len(seasonGaps) == 0 && len(episodeGaps) == 0 {
		return nil
	}

	var existing *models.MediaItem
	var locked []MetadataField
	if req.ContentID != "" && s.itemRepo != nil {
		stored, err := s.itemRepo.GetByID(ctx, req.ContentID)
		if err != nil {
			// Persistence loads it again and decides; without it the gaps
			// cannot be measured against stored text.
			return nil
		}
		existing, locked = stored, intSliceToFields(stored.LockedFields)
	}
	if _, canonical := canonicalMetadataLanguage(req, existing, locked); !canonical {
		return nil
	}

	fetched := &fallbackText{
		language: language, policy: fallbackPolicyFor(req.Mode),
		fillEmpty: req.Mode == ModeInitialMatch || req.Mode == ModeScheduledRefresh,
	}
	var storedTitle, storedOverview string
	if existing != nil {
		// An initial match replaces a skeleton's filename title.
		if req.Mode != ModeInitialMatch || !isSkeletonLikeStatus(existing.Status) {
			storedTitle = strings.TrimSpace(existing.Title)
		}
		storedOverview = strings.TrimSpace(existing.Overview)
		titleMissing = titleMissing && !isFieldLocked(locked, FieldName)
		overviewMissing = overviewMissing && !isFieldLocked(locked, FieldOverview)
		if len(seasonGaps) > 0 || len(episodeGaps) > 0 {
			fetched.childrenOf = existing.ContentID
			fetched.storedSeasons, fetched.storedEpisodes = s.storedChildren(ctx, existing.ContentID, seasonGaps, episodeGaps)
		}
	}
	itemGap := fetched.policy.fetches(titleMissing, item.Title, storedTitle, existing != nil) ||
		fetched.policy.fetches(overviewMissing, item.Overview, storedOverview, existing != nil)
	seasonGap, episodeSeasons := fetched.fetchedChildGaps(seasonGaps, episodeGaps, req.Language)
	if !itemGap && !seasonGap && len(episodeSeasons) == 0 {
		// Nothing to fetch; the policy may still keep stored text over
		// original-language text.
		return fetched
	}

	fetchCtx, cancel := fallbackFetchContext(ctx)
	defer cancel()
	if itemGap {
		fetched.item = fetchFallbackItem(fetchCtx, sources.item, providerIDs, contentType, language)
	}
	if seasonGap {
		fetched.seasons = fetchFallbackSeasons(fetchCtx, sources.season, providerIDs, contentType, language)
	}
	if len(episodeSeasons) > 0 {
		fetched.episodes = fetchFallbackEpisodes(fetchCtx, sources.episode, providerIDs, language, episodeSeasons)
	}
	return fetched
}

// fallbackFetchContext bounds the fallback fetches of one refresh to
// fallbackFetchBudget, or half the time the refresh has left.
func fallbackFetchContext(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := fallbackFetchBudget
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline)/2 < budget {
		budget = time.Until(deadline) / 2
	}
	return context.WithTimeout(ctx, budget)
}

// storedChildren loads the stored rows of the seasons and episodes with gaps.
// A load failure returns what loaded: an unknown stored row counts as absent.
func (s *MetadataService) storedChildren(ctx context.Context, seriesID string, seasons []SeasonResult, episodes []EpisodeResult) (map[int]SeasonResult, map[episodeResultKey]EpisodeResult) {
	storedSeasons := make(map[int]SeasonResult)
	storedEpisodes := make(map[episodeResultKey]EpisodeResult)
	if s.seasonRepo != nil && len(seasons) > 0 {
		numbers := make([]int32, 0, len(seasons))
		for _, season := range seasons {
			if catalog.FitsPostgresInteger(season.SeasonNumber) {
				numbers = append(numbers, int32(season.SeasonNumber)) //nolint:gosec // FitsPostgresInteger bounds it.
			}
		}
		if rows, err := s.seasonRepo.ListBySeriesAndNumbers(ctx, seriesID, numbers); err == nil {
			for _, row := range rows {
				storedSeasons[row.SeasonNumber] = seasonResultFromModel(row)
			}
		}
	}
	if s.episodeRepo != nil && len(episodes) > 0 {
		seasonNumbers := make([]int32, 0, len(episodes))
		episodeNumbers := make([]int32, 0, len(episodes))
		for _, episode := range episodes {
			if catalog.FitsPostgresInteger(episode.SeasonNumber) && catalog.FitsPostgresInteger(episode.EpisodeNumber) {
				seasonNumbers = append(seasonNumbers, int32(episode.SeasonNumber))    //nolint:gosec // FitsPostgresInteger bounds it.
				episodeNumbers = append(episodeNumbers, int32(episode.EpisodeNumber)) //nolint:gosec // FitsPostgresInteger bounds it.
			}
		}
		if rows, err := s.episodeRepo.ListBySeriesAndNumbers(ctx, seriesID, seasonNumbers, episodeNumbers); err == nil {
			for _, row := range rows {
				storedEpisodes[episodeResultKey{seasonNumber: row.SeasonNumber, episodeNumber: row.EpisodeNumber}] = episodeResultFromModel(row)
			}
		}
	}
	return storedSeasons, storedEpisodes
}

// remoteTextProviders yields the chain's remote text providers: the local
// sidecar provider has no other language, and enrichment providers carry no
// titles or descriptions.
func remoteTextProviders(chain []Provider) []Provider {
	out := make([]Provider, 0, len(chain))
	for _, p := range chain {
		if _, local := p.(IdentityHintProvider); local || isEnrichmentProvider(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// logFallbackFetchError records a failed fallback fetch. It never touches
// provider-ID state: a 404 in another language says nothing about identity.
// Plugin errors can carry URLs and key=value diagnostics, so the message is
// redacted the way the operational log stores it.
func logFallbackFetchError(ctx context.Context, p Provider, language, what string, err error) {
	slog.WarnContext(ctx, "metadata: fallback language fetch failed", "component", "metadata",
		"provider", p.Slug(), "language", language, "fetch", what,
		"error", logredact.RedactSecretAssignments(logredact.SanitizeURLError(err).Error()))
}

func fetchFallbackItem(ctx context.Context, chain []Provider, providerIDs map[string]string, contentType, language string) *MetadataResult {
	out := &MetadataResult{}
	for _, p := range remoteTextProviders(chain) {
		mp, ok := p.(MetadataProvider)
		if !ok {
			continue
		}
		result, err := mp.GetMetadata(ctx, MetadataRequest{ProviderIDs: providerIDs, ContentType: contentType, Language: language})
		if err != nil {
			logFallbackFetchError(ctx, p, language, "item", err)
			continue
		}
		if result == nil || !result.HasMetadata {
			continue
		}
		// The same selection a refresh in language makes, so a title alias in
		// language counts too.
		title, titleLanguage, fallback, _ := preferredMetadataResultTitle(result, language)
		if out.Title == "" && !fallback && !textMissing(title, titleLanguage, language) {
			out.Title = title
			if title == result.Title {
				out.SortTitle = result.SortTitle
			}
		}
		if out.Overview == "" && !textMissing(result.Overview, result.OverviewLanguage, language) {
			out.Overview, out.Tagline = result.Overview, result.Tagline
		}
		if out.Title != "" && out.Overview != "" {
			break
		}
	}
	return out
}

func fetchFallbackSeasons(ctx context.Context, chain []Provider, providerIDs map[string]string, contentType, language string) map[int]SeasonResult {
	out := make(map[int]SeasonResult)
	for _, p := range remoteTextProviders(chain) {
		ep, ok := p.(EpisodeProvider)
		if !ok {
			continue
		}
		seasons, err := ep.GetSeasons(ctx, SeasonsRequest{ProviderIDs: providerIDs, ContentType: contentType, Language: language})
		if err != nil {
			logFallbackFetchError(ctx, p, language, "seasons", err)
			continue
		}
		collectFallbackSeasons(out, seasons, language)
	}
	return out
}

// collectFallbackSeasons keeps, per season, the first title and overview in
// language.
func collectFallbackSeasons(out map[int]SeasonResult, seasons []SeasonResult, language string) {
	for _, season := range seasons {
		found := out[season.SeasonNumber]
		if found.Title == "" && !textMissing(season.Title, season.TitleLanguage, language) {
			found.Title = season.Title
		}
		if found.Overview == "" && !isForeignText(season.Overview, season.OverviewLanguage, language) {
			found.Overview = season.Overview
		}
		out[season.SeasonNumber] = found
	}
}

func fetchFallbackEpisodes(ctx context.Context, chain []Provider, providerIDs map[string]string, language string, seasonNumbers []int) map[episodeResultKey]EpisodeResult {
	out := make(map[episodeResultKey]EpisodeResult)
	for _, seasonNumber := range seasonNumbers {
		for _, p := range remoteTextProviders(chain) {
			ep, ok := p.(EpisodeProvider)
			if !ok {
				continue
			}
			episodes, err := ep.GetEpisodes(ctx, EpisodesRequest{ProviderIDs: providerIDs, SeasonNumber: seasonNumber, Language: language})
			if err != nil {
				logFallbackFetchError(ctx, p, language, "episodes", err)
				continue
			}
			collectFallbackEpisodes(out, episodes, language)
		}
	}
	return out
}

// collectFallbackEpisodes keeps, per episode, the first title and overview in
// language. A numbered stand-in is no title.
func collectFallbackEpisodes(out map[episodeResultKey]EpisodeResult, episodes []EpisodeResult, language string) {
	for _, episode := range episodes {
		key := episodeResultKey{seasonNumber: episode.SeasonNumber, episodeNumber: episode.EpisodeNumber}
		found := out[key]
		if found.Title == "" && strings.TrimSpace(episode.Title) != "" && !episodeTitleMissing(episode, language) {
			found.Title = episode.Title
		}
		if found.Overview == "" && !isForeignText(episode.Overview, episode.OverviewLanguage, language) {
			found.Overview = episode.Overview
		}
		out[key] = found
	}
}

// fillTargetChildText fills the gaps a targeted season or episode refresh in
// its series' base language left, fetching the same target in the fallback
// language. It reads only remote providers: the local sidecar provider has no
// other language.
func (s *MetadataService) fillTargetChildText(
	ctx context.Context,
	series *models.MediaItem,
	providerIDs map[string]string,
	folderID int,
	language string,
	seasonNumber, episodeNumber int,
	childCtx seriesChildLocalContext,
	seasons []SeasonResult,
	episodes []EpisodeResult,
	mode RefreshMode,
	mergeMode MergeMode,
) {
	fallback := s.fallbackLanguageFor(language)
	if fallback == "" {
		return
	}
	seasonGaps, episodeGaps := childGaps(seasons, episodes, language)
	if len(seasonGaps) == 0 && len(episodeGaps) == 0 {
		return
	}
	fetched := &fallbackText{
		language: fallback, policy: fallbackPolicyFor(mode),
		fillEmpty: mergeMode == MergeFillEmpty, childrenOf: series.ContentID,
	}
	fetched.storedSeasons, fetched.storedEpisodes = s.storedChildren(ctx, series.ContentID, seasonGaps, episodeGaps)
	seasonGap, episodeSeasons := fetched.fetchedChildGaps(seasonGaps, episodeGaps, language)
	fetchCtx, cancel := fallbackFetchContext(ctx)
	defer cancel()
	if seasonGap {
		if found, _, err := s.fetchTargetSeasonResults(fetchCtx, providerIDs, folderID, fallback, seasonNumber, childCtx, true); err == nil {
			fetched.seasons = make(map[int]SeasonResult)
			collectFallbackSeasons(fetched.seasons, found, fallback)
		}
	}
	if len(episodeSeasons) > 0 {
		if found, _, err := s.fetchTargetEpisodeResults(fetchCtx, providerIDs, folderID, fallback, seasonNumber, episodeNumber, childCtx, true); err == nil {
			fetched.episodes = make(map[episodeResultKey]EpisodeResult)
			collectFallbackEpisodes(fetched.episodes, found, fallback)
		}
	}
	fetched.applyToChildren(seasons, episodes, language, series.ContentID)
}

// applyToItem fills the item's gaps with fallback text. When the policy keeps
// stored text, a gap the fetch could not fill keeps the stored text rather
// than the original language; keepStoredTitle is false where the merge
// replaces the stored title anyway (a skeleton's filename title).
func (f *fallbackText) applyToItem(item *MetadataResult, requested string, existing *models.MediaItem, keepStoredTitle bool) {
	if f == nil {
		return
	}
	found := f.item
	if found == nil {
		found = &MetadataResult{}
	}
	keep := f.policy.keepStored && existing != nil
	if itemTitleMissing(item, requested) {
		switch {
		case found.Title != "":
			item.Title, item.SortTitle, item.TitleLanguage = found.Title, found.SortTitle, f.language
		case keep && keepStoredTitle && strings.TrimSpace(existing.Title) != "" && strings.TrimSpace(item.Title) != "":
			// An empty value leaves the stored title in place.
			item.Title, item.SortTitle = "", ""
		}
	}
	foreign := isForeignText(item.Overview, item.OverviewLanguage, requested)
	if foreign || strings.TrimSpace(item.Overview) == "" {
		switch {
		case found.Overview != "":
			item.Overview, item.OverviewLanguage = found.Overview, f.language
			if textMissing(item.Tagline, item.taglineLanguage, requested) {
				item.Tagline, item.taglineLanguage = found.Tagline, f.language
			}
		case foreign && keep && strings.TrimSpace(existing.Overview) != "":
			item.Overview = ""
			if isForeignText(item.Tagline, item.taglineLanguage, requested) {
				item.Tagline = ""
			}
		}
	}
}

// applyToChildren fills season and episode gaps the same way. seriesID is the
// series the write targets; stored children loaded for another series are
// not used.
func (f *fallbackText) applyToChildren(seasons []SeasonResult, episodes []EpisodeResult, requested, seriesID string) {
	if f == nil {
		return
	}
	keep := f.policy.keepStored && f.childrenOf != "" && f.childrenOf == seriesID
	for i := range seasons {
		season := &seasons[i]
		found := f.seasons[season.SeasonNumber]
		var stored SeasonResult
		if keep {
			stored = f.storedSeasons[season.SeasonNumber]
		}
		fillChildText(&season.Title, &season.TitleLanguage, textMissing(season.Title, season.TitleLanguage, requested),
			found.Title, realStoredText(stored.Title, season.SeasonNumber), f.language)
		fillChildText(&season.Overview, &season.OverviewLanguage, textMissing(season.Overview, season.OverviewLanguage, requested),
			found.Overview, stored.Overview, f.language)
	}
	for i := range episodes {
		episode := &episodes[i]
		key := episodeResultKey{seasonNumber: episode.SeasonNumber, episodeNumber: episode.EpisodeNumber}
		found := f.episodes[key]
		var stored EpisodeResult
		if keep {
			stored = f.storedEpisodes[key]
		}
		fillChildText(&episode.Title, &episode.TitleLanguage, episodeTitleMissing(*episode, requested),
			found.Title, realStoredText(stored.Title, episode.EpisodeNumber), f.language)
		fillChildText(&episode.Overview, &episode.OverviewLanguage, textMissing(episode.Overview, episode.OverviewLanguage, requested),
			found.Overview, stored.Overview, f.language)
	}
}

// fillChildText replaces missing text with fallback text. Without any, text
// that is there but not in the requested language gives way to real stored
// text: an empty value leaves the stored text in place.
func fillChildText(text, language *string, missing bool, fallback, stored, fallbackLanguage string) {
	switch {
	case !missing:
	case strings.TrimSpace(fallback) != "":
		*text, *language = fallback, fallbackLanguage
	case strings.TrimSpace(*text) != "" && strings.TrimSpace(stored) != "":
		*text, *language = "", ""
	}
}

// dropForeignChildText empties season and episode text a provider marked as
// another language, for a write to localization rows while the fallback is
// on: that text is no translation, and an empty row falls back to the base
// row when read. With the fallback off the original language is wanted, so
// the rows keep it as before.
func dropForeignChildText(seasons []SeasonResult, episodes []EpisodeResult, requested string) {
	for i := range seasons {
		dropForeignText(&seasons[i].Title, &seasons[i].TitleLanguage, requested)
		dropForeignText(&seasons[i].Overview, &seasons[i].OverviewLanguage, requested)
	}
	for i := range episodes {
		dropForeignText(&episodes[i].Title, &episodes[i].TitleLanguage, requested)
		dropForeignText(&episodes[i].Overview, &episodes[i].OverviewLanguage, requested)
	}
}

func dropForeignText(text, language *string, requested string) {
	if isForeignText(*text, *language, requested) {
		*text, *language = "", ""
	}
}
