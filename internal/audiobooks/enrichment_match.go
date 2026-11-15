package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	audiobookItemType = "audiobook"

	// maxCandidateFetches bounds how many search candidates one provider may
	// have fetched and checked for an item before it counts as unmatched there.
	maxCandidateFetches = 3
)

// audiobookMatch is what the provider chain settled on for one item. metadata
// has HasMetadata false when nothing was accepted.
type audiobookMatch struct {
	ids          map[string]string
	metadata     *metadata.MetadataResult
	providerErrs []error
}

// fetchRejection names the check a fetched record failed, or is empty.
type fetchRejection string

const (
	rejectAuthor   fetchRejection = "author"
	rejectNarrator fetchRejection = "narrator"
	// rejectNarratorConflict is a record reached through the tagged ID whose
	// narrators contradict those an accepted record already settled: two
	// providers disagree about the edition, so only the later record is
	// dropped.
	rejectNarratorConflict fetchRejection = "narrator_conflict"
	rejectTitle            fetchRejection = "title"
	rejectVolume           fetchRejection = "volume"
	rejectProviderID       fetchRejection = "provider_id"
)

// errMatchRejected stops matching when a record the item's IDs already pin
// down contradicts the item.
var errMatchRejected = errors.New("record contradicts the item")

// matchRun is the state of one item's pass over the provider chain.
type matchRun struct {
	e           *Enricher
	item        enrichmentItemRow
	providers   []metadata.Provider
	ids         map[string]string
	errs        []error
	state       []providerState // by provider index
	agreedTitle string
	merged      *metadata.MetadataResult
	// narrators are the names a record's narrators are checked against: the
	// item's narrator tags, until a record reached through the item's tagged
	// ID overrules them (noteNarrators).
	narrators []string
	// narratorsSettled marks that an accepted record has listed narrators.
	// From then on every record must agree with them: the match stores the
	// narrators of every record it merges.
	narratorsSettled bool
	// bare is the item's title without its series prefix, or "".
	bare string
	// volumeTitle is the title the volume rules read: the item's title, or
	// the series prefix's volume after the bare title.
	volumeTitle string
	// refetchOnly asks only for the records the item's stored IDs name and
	// searches no provider, for a matched book an admin refreshes.
	refetchOnly bool
}

// providerState is what one provider has answered for the item so far.
type providerState struct {
	// record is the provider's admitted record, or nil.
	record *metadata.MetadataResult
	// askedWith holds the IDs of the provider's last answered GetMetadata,
	// or nil when it has not answered yet (an error is not an answer).
	askedWith map[string]string
	// rejectedCredits marks that last answer as failing the author or
	// narrator check.
	rejectedCredits bool
	// lookupFailed marks a tagged-ASIN lookup that errored. The merge's retry
	// with the same tag-only IDs is that lookup again and gets its title check.
	lookupFailed bool
}

// answered records a GetMetadata answer from provider i.
func (r *matchRun) answered(i int, ids map[string]string, rejection fetchRejection) {
	r.state[i].askedWith = ids
	r.state[i].rejectedCredits = rejection == rejectAuthor || rejection == rejectNarrator ||
		rejection == rejectNarratorConflict
}

// findMatch picks the record an audiobook is matched to and merges the
// metadata of every provider that agrees with it.
//
// A tagged ASIN is looked up first, on providers that declare it as a lookup
// key, and accepted unless the record contradicts the item. Otherwise each
// provider is searched, and its best candidate is fetched and checked against
// the item's author and narrator. A candidate that fails is dropped and the
// next one tried, up to maxCandidateFetches. When the record behind the
// item's own tagged ID fails, the item is left unmatched: the tag and the file
// disagree, and no title match can settle which is right. Its narrators are
// the exception when its title agrees with the item's: the tagged ID names one
// edition, so it outranks a narrator tag, which taggers often fill with the
// author.
func (e *Enricher) findMatch(ctx context.Context, item enrichmentItemRow, providers []metadata.Provider) audiobookMatch {
	return e.runMatch(ctx, item, providers, false)
}

// refetchMatch fetches the records a matched item's stored IDs name, under
// the same admission rules as findMatch, without searching: a refresh must
// not move a matched book to another record.
func (e *Enricher) refetchMatch(ctx context.Context, item enrichmentItemRow, providers []metadata.Provider) audiobookMatch {
	return e.runMatch(ctx, item, providers, true)
}

