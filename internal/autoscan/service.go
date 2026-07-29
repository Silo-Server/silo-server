package autoscan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/scantrigger"
	"github.com/google/uuid"
)

const (
	scanTrigger = "autoscan"

	// Autoscan sources can occasionally report a very large marker window after
	// downtime or provider recovery. Keep those windows bounded in the scan
	// queue by falling back to one full-library scan per affected library.
	maxAutoscanTargetsPerPoll = 1000
)

// Store is the persistence surface the engine needs. The repository implements
// the full CRUD surface; this interface is the read/bookkeeping subset the poll
// loop touches.
type Store interface {
	GetSettings(ctx context.Context) (Settings, error)
	ListEnabledSources(ctx context.Context) ([]Source, error)
	GetSource(ctx context.Context, id string) (Source, error)
	GetConnection(ctx context.Context, id string) (Connection, error)
	AdvanceMarker(ctx context.Context, sourceID, marker string) error
	RecordError(ctx context.Context, sourceID, msg string) error
	CreateEvent(ctx context.Context, event EventCreate) (int64, error)
	FinishEvent(ctx context.Context, event EventFinish) error
	CreateWebhookDelivery(ctx context.Context, in ChangeIngest) (WebhookDelivery, error)
	ClaimWebhookDeliveries(ctx context.Context, workerID string, limit int) ([]WebhookDelivery, error)
	CompleteWebhookDelivery(ctx context.Context, id int64, lockedBy string) error
	RetryWebhookDelivery(ctx context.Context, id int64, lockedBy string, delay time.Duration, msg string) error
	RecordWebhookError(ctx context.Context, sourceID, msg string) error
	ClearWebhookError(ctx context.Context, sourceID string) error
}

// Resolver maps a Silo-native path to a concrete scan target (library folder).
type Resolver interface {
	Resolve(ctx context.Context, req scantrigger.Request) (*scantrigger.Target, error)
	ResolveMissingSubtree(ctx context.Context, subtreePath, trigger string) (*scantrigger.Target, error)
	ResolveVanishedPath(ctx context.Context, path, trigger string) (*scantrigger.Target, error)
}

// Queuer enqueues resolved scan targets.
type Queuer interface {
	EnqueueScans(ctx context.Context, targets []scantrigger.Target) error
	EnqueueAutoscanScans(ctx context.Context, targets []scantrigger.Target, eventID int64) (created, reused int, err error)
}

type resolveStats struct {
	ChangesResolved int
	TargetsClaimed  int
	Suppressed      int
	// TransientErrors counts resolve attempts that failed with an internal
	// (non-RequestError) error — resolver/database faults that may clear on a
	// later poll, as opposed to paths that are simply outside Silo's libraries.
	TransientErrors int
}

// connectionResolver resolves a stored connection to concrete credentials.
type connectionResolver interface {
	Resolve(ctx context.Context, c Connection) (ResolvedConnection, error)
}

// RootFolderClient lists a Radarr/Sonarr instance's configured root folder
// paths (used by the rewrite suggester). The concrete impl additionally
// satisfies ArrStatusProbe for the connection-test endpoint.
type RootFolderClient interface {
	RootFolders(ctx context.Context, baseURL, apiKey string) ([]string, error)
}

// FolderLister lists every Silo media-folder path (used by the rewrite
// suggester to match arr roots against Silo folders).
type FolderLister interface {
	ListFolderPaths(ctx context.Context) ([]string, error)
}

type Service struct {
	store    Store
	provider ScanSourceProvider
	connres  connectionResolver
	resolver Resolver
	queue    Queuer
	suppress Suppressor
	lister   ScanSourceLister

	// Optional deps for the connection-test and rewrite-suggester endpoints.
	// Wired via setters so the poll-loop constructor stays unchanged and tests
	// that only exercise PollOnce need not supply them.
	rootFolders RootFolderClient
	folders     FolderLister
}

