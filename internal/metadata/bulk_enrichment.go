package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The bulk enrichment pass looks items up with enrichment-only providers (a
// plugin that declares lookup_provider_ids and never identifies items) many at
// a time, instead of one per match worker. A provider opts in by declaring
// bulk_lookup_limit, the number of concurrent lookups it combines into one
// upstream request; the pass keeps exactly that many in flight. MDBList, for
// one, answers up to 100 titles per request, so a library its daily quota
// would take weeks to cover one title at a time fits in a fraction of that.
//
// The pass merges what it finds with the fill-empty rules and locks of a
// scheduled refresh, but it is not a refresh: see persistEnrichment.
// metadata_enrichment_state records each answer, which makes the pass
// resumable after a restart, a shutdown or a spent quota.

const (
	// bulkEnrichmentEmptyRecheck is how long an item the provider had nothing
	// for waits before the pass asks again. Provider catalogs grow.
	bulkEnrichmentEmptyRecheck = 30 * 24 * time.Hour
	// bulkEnrichmentFailedRecheck is how long an item whose own lookup or
	// write failed waits before the pass tries it again.
	bulkEnrichmentFailedRecheck = 24 * time.Hour
	// bulkEnrichmentPersistWorkers bounds concurrent database writes. Lookups
	// run at the provider's limit; writes do not need to, and a hundred
	// concurrent merges would crowd the connection pool.
	bulkEnrichmentPersistWorkers = 4
	// bulkEnrichmentPersistTimeout bounds one item's merge and write, like a
	// scheduled refresh of one item.
	bulkEnrichmentPersistTimeout = 2 * time.Minute
	// bulkEnrichmentRecordTimeout bounds recording a page's outcomes, which
	// still runs when the pass is shutting down so finished work is kept.
	bulkEnrichmentRecordTimeout = 10 * time.Second

	// bulkEnrichmentStoppedShutdown is the stop reason when the server shuts
	// down or the task is canceled mid-pass.
	bulkEnrichmentStoppedShutdown = "shutting down"
)

// shuttingDown reports whether the pass has been told to stop. Stopping is
// not an error: the next run resumes from the items still missing an answer.
func shuttingDown(ctx context.Context) bool {
	return ctx.Err() != nil
}

// bulkEnrichmentTarget is one opted-in provider and the libraries that run it.
type bulkEnrichmentTarget struct {
	provider      MetadataProvider
	limit         int
	lookupKeys    []string
	movieFolders  []int
	seriesFolders []int
}

// BulkEnrichmentProviderReport summarizes one provider's share of a pass.
type BulkEnrichmentProviderReport struct {
	Provider string `json:"provider"`
	// Pending is how many items needed a lookup when the provider's turn
	// began.
	Pending int `json:"pending"`
	Found   int `json:"found"`
	Empty   int `json:"empty"`
	Failed  int `json:"failed"`
	// Stopped says why the provider's turn ended before its work ran out,
	// for example a spent quota. Empty when it finished.
	Stopped string `json:"stopped,omitempty"`
}

// BulkEnrichmentReport summarizes one pass.
type BulkEnrichmentReport struct {
	Providers []BulkEnrichmentProviderReport `json:"providers"`
}

// BulkEnrichmentProgress receives the pass's position: items looked up so far
// out of the total pending when the pass began.
type BulkEnrichmentProgress func(done, total int, provider string)

// isEnrichmentProvider reports whether p is an enrichment-only plugin
// provider: one that looks items up by other providers' IDs.
func isEnrichmentProvider(p Provider) bool {
	pp, ok := p.(*PluginProvider)
	return ok && len(pp.lookupProviderIDs) > 0
}

// isRoutineEnrichmentError reports an enrichment-only provider saying it
// cannot answer right now: a spent quota, an outage, a missing or rejected
// key. On a small daily quota that happens every day, and the plugin logs it
// itself when it begins, so a match or refresh does not warn once per item.
func isRoutineEnrichmentError(p Provider, err error) bool {
	if !isEnrichmentProvider(p) {
		return false
	}
	switch status.Code(err) {
	case codes.ResourceExhausted, codes.Unavailable, codes.FailedPrecondition, codes.Unauthenticated, codes.PermissionDenied:
		return true
	default:
		return false
	}
}

// bulkEnrichmentStopsPass reports whether a lookup error concerns the provider
// as a whole (a spent quota, an outage, a missing key, a plugin that is not
// running, the pass shutting down) rather than one item. Such an error ends the
// provider's turn and records nothing, so the items are looked up again on the
// next pass instead of being filed as having nothing to find.
func bulkEnrichmentStopsPass(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	st, ok := status.FromError(err)
	if !ok {
		// Not a gRPC status: the host could not reach the plugin.
		return true
	}
	switch st.Code() {
	case codes.InvalidArgument, codes.NotFound, codes.Internal:
		return false
	default:
		return true
	}
}

// recordEnrichedBy marks enrichment-only providers that answered during a
// match or refresh, so the bulk pass skips the item for them. Failures only
// cost a repeated lookup later, so they are logged.
func (s *MetadataService) recordEnrichedBy(ctx context.Context, contentID string, providers []string) {
	if s.enrichmentState == nil || len(providers) == 0 {
		return
	}
	providers = slices.Compact(slices.Sorted(slices.Values(providers)))
	if err := s.enrichmentState.RecordFound(ctx, contentID, providers); err != nil {
		slog.WarnContext(ctx, "metadata: failed to record enrichment providers", "component", "metadata",
			"content_id", contentID, "providers", providers, "error", err)
	}
}

// HasBulkEnrichmentWork reports whether any opted-in provider has an item to
// look up.
func (s *MetadataService) HasBulkEnrichmentWork(ctx context.Context) (bool, error) {
	if s.enrichmentState == nil {
		return false, nil
	}
	targets, err := s.bulkEnrichmentTargets(ctx)
	if err != nil {
		return false, err
	}
	for _, target := range targets {
		page, err := s.enrichmentState.Candidates(ctx, target.query("", 1))
		if err != nil {
			return false, err
		}
		if len(page) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// RunBulkEnrichment runs every opted-in provider over the items it has not
// answered for, one provider after another. It returns when the work runs out,
// every provider has stopped, or ctx ends; an ended context is not an error.
func (s *MetadataService) RunBulkEnrichment(ctx context.Context, progress BulkEnrichmentProgress) (BulkEnrichmentReport, error) {
	if s.enrichmentState == nil {
		return BulkEnrichmentReport{}, nil
	}
	targets, err := s.bulkEnrichmentTargets(ctx)
	if err != nil {
		return BulkEnrichmentReport{}, err
	}
	return s.runBulkEnrichment(ctx, targets, progress)
}

func (s *MetadataService) runBulkEnrichment(ctx context.Context, targets []bulkEnrichmentTarget, progress BulkEnrichmentProgress) (BulkEnrichmentReport, error) {
	var report BulkEnrichmentReport
	pending := make([]int, len(targets))
	total := 0
	for i, target := range targets {
		count, err := s.enrichmentState.CountCandidates(ctx, target.query("", 0))
		if err != nil {
			return report, err
		}
		pending[i] = count
		total += count
	}

	done := 0
	for i, target := range targets {
		if shuttingDown(ctx) {
			break
		}
		providerReport := BulkEnrichmentProviderReport{Provider: target.provider.Slug(), Pending: pending[i]}
		err := s.runBulkEnrichmentTarget(ctx, target, &providerReport, func(looked int) {
			done += looked
			if progress != nil {
				progress(done, total, target.provider.Slug())
			}
		})
		report.Providers = append(report.Providers, providerReport)
		if err != nil {
			return report, err
		}
		slog.InfoContext(ctx, "metadata: bulk enrichment pass finished a provider", "component", "metadata",
			"provider", providerReport.Provider, "pending", providerReport.Pending,
			"found", providerReport.Found, "empty", providerReport.Empty, "failed", providerReport.Failed,
			"stopped", providerReport.Stopped)
	}
	return report, nil
}

// runBulkEnrichmentTarget pages through one provider's candidates until they
// run out or the provider stops answering. It returns an error only when the
// candidate query fails.
func (s *MetadataService) runBulkEnrichmentTarget(ctx context.Context, target bulkEnrichmentTarget, report *BulkEnrichmentProviderReport, advanced func(looked int)) error {
	after := ""
	for {
		page, err := s.enrichmentState.Candidates(ctx, target.query(after, target.limit))
		if shuttingDown(ctx) {
			report.Stopped = bulkEnrichmentStoppedShutdown
			return nil
		}
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		if stop := s.enrichBulkPage(ctx, target, page, report); stop != "" {
			report.Stopped = stop
			return nil
		}
		advanced(len(page))
		if len(page) < target.limit {
			return nil
		}
		after = page[len(page)-1].ContentID
	}
}

// bulkLookup is one candidate's lookup result.
type bulkLookup struct {
	result *MetadataResult
	err    error
}

// enrichBulkPage looks every candidate in the page up at once, writes what
// was found, and records the outcomes. It returns a non-empty reason when the
// provider stopped answering; the page's finished lookups are still kept.
func (s *MetadataService) enrichBulkPage(ctx context.Context, target bulkEnrichmentTarget, page []enrichmentCandidate, report *BulkEnrichmentProviderReport) string {
	// Every lookup in the page is in flight at once (the page is the
	// provider's limit), so the provider can answer them together.
	lookups := make([]bulkLookup, len(page))
	lookupCtx, cancelLookups := context.WithCancel(ctx)
	defer cancelLookups()
	var stopErr atomic.Pointer[error]
	var wg sync.WaitGroup
	for i, candidate := range page {
		wg.Go(func() {
			result, err := target.provider.GetMetadata(lookupCtx, MetadataRequest{
				ProviderIDs: candidate.ProviderIDs,
				ContentType: candidate.Type,
				Language:    candidate.Language,
			})
			lookups[i] = bulkLookup{result: result, err: err}
			if err != nil && bulkEnrichmentStopsPass(err) && stopErr.CompareAndSwap(nil, &err) {
				cancelLookups()
			}
		})
	}
	wg.Wait()

	outcomes := make([]enrichmentOutcome, len(page))
	now := time.Now()
	var persist sync.WaitGroup
	work := make(chan int)
	for range bulkEnrichmentPersistWorkers {
		persist.Go(func() {
			for i := range work {
				if err := s.persistEnrichment(ctx, page[i], target.provider.Slug(), lookups[i].result); err != nil {
					if shuttingDown(ctx) {
						continue
					}
					slog.WarnContext(ctx, "metadata: bulk enrichment write failed", "component", "metadata",
						"provider", target.provider.Slug(), "content_id", page[i].ContentID, "error", err)
					outcomes[i] = failedEnrichmentOutcome(page[i].ContentID, now)
					continue
				}
				outcomes[i] = enrichmentOutcome{ContentID: page[i].ContentID, Outcome: enrichmentOutcomeFound}
			}
		})
	}
	for i, lookup := range lookups {
		switch {
		case lookup.err != nil && bulkEnrichmentStopsPass(lookup.err):
			// Not this item's fault; look it up again next pass.
		case lookup.err != nil:
			slog.DebugContext(ctx, "metadata: bulk enrichment lookup failed", "component", "metadata",
				"provider", target.provider.Slug(), "content_id", page[i].ContentID, "error", lookup.err)
			outcomes[i] = failedEnrichmentOutcome(page[i].ContentID, now)
		case lookup.result == nil || !lookup.result.HasMetadata:
			next := now.Add(bulkEnrichmentEmptyRecheck)
			outcomes[i] = enrichmentOutcome{ContentID: page[i].ContentID, Outcome: enrichmentOutcomeEmpty, NextCheckAt: &next}
		default:
			work <- i
		}
	}
	close(work)
	persist.Wait()

	recorded := make([]enrichmentOutcome, 0, len(outcomes))
	for _, outcome := range outcomes {
		if outcome.ContentID == "" {
			continue
		}
		recorded = append(recorded, outcome)
		switch outcome.Outcome {
		case enrichmentOutcomeFound:
			report.Found++
		case enrichmentOutcomeEmpty:
			report.Empty++
		case enrichmentOutcomeFailed:
			report.Failed++
		}
	}
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), bulkEnrichmentRecordTimeout)
	defer cancelRecord()
	if err := s.enrichmentState.RecordOutcomes(recordCtx, target.provider.Slug(), recorded); err != nil {
		// The items are simply looked up again next pass.
		slog.WarnContext(ctx, "metadata: failed to record bulk enrichment outcomes", "component", "metadata",
			"provider", target.provider.Slug(), "count", len(recorded), "error", err)
	}

	if shuttingDown(ctx) {
		return bulkEnrichmentStoppedShutdown
	}
	if stopped := stopErr.Load(); stopped != nil {
		if status.Code(*stopped) == codes.ResourceExhausted {
			return "provider quota spent"
		}
		return fmt.Sprintf("provider unavailable: %v", *stopped)
	}
	return ""
}

func failedEnrichmentOutcome(contentID string, now time.Time) enrichmentOutcome {
	next := now.Add(bulkEnrichmentFailedRecheck)
	return enrichmentOutcome{ContentID: contentID, Outcome: enrichmentOutcomeFailed, NextCheckAt: &next}
}

// persistEnrichment merges one enrichment provider's result into a matched
// item. It goes through mergeAndPersist with the fill-empty rules and field
// locks of a scheduled refresh, but as an enrichment write, which differs from
// a refresh in what it leaves alone:
//
//   - refresh bookkeeping (last refresh, failure count, match time, status)
//     and refresh debt, so a pending refresh still happens;
//   - identity: the stored provider IDs, and the identity repairs that belong
//     to matching and refreshes;
//   - people and remote videos, which a refresh replaces wholesale from every
//     provider's answer and one provider's answer would wipe;
//   - series seasons and episodes.
func (s *MetadataService) persistEnrichment(ctx context.Context, candidate enrichmentCandidate, providerSlug string, result *MetadataResult) error {
	ctx, cancel := context.WithTimeout(ctx, bulkEnrichmentPersistTimeout)
	defer cancel()

	language := candidate.Language
	if language == "" {
		language = "en"
	}
	// An enrichment provider never identifies an item, so none of its provider
	// IDs are kept. One the item lacks could belong to another item, and the
	// write's provider-ID conflict recovery would then merge the two.
	result.ProviderIDs = nil
	accumulator := &MetadataResult{HasMetadata: true, ProviderIDs: copyMap(candidate.ProviderIDs)}
	foldProviderResult(accumulator, result, language, providerSlug, false, nil)
	_, err := s.mergeAndPersist(ctx, ProcessRequest{
		ContentID:      candidate.ContentID,
		Language:       language,
		Mode:           ModeScheduledRefresh,
		enrichmentOnly: true,
	}, accumulator, nil, nil, nil, candidate.Type)
	return err
}

func (target bulkEnrichmentTarget) query(after string, limit int) enrichmentCandidateQuery {
	return enrichmentCandidateQuery{
		Provider:      target.provider.Slug(),
		LookupKeys:    target.lookupKeys,
		MovieFolders:  target.movieFolders,
		SeriesFolders: target.seriesFolders,
		After:         after,
		Limit:         limit,
	}
}

// bulkEnrichmentTargets finds the providers that opted into the pass and the
// libraries whose movie and series chains run them. The chains resolve exactly
// as a refresh resolves them, including the default chain of a library with no
// explicit one.
func (s *MetadataService) bulkEnrichmentTargets(ctx context.Context) ([]bulkEnrichmentTarget, error) {
	if s.hooks.bulkEnrichmentTargets != nil {
		return s.hooks.bulkEnrichmentTargets(ctx)
	}
	if s.dbPool == nil {
		return nil, nil
	}
	capabilities, err := ListEnabledMetadataCapabilities(ctx, s.dbPool)
	if err != nil {
		return nil, err
	}
	// Chain providers are matched to capabilities by installation and
	// capability ID, since two installations may share a capability ID.
	type capabilityKey struct {
		installationID int
		capabilityID   string
	}
	var targets []bulkEnrichmentTarget
	index := make(map[capabilityKey]int)
	for _, capability := range capabilities {
		if capability.BulkLookupLimit <= 0 || len(capability.LookupProviderIDs) == 0 {
			continue
		}
		index[capabilityKey{capability.PluginInstallationID, capability.CapabilityID}] = len(targets)
		targets = append(targets, bulkEnrichmentTarget{limit: capability.BulkLookupLimit, lookupKeys: capability.LookupProviderIDs})
	}
	if len(targets) == 0 {
		return nil, nil
	}
	folderIDs, err := s.enabledFolderIDs(ctx)
	if err != nil {
		return nil, err
	}

	for _, folderID := range folderIDs {
		for _, level := range []string{matchContentTypeMovie, matchContentTypeSeries} {
			chain, err := s.resolveChainCached(ctx, folderID, level)
			if err != nil {
				return nil, err
			}
			for _, p := range chain {
				pp, ok := p.(*PluginProvider)
				if !ok {
					continue
				}
				i, ok := index[capabilityKey{pp.installationID, pp.capabilityID}]
				if !ok {
					continue
				}
				target := &targets[i]
				if target.provider == nil {
					target.provider = pp
				}
				if level == matchContentTypeMovie {
					target.movieFolders = append(target.movieFolders, folderID)
				} else {
					target.seriesFolders = append(target.seriesFolders, folderID)
				}
			}
		}
	}
	return slices.DeleteFunc(targets, func(target bulkEnrichmentTarget) bool {
		return target.provider == nil
	}), nil
}

// enabledFolderIDs lists the enabled libraries.
func (s *MetadataService) enabledFolderIDs(ctx context.Context) ([]int, error) {
	rows, err := s.dbPool.Query(ctx, `SELECT id FROM media_folders WHERE enabled = true ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list enabled libraries: %w", err)
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan library id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