func (e *Enricher) runMatch(ctx context.Context, item enrichmentItemRow, providers []metadata.Provider, refetchOnly bool) audiobookMatch {
	run := &matchRun{
		refetchOnly: refetchOnly,
		e:           e,
		item:        item,
		providers:   providers,
		ids:         maps.Clone(item.ProviderIDs),
		state:       make([]providerState, len(providers)),
		narrators:   item.Narrators,
		volumeTitle: item.Title,
	}
	var volume string
	if run.bare, volume = bareSeriesTitle(item.Title, item.SeriesName, item.SeriesPosition); run.bare != "" {
		run.volumeTitle = metadata.SeriesVolumeTitle(run.bare, volume)
	}
	if run.ids == nil {
		run.ids = map[string]string{}
	}
	err := run.lookupTaggedASIN(ctx)
	for i := range providers {
		if err != nil {
			break
		}
		err = run.searchProvider(ctx, i)
	}
	if err == nil {
		err = run.merge(ctx)
	}
	if err != nil {
		return audiobookMatch{ids: run.ids, metadata: &metadata.MetadataResult{ProviderIDs: run.ids}, providerErrs: run.errs}
	}
	return audiobookMatch{ids: run.ids, metadata: run.merged, providerErrs: run.errs}
}

// lookupTaggedASIN asks each metadata provider for the item's tagged ASIN.
// Only a provider that declares asin in lookup_provider_ids answers; the
// others are reached through search, where the ASIN record ranks first.
func (r *matchRun) lookupTaggedASIN(ctx context.Context) error {
	if strings.TrimSpace(r.item.ProviderIDs["asin"]) == "" {
		return nil
	}
	for i, p := range r.providers {
		mp, ok := p.(metadata.MetadataProvider)
		if !ok {
			continue
		}
		// Ask with the tag alone: an ID an earlier provider added could
		// name a different record, which the tag would then be blamed for.
		tagged := maps.Clone(r.item.ProviderIDs)
		result, err := mp.GetMetadata(ctx, r.metadataRequest(tagged))
		if err != nil {
			// Not recorded as asked: the merge retries a failed lookup.
			r.state[i].lookupFailed = true
			r.errs = append(r.errs, fmt.Errorf("%s asin lookup: %w", p.Slug(), err))
			continue
		}
		if result == nil || !result.HasMetadata {
			r.answered(i, tagged, "")
			continue
		}
		admitted, rejection, err := r.admitFetched(ctx, tagged, result, true)
		r.answered(i, tagged, rejection)
		if err != nil {
			r.errs = append(r.errs, fmt.Errorf("%s asin lookup identity admission: %w", p.Slug(), err))
			continue
		}
		if rejection == rejectNarratorConflict {
			r.logRejected(ctx, p, rejection, "skip: contradicts an earlier provider's narrators")
			continue
		}
		if rejection != "" {
			r.logRejected(ctx, p, rejection, "leave unmatched: tagged asin record")
			return errMatchRejected
		}
		// The record agrees with the tag; it must also agree with the IDs an
		// earlier provider's record added, or two providers' cross-IDs for
		// different records would be merged.
		if r.anyRecord() {
			admitted, rejection, err = r.admitFetched(ctx, r.ids, result, true)
			if err != nil {
				r.errs = append(r.errs, fmt.Errorf("%s asin lookup identity admission: %w", p.Slug(), err))
				continue
			}
			if rejection != "" {
				r.logRejected(ctx, p, rejection, "skip: contradicts an earlier provider's record")
				continue
			}
		}
		r.state[i].record = admitted
		r.noteNarrators(ctx, p, admitted)
		maps.Copy(r.ids, admitted.ProviderIDs)
		if r.agreedTitle == "" {
			r.agreedTitle = agreeingTitle(r.item.Title, admitted)
		}
	}
	return nil
}