// SetSuggesterDeps wires the dependencies the rewrite-suggester and
// connection-test endpoints need: an arr root-folder/status client and a Silo
// media-folder lister. Optional — when unset, SuggestRewrites/TestConnection
// return an error.
func (s *Service) SetSuggesterDeps(rootFolders RootFolderClient, folders FolderLister) {
	s.rootFolders = rootFolders
	s.folders = folders
}

// NewService builds the autoscan engine. The provider supplies changed paths
// from scan_source plugins; connres resolves a source's connection to concrete
// credentials; resolver/queue/suppress drive the resolve→suppress→enqueue loop.
// lister enumerates installed scan_source capabilities so the Add-source picker
// can offer them; it may be nil (the picker then returns an empty list).
func NewService(
	store Store,
	provider ScanSourceProvider,
	connres connectionResolver,
	resolver Resolver,
	queue Queuer,
	suppress Suppressor,
	lister ScanSourceLister,
) *Service {
	return &Service{
		store:    store,
		provider: provider,
		connres:  connres,
		resolver: resolver,
		queue:    queue,
		suppress: suppress,
		lister:   lister,
	}
}

// PollOnce runs one autoscan cycle. Per-source failures are logged, recorded on
// the source/event, and the loop continues; only settings/listing errors
// propagate. The opaque next marker returned by the provider is stored
// verbatim once the window's work is consumed — including windows whose paths
// all resolve OUTSIDE Silo's libraries (routine for whole-volume filesystem
// watchers; the event finishes as "unresolved" so it stays visible). The
// marker is held only on genuine failures: provider errors, enqueue errors,
// and windows where any resolve attempt failed internally (possibly
// transient), so the affected imports are retried next poll.
func (s *Service) PollOnce(ctx context.Context) error {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.Enabled {
		return nil
	}
	sources, err := s.store.ListEnabledSources(ctx)
	if err != nil {
		return err
	}
	ttl := time.Duration(settings.DebounceSeconds) * time.Second
	now := time.Now()

	for _, src := range sources {
		// Webhook sources are fed by IngestChanges when the provider POSTs to
		// their endpoint; there is nothing to poll and no plugin to invoke.
		if src.DeliveryMode == DeliveryModeWebhook {
			continue
		}
		// Honor the per-source poll interval as a "poll at most every N seconds"
		// floor: the global task fires at the default cadence, so a source with a
		// longer interval is skipped until enough time has elapsed.
		interval := time.Duration(settings.DefaultPollIntervalSeconds) * time.Second
		if src.PollIntervalSeconds != nil {
			interval = time.Duration(*src.PollIntervalSeconds) * time.Second
		}
		if src.LastRunAt != nil && now.Sub(*src.LastRunAt) < interval {
			continue
		}
		marker := ""
		if src.Marker != nil {
			marker = *src.Marker
		}
		eventID, started := s.createEvent(ctx, src, marker, time.Now())
		if !started {
			continue
		}
		// A connection is OPTIONAL. Server-based providers (Sonarr/Radarr) bind a
		// connection and the resolved {base_url, api_key} is handed to the plugin.
		// Other providers (e.g. a filesystem/CephFS watcher) need none and get an
		// empty connection they ignore. If a plugin requires a connection it didn't
		// get, it returns an error that is RecordError'd below — so the operator
		// still sees "needs attention" without the host assuming every source is
		// credential-based.
		var conn ResolvedConnection
		if src.ConnectionID != nil {
			resolved, cerr := s.resolveConnection(ctx, *src.ConnectionID)
			if cerr != nil {
				slog.WarnContext(ctx, "autoscan: resolve connection failed", "component", "autoscan", "source_id", src.ID, "err", cerr)
				if rerr := s.store.RecordError(ctx, src.ID, cerr.Error()); rerr != nil {
					slog.WarnContext(ctx, "autoscan: record error failed", "component", "autoscan", "source_id", src.ID, "err", rerr)
				}
				s.finishEvent(ctx, eventID, EventFinish{
					Status:       EventStatusError,
					ErrorMessage: cerr.Error(),
					MarkerAfter:  marker,
				})
				continue
			}
			conn = resolved
		}
		changes, next, perr := s.provider.PollChanges(ctx, src.PluginID, src.CapabilityID, marker, conn, src.SourceConfig)
		if perr != nil {
			slog.WarnContext(ctx, "autoscan: poll changes failed", "component", "autoscan", "source_id", src.ID, "err", perr)
			if rerr := s.store.RecordError(ctx, src.ID, perr.Error()); rerr != nil {
				slog.WarnContext(ctx, "autoscan: record error failed", "component", "autoscan", "source_id", src.ID, "err", rerr)
			}
			s.finishEvent(ctx, eventID, EventFinish{
				Status:       EventStatusError,
				ErrorMessage: perr.Error(),
				MarkerAfter:  marker,
			})
			continue // do NOT advance marker
		}

		// consumeSourceChanges finishes the event and does all per-source error
		// logging/recording; per-source failures never abort the poll loop.
		_, _ = s.consumeSourceChanges(ctx, src, changes, consumeOptions{
			EventID:       eventID,
			TTL:           ttl,
			Marker:        marker,
			NextMarker:    next,
			AdvanceMarker: true,
		})
	}
	return nil
}

