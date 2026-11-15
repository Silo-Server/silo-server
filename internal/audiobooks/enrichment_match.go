package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

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
	rejectAuthor     fetchRejection = "author"
	rejectNarrator   fetchRejection = "narrator"
	rejectTitle      fetchRejection = "title"
	rejectVolume     fetchRejection = "volume"
	rejectProviderID fetchRejection = "provider_id"
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
	// with the same tag-only IDs is that lookup again and is admitted as one.
	lookupFailed bool
}

// answered records a GetMetadata answer from provider i.
func (r *matchRun) answered(i int, ids map[string]string, rejection fetchRejection) {
	r.state[i].askedWith = ids
	r.state[i].rejectedCredits = rejection == rejectAuthor || rejection == rejectNarrator
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
// disagree, and no title match can settle which is right.
func (e *Enricher) findMatch(ctx context.Context, item enrichmentItemRow, providers []metadata.Provider) audiobookMatch {
	run := &matchRun{
		e:         e,
		item:      item,
		providers: providers,
		ids:       maps.Clone(item.ProviderIDs),
		state:     make([]providerState, len(providers)),
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
		maps.Copy(r.ids, admitted.ProviderIDs)
		if r.agreedTitle == "" {
			r.agreedTitle = admitted.Title
		}
	}
	return nil
}

// searchProvider searches provider i and fetches its best candidates in turn
// until one agrees with the item.
func (r *matchRun) searchProvider(ctx context.Context, i int) error {
	p := r.providers[i]
	sp, ok := p.(metadata.SearchProvider)
	if r.state[i].record != nil || !ok {
		return nil
	}
	results, err := sp.Search(ctx, metadata.SearchQuery{
		Title:       r.item.Title,
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
	rules := &metadata.BookMatchRules{SeriesPosition: r.item.SeriesPosition}

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
			return nil
		}
		r.logOwnedConflicts(ctx, admission.Conflicts)
		if admission.Status == metadata.SearchMatchProviderIDConflict {
			remaining = dropResult(remaining, admission.Selected)
			continue
		}
		if admission.Status != metadata.SearchMatchAccepted {
			r.logUnaccepted(ctx, p, admission, len(results))
			return nil
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
			return nil
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
		r.state[i].record = admitted
		r.ids = candidateIDs
		maps.Copy(r.ids, admitted.ProviderIDs)
		r.agreedTitle = admission.AgreedTitle
		slog.DebugContext(ctx, "audiobook enrichment: search result", "component", "audiobooks",
			"provider", p.Slug(), "content_id", r.item.ContentID, "matched_ids", r.ids)
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
	case rejection != "" && prev.rejectedCredits:
		r.logRejected(ctx, p, rejection, "skip: provider's record was already rejected")
		return nil, nil
	case rejection != "":
		r.logRejected(ctx, p, rejection, "leave unmatched: metadata contradicts the item")
		return nil, errMatchRejected
	}
	r.state[i].record = admitted
	return admitted, nil
}

// missingNarrator applies the narrator rule to the record that would
// establish the match: for a narrator-tagged item it must list a narrator, or
// the tagged edition is unconfirmed. A record reached through the item's own
// tagged ID is exempt, since the tag names that edition. A record merged after
// the match is established is held only to CreditsAgree, which rejects listed
// narrators that disagree but never an empty list.
func (r *matchRun) missingNarrator(result *metadata.MetadataResult, byTaggedID bool) bool {
	if byTaggedID || r.anyRecord() {
		return false
	}
	if !slices.ContainsFunc(r.item.Narrators, func(n string) bool { return strings.TrimSpace(n) != "" }) {
		return false
	}
	return !slices.ContainsFunc(result.People, func(p models.ItemPerson) bool {
		return p.Kind == models.PersonKindNarrator && strings.TrimSpace(p.Name) != ""
	})
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

func (r *matchRun) anyRecord() bool {
	return slices.ContainsFunc(r.state, func(s providerState) bool { return s.record != nil })
}

func (r *matchRun) metadataRequest(ids map[string]string) metadata.MetadataRequest {
	return metadata.MetadataRequest{ProviderIDs: ids, ContentType: audiobookItemType, Language: r.item.Language}
}

// admitFetched checks a fetched record against the item before any of it is
// merged: credited author and narrator, then provider IDs. byTaggedID marks a
// record reached through the item's own tagged ID; it is exempt from needing a
// narrator, and is rejected when the item names an author and the two titles
// share no word.
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
	if !metadata.CreditsAgree(item.Narrators, result.People, models.PersonKindNarrator) {
		return nil, rejectNarrator, nil
	}
	if r.missingNarrator(result, byTaggedID) {
		return nil, rejectNarrator, nil
	}
	// A volume the item's title states that the record's title states
	// differently is another book, whichever path found the record.
	if metadata.VolumesContradict(item.Title, result.Title) {
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