// searchProvider searches provider i and fetches its best candidates in turn
// until one agrees with the item.
func (r *matchRun) searchProvider(ctx context.Context, i int) error {
	p := r.providers[i]
	sp, ok := p.(metadata.SearchProvider)
	if r.state[i].record != nil || !ok || r.refetchOnly {
		return nil
	}
	query := r.item.Title
	if r.bare != "" {
		query = r.bare
	}
	results, err := sp.Search(ctx, metadata.SearchQuery{
		Title:       query,
		Author:      r.item.Author,
		Year:        r.item.Year,
		ContentType: audiobookItemType,
		ProviderIDs: r.ids,
		Language:    r.item.Language,
	})
	if err != nil {
		slog.WarnContext(ctx, "audiobook enrichment: search error", "component", "audiobooks",
			"provider", p.Slug(), "content_id", r.item.ContentID, "error", redactProviderText(err.Error()))
		r.errs = append(r.errs, fmt.Errorf("%s search: %w", p.Slug(), err))
		return nil
	}
	mp, canFetch := p.(metadata.MetadataProvider)
	rules := &metadata.BookMatchRules{SeriesPosition: r.item.SeriesPosition, BareTitle: r.bare, VolumeTitle: r.volumeTitle}

	remaining := results
	for fetches := 0; fetches < maxCandidateFetches && len(remaining) > 0; {
		admission, err := metadata.AdmitSearchMatch(ctx, metadata.SearchMatchAdmissionRequest{
			WantTitle:           r.item.Title,
			WantYear:            r.item.Year,
			Results:             remaining,
			AgreedTitle:         r.agreedTitle,
			ExistingProviderIDs: r.ids,
			Owner:               r.e.providerIDs,
			ItemType:            audiobookItemType,
			ContentID:           r.item.ContentID,
			NoAuthor:            strings.TrimSpace(r.item.Author) == "",
			Rules:               rules,
		})
		if err != nil {
			r.errs = append(r.errs, fmt.Errorf("%s candidate admission: %w", p.Slug(), err))
			break
		}
		r.logOwnedConflicts(ctx, admission.Conflicts)
		if admission.Status == metadata.SearchMatchProviderIDConflict {
			remaining = dropResult(remaining, admission.Selected)
			continue
		}
		if admission.Status != metadata.SearchMatchAccepted {
			r.logUnaccepted(ctx, p, admission, len(results))
			break
		}

		candidateIDs := maps.Clone(r.ids)
		maps.Copy(candidateIDs, admission.ProviderIDs)
		if !canFetch {
			r.ids, r.agreedTitle = candidateIDs, admission.AgreedTitle
			return nil
		}
		fetches++
		result, err := mp.GetMetadata(ctx, r.metadataRequest(candidateIDs))
		if err != nil {
			r.logGetMetadataError(ctx, p, err)
			if !perItemAnswer(err) {
				// Paused, throttled or failing: the gate decides when this
				// provider is called again, not the next candidate.
				break
			}
			remaining = dropResult(remaining, admission.Selected)
			continue
		}
		if result == nil || !result.HasMetadata {
			// Nothing to check the candidate against: try the next one, and
			// keep none of its IDs.
			r.answered(i, candidateIDs, "")
			remaining = dropResult(remaining, admission.Selected)
			continue
		}
		// A candidate chosen for carrying the item's tagged ID is the tagged
		// record, whatever IDs the provider echoes back.
		byTaggedID := metadata.SharesProviderID(r.item.ProviderIDs, admission.Selected.ProviderIDs)
		admitted, rejection, err := r.admitFetched(ctx, candidateIDs, result, byTaggedID)
		r.answered(i, candidateIDs, rejection)
		if err != nil {
			r.errs = append(r.errs, fmt.Errorf("%s metadata identity admission: %w", p.Slug(), err))
			break
		}
		if rejection == rejectNarratorConflict {
			r.logRejected(ctx, p, rejection, "skip: contradicts an earlier provider's narrators")
			return nil
		}
		if rejection != "" {
			if metadata.SharesProviderID(r.item.ProviderIDs, admission.Selected.ProviderIDs) {
				r.logRejected(ctx, p, rejection, "leave unmatched: tagged id record")
				return errMatchRejected
			}
			r.logRejected(ctx, p, rejection, "try the next candidate")
			remaining = dropResult(remaining, admission.Selected)
			continue
		}
		r.noteNarrators(ctx, p, admitted)
		r.accept(ctx, i, admitted, candidateIDs, admission.AgreedTitle)
		return nil
	}
	return nil
}