// consumeOptions parameterizes the shared consume path for its two callers.
// Marker/NextMarker/AdvanceMarker apply to poll-mode consumption only: webhook
// deliveries carry no marker window, never advance one, and hold nothing on
// failure (arr retries the delivery instead).
type consumeOptions struct {
	EventID       int64
	TTL           time.Duration
	Marker        string // poll: the window's opening marker, held on failure
	NextMarker    string // poll: the provider's next marker
	AdvanceMarker bool   // poll: true; webhook: false
}

// consumeResult reports what one consume pass did, for callers that surface
// the outcome (webhook delivery responses).
type consumeResult struct {
	Status      EventStatus
	Enqueue     EnqueueResult
	Stats       resolveStats
	ResolvedAny bool
}

// consumeSourceChanges is the shared back half of both delivery modes: it
// rewrites raw provider paths, resolves and claims scan targets, enqueues
// them, decides the event status, and finishes the event. The returned error
// reports a genuine failure (enqueue fault or transient resolve fault); the
// poll loop ignores it (already logged/recorded per source), webhook ingestion
// propagates it so the delivery can be retried by the sender.
func (s *Service) consumeSourceChanges(ctx context.Context, src Source, changes []Change, opts consumeOptions) (consumeResult, error) {
	rewritten := rewriteChanges(changes, src.PathRewrites)
	targets, claimed, resolvedAny, stats := s.resolveAndClaim(ctx, rewritten, opts.TTL)
	result := consumeResult{Stats: stats, ResolvedAny: resolvedAny}
	if len(targets) > maxAutoscanTargetsPerPoll {
		collapsed := collapseTargetsToLibraryScans(targets)
		slog.WarnContext(ctx, "autoscan: collapsed large scan target batch to library scans", "component", "autoscan",
			"source_id", src.ID,
			"targets", len(targets),
			"collapsed_targets", len(collapsed),
			"limit", maxAutoscanTargetsPerPoll,
		)
		targets = collapsed
	}
	if len(targets) > 0 {
		enqueue, eerr := s.enqueueScanTargets(ctx, targets, opts.EventID)
		result.Enqueue = enqueue
		if eerr != nil {
			s.releaseClaims(ctx, claimed)
			slog.WarnContext(ctx, "autoscan: enqueue failed", "component", "autoscan", "source_id", src.ID, "err", eerr)
			result.Status = EventStatusError
			s.finishEvent(ctx, opts.EventID, EventFinish{
				Status:          EventStatusError,
				ChangesReturned: len(changes),
				ChangesResolved: stats.ChangesResolved,
				TargetsClaimed:  stats.TargetsClaimed,
				ScansSuppressed: stats.Suppressed,
				ErrorMessage:    eerr.Error(),
				MarkerAfter:     opts.Marker,
			})
			return result, eerr // do NOT advance marker
		}
	}

	// Advancing the marker is what tells the provider "I've consumed up to
	// here". Advance it ONLY when the work it represents is genuinely done:
	//   - provider returned ZERO paths        → nothing to do, advance normally.
	//   - returned paths AND ≥1 resolved       → enqueued (above), advance.
	//   - returned paths, resolved but all
	//     suppressed (recently scanned /
	//     debounced)                           → work is effectively done; advance.
	//   - ANY resolve attempt failed INTERNALLY (resolver/database fault —
	//     possibly transient), whether or not other paths resolved → hold the
	//     marker and record the error; a later poll re-reads the same window
	//     once the fault clears. Advancing would silently skip the failed
	//     paths. Targets that DID resolve were already enqueued above; the
	//     re-read at worst re-scans them, which is safe.
	//   - returned paths AND NOTHING resolved  → every path is outside Silo's
	//     libraries (RequestError) — a benign, expected condition for
	//     whole-volume filesystem watchers (e.g. CephFS), which observe
	//     folders that are not registered as Silo libraries. Holding here
	//     would pin the marker and permanently stall autoscan, so advance;
	//     finish the event as "unresolved" so the condition stays visible in
	//     poll history.
	// (Some-but-not-all resolving still advances when the unresolved
	// remainder is merely outside Silo's libraries.)
	//
	// Webhook deliveries have no marker: a transient resolve fault still
	// finishes the event as error (the sender retries the delivery; a
	// duplicate is at worst suppressed), and the unresolved case is the same
	// benign "paths outside Silo's libraries" signal.
	//
	// NOTE: len(targets)==0 alone is NOT misconfiguration — paths can resolve
	// yet be fully suppressed. Gate on resolvedAny, not targets.
	if stats.TransientErrors > 0 {
		msg := fmt.Sprintf("%d resolve attempt(s) failed internally (%d of %d path(s) resolved)", stats.TransientErrors, stats.ChangesResolved, len(changes))
		if opts.AdvanceMarker {
			msg += " — holding marker to retry"
		}
		if rerr := s.store.RecordError(ctx, src.ID, msg); rerr != nil {
			slog.WarnContext(ctx, "autoscan: record error failed", "component", "autoscan", "source_id", src.ID, "err", rerr)
		}
		result.Status = EventStatusError
		s.finishEvent(ctx, opts.EventID, EventFinish{
			Status:          EventStatusError,
			ChangesReturned: len(changes),
			ChangesResolved: stats.ChangesResolved,
			TargetsClaimed:  stats.TargetsClaimed,
			ScansCreated:    result.Enqueue.Created,
			ScansReused:     result.Enqueue.Reused,
			ScansSuppressed: stats.Suppressed,
			ErrorMessage:    msg,
			MarkerAfter:     opts.Marker,
		})
		return result, errors.New(msg) // do NOT advance marker
	}
	status := EventStatusSuccess
	var statusMsg string
	if len(changes) > 0 && !resolvedAny {
		status = EventStatusUnresolved
		statusMsg = fmt.Sprintf("returned %d path(s) but none matched a Silo library folder", len(changes))
		if opts.AdvanceMarker {
			statusMsg += " — advanced past them"
			slog.WarnContext(ctx, "autoscan: returned paths matched no library folder — advancing marker",
				"source_id", src.ID, "changes", len(changes))
		} else {
			slog.WarnContext(ctx, "autoscan: webhook paths matched no library folder",
				"source_id", src.ID, "changes", len(changes))
		}
	}
	if opts.AdvanceMarker {
		if aerr := s.store.AdvanceMarker(ctx, src.ID, opts.NextMarker); aerr != nil {
			slog.WarnContext(ctx, "autoscan: advance marker failed", "component", "autoscan", "source_id", src.ID, "err", aerr)
			result.Status = EventStatusError
			s.finishEvent(ctx, opts.EventID, EventFinish{
				Status:          EventStatusError,
				ChangesReturned: len(changes),
				ChangesResolved: stats.ChangesResolved,
				TargetsClaimed:  stats.TargetsClaimed,
				ScansCreated:    result.Enqueue.Created,
				ScansReused:     result.Enqueue.Reused,
				ScansSuppressed: stats.Suppressed,
				ErrorMessage:    aerr.Error(),
				MarkerAfter:     opts.Marker,
			})
			return result, aerr
		}
	}
	result.Status = status
	s.finishEvent(ctx, opts.EventID, EventFinish{
		Status:          status,
		ChangesReturned: len(changes),
		ChangesResolved: stats.ChangesResolved,
		TargetsClaimed:  stats.TargetsClaimed,
		ScansCreated:    result.Enqueue.Created,
		ScansReused:     result.Enqueue.Reused,
		ScansSuppressed: stats.Suppressed,
		ErrorMessage:    statusMsg,
		MarkerAfter:     opts.NextMarker,
	})
	return result, nil
}