// merge folds every provider's admitted record into r.merged, in chain order.
// A provider without one is asked again only if the IDs have grown since its
// last answer. If that answer contradicts the item's author or narrator, the
// item is left unmatched, unless the provider's previous answer had already
// failed that check: then only its record is dropped. An answer whose IDs
// contradict is skipped.
func (r *matchRun) merge(ctx context.Context) error {
	accumulator := &metadata.MetadataResult{ProviderIDs: r.ids}
	for i, p := range r.providers {
		result := r.state[i].record
		if result == nil {
			var err error
			if result, err = r.fetchForMerge(ctx, i, accumulator.ProviderIDs); err != nil {
				return err
			}
			if result == nil {
				continue
			}
		}
		// MergeMetadata copies fields, not the provider-level admission
		// marker. Preserve it so a sparse response (title/year/people only)
		// still counts as a successful match rather than no_match.
		accumulator.HasMetadata = true
		metadata.MergeMetadata(result, accumulator, nil, metadata.MergeFillEmpty)
		slog.DebugContext(ctx, "audiobook enrichment: metadata received", "component", "audiobooks",
			"provider", p.Slug(),
			"content_id", r.item.ContentID,
			"has_poster", result.PosterPath != "",
			"has_overview", result.Overview != "",
		)
	}
	r.ids = accumulator.ProviderIDs
	r.merged = accumulator
	return nil
}

// fetchForMerge fetches provider i's record for the merge. It returns nil
// with no error when the provider has nothing to add.
func (r *matchRun) fetchForMerge(ctx context.Context, i int, ids map[string]string) (*metadata.MetadataResult, error) {
	p := r.providers[i]
	mp, ok := p.(metadata.MetadataProvider)
	prev := r.state[i]
	if !ok || (prev.askedWith != nil && maps.Equal(prev.askedWith, ids)) {
		return nil, nil
	}
	got, err := mp.GetMetadata(ctx, r.metadataRequest(ids))
	if err != nil {
		r.logGetMetadataError(ctx, p, err)
		return nil, nil
	}
	if got == nil || !got.HasMetadata {
		return nil, nil
	}
	// A retry of a failed tagged-ASIN lookup, still with only the tag's IDs,
	// gets the same admission rules the lookup would have applied.
	byTaggedID := prev.lookupFailed && maps.Equal(ids, r.item.ProviderIDs)
	admitted, rejection, err := r.admitFetched(ctx, ids, got, byTaggedID)
	r.answered(i, maps.Clone(ids), rejection)
	switch {
	case err != nil:
		r.errs = append(r.errs, fmt.Errorf("%s metadata identity admission: %w", p.Slug(), err))
		return nil, nil
	case rejection == rejectProviderID:
		r.logRejected(ctx, p, rejection, "skip: metadata identity contradicts an existing id")
		return nil, nil
	case rejection == rejectNarratorConflict:
		r.logRejected(ctx, p, rejection, "skip: contradicts an earlier provider's narrators")
		return nil, nil
	case rejection != "" && prev.rejectedCredits:
		r.logRejected(ctx, p, rejection, "skip: provider's record was already rejected")
		return nil, nil
	case rejection != "":
		r.logRejected(ctx, p, rejection, "leave unmatched: metadata contradicts the item")
		return nil, errMatchRejected
	}
	r.noteNarrators(ctx, p, admitted)
	return admitted, nil
}

// bareSeriesTitle is title without a leading series name and number, as in
// "Series Name 3 - Book Title" or "Series Name, Book 3: Book Title", when
// series is the item's series name. Providers search such a title badly, and
// find the book by its own title. The number must equal position when that is
// known; an unknown position (0, which a decimal one also reads as) accepts
// any number. It returns the rest of the title and the number, or "" when
// title has no such prefix.
func bareSeriesTitle(title, series string, position int) (bare, volume string) {
	series = strings.TrimSpace(series)
	if series == "" {
		return "", ""
	}
	re := regexp.MustCompile(`(?i)^\s*` + regexp.QuoteMeta(series) +
		`[\s,:]+(?:(?:books?|bk|vol(?:ume)?|part)\.?\s*)?#?\s*(\d{1,4}(?:\.\d{1,2})?)\s*[-–—:]\s*(\S.*)$`)
	m := re.FindStringSubmatch(title)
	if m == nil {
		return "", ""
	}
	if position > 0 {
		if n, err := strconv.Atoi(m[1]); err != nil || n != position {
			return "", ""
		}
	}
	return strings.TrimSpace(m[2]), m[1]
}