// ChangeIngest is one webhook delivery's worth of changes for a source.
type ChangeIngest struct {
	SourceID          string
	ProviderEventType string
	Changes           []Change
	ReceivedAt        time.Time
}

// IngestResult summarizes what a webhook delivery produced.
type IngestResult struct {
	Enqueued   int
	Suppressed int
	Unresolved bool
	// Pending reports that the delivery was accepted durably but its immediate
	// ingest failed. The retry task will process it again inside Silo.
	Pending bool
}

var errWebhookDeliveryDisabled = errors.New("autoscan: webhook delivery disabled")

const (
	webhookRetryBaseDelay = 5 * time.Second
	webhookRetryMaxDelay  = 10 * time.Minute
)

func webhookRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := webhookRetryBaseDelay
	for i := 1; i < attempt && delay < webhookRetryMaxDelay; i++ {
		delay *= 2
		if delay >= webhookRetryMaxDelay {
			return webhookRetryMaxDelay
		}
	}
	return delay
}

// IngestChanges durably records a webhook delivery before attempting the same
// rewrite/resolve/suppress/enqueue/event pipeline as polling. A transient
// processing failure leaves the delivery queued for an internal retry and is
// surfaced as Pending rather than depending on Sonarr/Radarr to replay it.
func (s *Service) IngestChanges(ctx context.Context, in ChangeIngest) (IngestResult, error) {
	delivery, err := s.store.CreateWebhookDelivery(ctx, in)
	if err != nil {
		return IngestResult{}, err
	}
	return s.processWebhookDelivery(ctx, delivery), nil
}