// agreeingTitle is the record title or alias that best matches want, the
// baseline later providers must agree with: a translated primary title
// ("El marciano") must not make a later provider's "The Martian" read as a
// disagreement.
func agreeingTitle(want string, record *metadata.MetadataResult) string {
	best, bestScore := record.Title, -1.0
	for _, t := range recordTitles(record) {
		if strings.TrimSpace(t) == "" {
			continue
		}
		if score := metadata.TitleScore(want, t); score > bestScore {
			best, bestScore = t, score
		}
	}
	return best
}

// recordTitles lists every title a record goes by: primary, original and
// aliases.
func recordTitles(result *metadata.MetadataResult) []string {
	titles := []string{result.Title, result.OriginalTitle}
	for _, alias := range result.TitleAliases {
		titles = append(titles, alias.Title)
	}
	return titles
}

// perItemAnswer reports an error about the requested record only, after
// which the provider can still be asked for the next candidate: a gRPC
// NotFound or InvalidArgument. Everything else stops the provider for the
// item: a pause, throttling, an outage, and provider-wide refusals such as
// Unauthenticated, PermissionDenied or Unimplemented, which the shared
// classification also calls permanent but which would fail every candidate.
func perItemAnswer(err error) bool {
	var paused *providerPausedError
	if errors.As(err, &paused) {
		return false
	}
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.NotFound, codes.InvalidArgument:
		return true
	default:
		return false
	}
}

// accept makes record, fetched with ids, provider i's match.
func (r *matchRun) accept(ctx context.Context, i int, record *metadata.MetadataResult, ids map[string]string, agreedTitle string) {
	r.state[i].record = record
	r.ids = ids
	maps.Copy(r.ids, record.ProviderIDs)
	r.agreedTitle = agreedTitle
	slog.DebugContext(ctx, "audiobook enrichment: search result", "component", "audiobooks",
		"provider", r.providers[i].Slug(), "content_id", r.item.ContentID, "matched_ids", r.ids)
}

// noteNarrators records the narrators of an accepted record. The first
// record to list any settles them; when they contradict the item's narrator
// tags (admitFetched let a tagged-ID record through), they replace the tags as
// the names later records are checked against. The match stores the record's
// narrators in place of the tags, so a later provider describing the same
// edition must not be rejected for agreeing with it.
func (r *matchRun) noteNarrators(ctx context.Context, p metadata.Provider, record *metadata.MetadataResult) {
	var names []string
	for _, person := range record.People {
		if person.Kind == models.PersonKindNarrator {
			names = append(names, person.Name)
		}
	}
	if len(names) == 0 || r.narratorsSettled {
		return
	}
	r.narratorsSettled = true
	if metadata.CreditsAgree(r.narrators, record.People, models.PersonKindNarrator) {
		return
	}
	r.narrators = names
	slog.InfoContext(ctx, "audiobook enrichment: tagged id record overrules the narrator tag", "component", "audiobooks",
		"provider", p.Slug(),
		"content_id", r.item.ContentID,
		"title", r.item.Title,
	)
}

// titleAgrees reports whether one of the record's titles names the item's
// work on its own (metadata.TitleIdentifies), against the item's title or its
// title without the series prefix. Sharing words is not enough: two books of
// one series share the series name.
func (r *matchRun) titleAgrees(record *metadata.MetadataResult) bool {
	for _, t := range recordTitles(record) {
		if strings.TrimSpace(t) == "" {
			continue
		}
		if metadata.TitleIdentifies(r.item.Title, t) || (r.bare != "" && metadata.TitleIdentifies(r.bare, t)) {
			return true
		}
	}
	return false
}

func (r *matchRun) anyRecord() bool {
	return slices.ContainsFunc(r.state, func(s providerState) bool { return s.record != nil })
}

func (r *matchRun) metadataRequest(ids map[string]string) metadata.MetadataRequest {
	return metadata.MetadataRequest{ProviderIDs: ids, ContentType: audiobookItemType, Language: r.item.Language}
}