// RetryPendingWebhookDeliveries claims and consumes a bounded batch of durable
// deliveries. Repository leases make concurrent nodes safe; individual ingest
// failures are rescheduled and do not abort the rest of the batch.
func (s *Service) RetryPendingWebhookDeliveries(ctx context.Context, limit int) (int, error) {
	deliveries, err := s.store.ClaimWebhookDeliveries(ctx, uuid.NewString(), limit)
	if err != nil {
		return 0, err
	}
	for _, delivery := range deliveries {
		s.processWebhookDelivery(ctx, delivery)
	}
	return len(deliveries), nil
}

func (s *Service) processWebhookDelivery(ctx context.Context, delivery WebhookDelivery) IngestResult {
	result, ingestErr := s.ingestChangesNow(ctx, ChangeIngest{
		SourceID:          delivery.SourceID,
		ProviderEventType: delivery.ProviderEventType,
		Changes:           delivery.Changes,
		ReceivedAt:        delivery.ReceivedAt,
	})
	if ingestErr == nil {
		if err := s.store.CompleteWebhookDelivery(ctx, delivery.ID, delivery.LockedBy); err != nil {
			result.Pending = true
			slog.WarnContext(ctx, "autoscan: complete webhook delivery failed", "component", "autoscan", "delivery_id", delivery.ID, "err", err)
		} else {
			if err := s.store.ClearWebhookError(ctx, delivery.SourceID); err != nil {
				slog.WarnContext(ctx, "autoscan: clear webhook error failed", "component", "autoscan", "source_id", delivery.SourceID, "err", err)
			}
		}
		return result
	}
	if errors.Is(ingestErr, errWebhookDeliveryDisabled) {
		// A source/global disable is a pause, not a reason to discard work that
		// was already accepted while enabled. Keep it pending without surfacing a
		// delivery error and try again after the operator re-enables Autoscan.
		result.Pending = true
		if err := s.store.RetryWebhookDelivery(ctx, delivery.ID, delivery.LockedBy, time.Minute, ""); err != nil {
			slog.WarnContext(ctx, "autoscan: pause webhook retry failed", "component", "autoscan", "delivery_id", delivery.ID, "err", err)
		}
		return result
	}

	result.Pending = true
	if err := s.store.RetryWebhookDelivery(
		ctx,
		delivery.ID,
		delivery.LockedBy,
		webhookRetryDelay(delivery.AttemptCount),
		ingestErr.Error(),
	); err != nil {
		// The durable row remains leased and becomes reclaimable when the lease
		// expires, so a bookkeeping failure here still cannot lose the delivery.
		slog.WarnContext(ctx, "autoscan: schedule webhook retry failed", "component", "autoscan", "delivery_id", delivery.ID, "err", err)
	}
	if err := s.store.RecordWebhookError(ctx, delivery.SourceID, ingestErr.Error()); err != nil {
		slog.WarnContext(ctx, "autoscan: record webhook error failed", "component", "autoscan", "source_id", delivery.SourceID, "err", err)
	}
	return result
}