// admitFetched checks a fetched record against the item before any of it is
// merged: credited author and narrator (listed narrators must agree; none
// listed is no contradiction), stated volume, then provider IDs. byTaggedID
// marks a record reached through the item's own tagged ID. Such a record is
// rejected when the item names an author and none of its titles shares a word
// with the item's. Its narrators may contradict the narrator tag, but only if
// one of its titles names the item's work on its own (titleAgrees), so a wrong
// ID tag cannot pull in another book, such as another volume of the same
// series. Once an accepted record has listed narrators, such a record that
// contradicts them is rejectNarratorConflict: only it is dropped.
func (r *matchRun) admitFetched(
	ctx context.Context,
	existing map[string]string,
	result *metadata.MetadataResult,
	byTaggedID bool,
) (*metadata.MetadataResult, fetchRejection, error) {
	item := r.item
	if !metadata.CreditsAgree([]string{item.Author}, result.People, models.PersonKindAuthor) {
		return nil, rejectAuthor, nil
	}
	if !metadata.CreditsAgree(r.narrators, result.People, models.PersonKindNarrator) {
		tagged := byTaggedID && r.titleAgrees(result)
		switch {
		case !tagged:
			return nil, rejectNarrator, nil
		case r.narratorsSettled:
			return nil, rejectNarratorConflict, nil
		}
	}
	// A record whose titles state only volumes other than the one the item's
	// title states is another book, whichever path found the record.
	if metadata.RecordVolumeContradicts(r.volumeTitle, recordTitles(result)) {
		return nil, rejectVolume, nil
	}
	if byTaggedID && strings.TrimSpace(item.Author) != "" &&
		metadata.TitleContradicts(item.Title, recordTitles(result)...) {
		return nil, rejectTitle, nil
	}
	identity, err := metadata.AdmitProviderIDs(ctx, metadata.ProviderIDAdmissionRequest{
		CandidateProviderIDs: result.ProviderIDs,
		ExistingProviderIDs:  existing,
		Owner:                r.e.providerIDs,
		ItemType:             audiobookItemType,
		ContentID:            item.ContentID,
	})
	if err != nil {
		return nil, "", err
	}
	if identity.ContradictsExisting {
		return nil, rejectProviderID, nil
	}
	r.logOwnedConflicts(ctx, identity.Conflicts)
	admitted := *result
	admitted.ProviderIDs = identity.ProviderIDs
	return &admitted, "", nil
}

func dropResult(results []metadata.SearchResult, drop metadata.SearchResult) []metadata.SearchResult {
	return slices.DeleteFunc(slices.Clone(results), func(r metadata.SearchResult) bool {
		return metadata.SameSearchResult(r, drop)
	})
}

func (r *matchRun) logGetMetadataError(ctx context.Context, p metadata.Provider, err error) {
	slog.WarnContext(ctx, "audiobook enrichment: GetMetadata error", "component", "audiobooks",
		"provider", p.Slug(), "content_id", r.item.ContentID, "error", redactProviderText(err.Error()))
	r.errs = append(r.errs, fmt.Errorf("%s metadata: %w", p.Slug(), err))
}

func (r *matchRun) logOwnedConflicts(ctx context.Context, conflicts []metadata.ProviderIDConflict) {
	for _, conflict := range conflicts {
		slog.InfoContext(ctx, "audiobook enrichment: provider id already owned by another item; skipping", "component", "audiobooks",
			"provider", conflict.Provider,
			"provider_id", conflict.ProviderID,
			"content_id", r.item.ContentID,
			"owned_by", conflict.OwnedBy,
		)
	}
}

func (r *matchRun) logRejected(ctx context.Context, p metadata.Provider, rejection fetchRejection, action string) {
	slog.InfoContext(ctx, "audiobook enrichment: record contradicts the item", "component", "audiobooks",
		"provider", p.Slug(),
		"content_id", r.item.ContentID,
		"title", r.item.Title,
		"item_author", r.item.Author,
		"check", string(rejection),
		"action", action,
	)
}

func (r *matchRun) logUnaccepted(ctx context.Context, p metadata.Provider, admission metadata.SearchMatchAdmission, candidates int) {
	switch admission.Status {
	case metadata.SearchMatchNoCredibleMatch:
		// Info, not Debug: during a backlog drain the rejection rate is what
		// separates "threshold too strict" from "providers answering badly",
		// and it cannot be read from a log level nobody enables.
		slog.InfoContext(ctx, "audiobook enrichment: no credible match", "component", "audiobooks",
			"provider", p.Slug(),
			"content_id", r.item.ContentID,
			"title", r.item.Title,
			"candidates", candidates,
		)
	case metadata.SearchMatchProviderDisagreement:
		slog.WarnContext(ctx, "audiobook enrichment: provider disagreement; skipping", "component", "audiobooks",
			"provider", p.Slug(),
			"content_id", r.item.ContentID,
			"accepted_title", r.agreedTitle,
			"rejected_title", admission.MatchedTitle,
		)
	}
}