// ingestChangesNow performs one webhook consume attempt. It is deliberately
// separate from IngestChanges so retries do not create nested delivery rows.
func (s *Service) ingestChangesNow(ctx context.Context, in ChangeIngest) (IngestResult, error) {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return IngestResult{}, err
	}
	if !settings.Enabled {
		return IngestResult{}, errWebhookDeliveryDisabled
	}
	src, err := s.store.GetSource(ctx, in.SourceID)
	if err != nil {
		return IngestResult{}, err
	}
	if src.DeliveryMode != DeliveryModeWebhook {
		return IngestResult{}, fmt.Errorf("autoscan: source %s is not a webhook source", src.ID)
	}
	if !src.Enabled {
		return IngestResult{}, errWebhookDeliveryDisabled
	}
	startedAt := in.ReceivedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	// Event bookkeeping failure does not block the scan work (same resilience
	// as polling): eventID 0 makes finishEvent a no-op and enqueues without an
	// event link.
	eventID, err := s.store.CreateEvent(ctx, EventCreate{
		SourceID:          src.ID,
		PluginID:          src.PluginID,
		CapabilityID:      src.CapabilityID,
		StartedAt:         startedAt,
		DeliveryMode:      DeliveryModeWebhook,
		ProviderEventType: in.ProviderEventType,
		SkipRunningCheck:  true,
	})
	if err != nil {
		slog.WarnContext(ctx, "autoscan: create webhook event failed", "component", "autoscan", "source_id", src.ID, "err", err)
		eventID = 0
	}
	result, cerr := s.consumeSourceChanges(ctx, src, in.Changes, consumeOptions{
		EventID: eventID,
		TTL:     time.Duration(settings.DebounceSeconds) * time.Second,
	})
	return IngestResult{
		Enqueued:   result.Enqueue.Created + result.Enqueue.Reused,
		Suppressed: result.Stats.Suppressed,
		Unresolved: result.Status == EventStatusUnresolved,
	}, cerr
}

func (s *Service) enqueueScanTargets(ctx context.Context, targets []scantrigger.Target, eventID int64) (EnqueueResult, error) {
	var result EnqueueResult
	if len(targets) == 0 {
		return result, nil
	}
	for start := 0; start < len(targets); start += maxAutoscanTargetsPerPoll {
		end := start + maxAutoscanTargetsPerPoll
		if end > len(targets) {
			end = len(targets)
		}
		chunk := targets[start:end]
		if eventID != 0 {
			created, reused, err := s.queue.EnqueueAutoscanScans(ctx, chunk, eventID)
			if err != nil {
				return result, err
			}
			result.Created += created
			result.Reused += reused
			continue
		}
		if err := s.queue.EnqueueScans(ctx, chunk); err != nil {
			return result, err
		}
		result.Created += len(chunk)
	}
	return result, nil
}

func (s *Service) createEvent(ctx context.Context, src Source, marker string, startedAt time.Time) (int64, bool) {
	if s == nil || s.store == nil {
		return 0, true
	}
	id, err := s.store.CreateEvent(ctx, EventCreate{
		SourceID:     src.ID,
		PluginID:     src.PluginID,
		CapabilityID: src.CapabilityID,
		StartedAt:    startedAt,
		MarkerBefore: marker,
	})
	if err != nil {
		if errors.Is(err, ErrPollAlreadyRunning) {
			slog.DebugContext(ctx, "autoscan: source poll already running", "component", "autoscan", "source_id", src.ID)
			return 0, false
		}
		slog.WarnContext(ctx, "autoscan: create event failed", "component", "autoscan", "source_id", src.ID, "err", err)
		return 0, true
	}
	return id, true
}

func (s *Service) finishEvent(ctx context.Context, eventID int64, finish EventFinish) {
	if eventID == 0 || s == nil || s.store == nil {
		return
	}
	finish.ID = eventID
	finish.CompletedAt = time.Now()
	if finish.Status == "" {
		finish.Status = EventStatusSuccess
	}
	if err := s.store.FinishEvent(ctx, finish); err != nil {
		slog.WarnContext(ctx, "autoscan: finish event failed", "component", "autoscan", "event_id", eventID, "err", err)
	}
}

// resolveConnection loads and resolves a source's connection to credentials.
func (s *Service) resolveConnection(ctx context.Context, connectionID string) (ResolvedConnection, error) {
	conn, err := s.store.GetConnection(ctx, connectionID)
	if err != nil {
		return ResolvedConnection{}, err
	}
	return s.connres.Resolve(ctx, conn)
}

func rewriteChanges(changes []Change, rewrites []PathRewrite) []Change {
	rewritten := make([]Change, 0, len(changes))
	for _, change := range changes {
		path := applyRewrites(change.SourcePath, rewrites)
		rewritten = append(rewritten, Change{SourcePath: path, Scope: change.Scope})
	}
	return rewritten
}

// resolveAndClaim resolves changes to scan targets and atomically claims them
// via the suppressor. Legacy/auto changes retain the historical parent-dir
// collapse. Structured file changes resolve exact files, and subtree changes
// resolve exact subtree paths even when the path no longer exists.
func (s *Service) resolveAndClaim(ctx context.Context, changes []Change, ttl time.Duration) (targets []scantrigger.Target, claimed []string, resolvedAny bool, stats resolveStats) {
	seenTargets := make(map[string]struct{})

	var legacyPaths []string
	for _, change := range changes {
		switch change.Scope {
		case ChangeScopeFile, ChangeScopeSubtree:
			target, ok := s.resolveChange(ctx, change, &stats)
			if !ok {
				continue
			}
			resolvedAny = true
			stats.ChangesResolved++
			if !s.claimTarget(ctx, *target, ttl, seenTargets, &targets, &claimed) {
				stats.Suppressed++
			}
		default:
			legacyPaths = append(legacyPaths, change.SourcePath)
		}
	}

	for _, dir := range uniqueParentDirs(legacyPaths) {
		target, rerr := s.resolver.Resolve(ctx, scantrigger.Request{Path: dir, Trigger: scanTrigger})
		if isRequestError(rerr) {
			// The directory may have been removed (e.g. a deleted movie
			// folder). Fall back to a reconciling scan of the vanished path so
			// its files are marked missing promptly. Paths outside Silo's
			// media folders still resolve to nothing and are skipped below.
			target, rerr = s.resolver.ResolveVanishedPath(ctx, dir, scanTrigger)
		}
		if rerr != nil {
			var reqErr *scantrigger.RequestError
			if errors.As(rerr, &reqErr) {
				// Path outside Silo's media folders (or otherwise unresolvable)
				// — an expected skip, not an error worth logging every cycle.
				continue
			}
			stats.TransientErrors++
			slog.WarnContext(ctx, "autoscan: resolve failed", "component", "autoscan", "path", dir, "err", rerr)
			continue
		}
		if target == nil || target.Folder == nil {
			continue
		}
		resolvedAny = true
		stats.ChangesResolved++
		if !s.claimTarget(ctx, *target, ttl, seenTargets, &targets, &claimed) {
			stats.Suppressed++
		}
	}
	stats.TargetsClaimed = len(targets)
	return targets, claimed, resolvedAny, stats
}

func collapseTargetsToLibraryScans(targets []scantrigger.Target) []scantrigger.Target {
	if len(targets) == 0 {
		return []scantrigger.Target{}
	}
	seen := make(map[int]struct{})
	collapsed := make([]scantrigger.Target, 0)
	for _, target := range targets {
		if target.Folder == nil {
			continue
		}
		if _, ok := seen[target.Folder.ID]; ok {
			continue
		}
		seen[target.Folder.ID] = struct{}{}
		collapsed = append(collapsed, scantrigger.Target{
			Folder:  target.Folder,
			Mode:    scantrigger.ModeLibrary,
			Trigger: scanTrigger,
		})
	}
	return collapsed
}

// isRequestError reports whether err is a scantrigger.RequestError — the
// resolver's "this path is not scannable as-is" signal, as opposed to an
// internal failure.
func isRequestError(err error) bool {
	var reqErr *scantrigger.RequestError
	return errors.As(err, &reqErr)
}

func (s *Service) resolveChange(ctx context.Context, change Change, stats *resolveStats) (*scantrigger.Target, bool) {
	if change.SourcePath == "" {
		return nil, false
	}
	var (
		target *scantrigger.Target
		err    error
	)
	switch change.Scope {
	case ChangeScopeSubtree:
		target, err = s.resolver.ResolveMissingSubtree(ctx, change.SourcePath, scanTrigger)
	case ChangeScopeFile:
		target, err = s.resolver.Resolve(ctx, scantrigger.Request{Path: change.SourcePath, Trigger: scanTrigger})
		if err == nil && target != nil && target.Mode != scantrigger.ModeFile {
			return nil, false
		}
		if isRequestError(err) {
			// The file may have been deleted (upgrade/replacement). Fall back
			// to a reconciling scan so the stale row is marked missing
			// promptly instead of lingering until the next full library scan.
			target, err = s.resolver.ResolveVanishedPath(ctx, change.SourcePath, scanTrigger)
		}
	default:
		target, err = s.resolver.Resolve(ctx, scantrigger.Request{Path: change.SourcePath, Trigger: scanTrigger})
	}
	if err != nil {
		var reqErr *scantrigger.RequestError
		if errors.As(err, &reqErr) {
			return nil, false
		}
		stats.TransientErrors++
		slog.WarnContext(ctx, "autoscan: resolve failed", "component", "autoscan", "path", change.SourcePath, "scope", change.Scope, "err", err)
		return nil, false
	}
	if target == nil || target.Folder == nil {
		return nil, false
	}
	return target, true
}

func (s *Service) claimTarget(
	ctx context.Context,
	target scantrigger.Target,
	ttl time.Duration,
	seenTargets map[string]struct{},
	targets *[]scantrigger.Target,
	claimed *[]string,
) bool {
	targetKey := fmt.Sprintf("%d|%s|%s", target.Folder.ID, target.Mode, target.Path)
	if _, seen := seenTargets[targetKey]; seen {
		return false
	}
	seenTargets[targetKey] = struct{}{}

	key := fmt.Sprintf("%d|%s", target.Folder.ID, target.Path)
	ok, serr := s.suppress.ShouldScan(ctx, key, ttl)
	if serr != nil || !ok {
		return false
	}
	target.Trigger = scanTrigger
	*targets = append(*targets, target)
	*claimed = append(*claimed, key)
	return true
}

// releaseClaims drops suppression claims (used when the scan enqueue fails so a
// later cycle can retry the same targets).
func (s *Service) releaseClaims(ctx context.Context, claimed []string) {
	for _, k := range claimed {
		if rerr := s.suppress.Release(ctx, k); rerr != nil {
			slog.WarnContext(ctx, "autoscan: release claim failed", "component", "autoscan", "key", k, "err", rerr)
		}
	}
}
