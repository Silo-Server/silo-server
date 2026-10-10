package autoscan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
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
	AdvanceMarker(ctx context.Context, adv MarkerAdvance) (bool, error)
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
	// EnqueueAutoscanScans returns one outcome per target, in order.
	EnqueueAutoscanScans(ctx context.Context, targets []scantrigger.Target, eventID int64) ([]scantrigger.EnqueueOutcome, error)
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

	// observe reads a reported path's debounce state (observePathState);
	// tests substitute a fake filesystem.
	observe func(path string) (state string, debounce bool)

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
		observe:  observePathState,
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
//
// PollOnce is the scheduled cycle: a source polled within its interval
// (PollIntervalSeconds, else the default) is skipped.
func (s *Service) PollOnce(ctx context.Context) error {
	return s.poll(ctx, false)
}

// PollNow runs the same cycle for an operator's manual run: every enabled
// polling source is polled immediately, regardless of its interval. Autoscan
// being disabled, disabled sources, webhook sources, and a poll already
// running for a source still skip it.
func (s *Service) PollNow(ctx context.Context) error {
	return s.poll(ctx, true)
}

func (s *Service) poll(ctx context.Context, ignoreIntervals bool) error {
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
	// Descriptors are only needed for sources with no bound connection, so the
	// installed-plugin listing is read at most once per cycle and only then.
	var requirements map[sourceIdentity]ConnectionRequirement
	requirementsLoaded := false

	for _, src := range sources {
		// Webhook sources are fed by IngestChanges when the provider POSTs to
		// their endpoint; there is nothing to poll and no plugin to invoke.
		if src.DeliveryMode == DeliveryModeWebhook {
			continue
		}
		// Scheduled cycles honor the per-source poll interval as a "poll at most
		// every N seconds" floor: the global task fires at the default cadence,
		// so a source with a longer interval is skipped until enough time has
		// elapsed. A manual run polls every source now.
		if !ignoreIntervals {
			interval := time.Duration(settings.DefaultPollIntervalSeconds) * time.Second
			if src.PollIntervalSeconds != nil {
				interval = time.Duration(*src.PollIntervalSeconds) * time.Second
			}
			if src.LastRunAt != nil && now.Sub(*src.LastRunAt) < interval {
				continue
			}
		}
		// The list was read at the start of the cycle, and the sources ahead
		// of this one may have taken a while. An admin edit since then may have
		// reset the marker (a new connection or config, or a repointed
		// connection), so poll from the current row: sending the listed marker
		// to a new upstream would replay or skip its history.
		current, ok := s.currentPollSource(ctx, src)
		if !ok {
			continue
		}
		src = current
		marker := ""
		if src.Marker != nil {
			marker = *src.Marker
		}
		eventID, started := s.createEvent(ctx, src, marker, time.Now())
		if !started {
			continue
		}
		// Whether a connection is needed comes from the source's descriptor.
		// Server-based providers (Sonarr/Radarr) bind a connection and the
		// resolved {base_url, api_key} is handed to the plugin. Other providers
		// (e.g. a filesystem/CephFS watcher) need none and get an empty
		// connection they ignore. A source whose descriptor requires a
		// connection but has none bound is not sent to the plugin: the call can
		// only fail, and the host can say what to fix more plainly than the
		// plugin's error would.
		if src.ConnectionID == nil {
			if !requirementsLoaded {
				requirements = s.connectionRequirements(ctx)
				requirementsLoaded = true
			}
			if requirements[sourceIdentity{src.PluginID, src.CapabilityID}] == ConnectionRequired {
				s.failPoll(ctx, src, eventID, marker, missingConnectionMessage)
				continue
			}
		}
		var conn ResolvedConnection
		var connRow *Connection
		if src.ConnectionID != nil {
			row, resolved, cerr := s.resolveConnection(ctx, *src.ConnectionID)
			if cerr != nil {
				slog.WarnContext(ctx, "autoscan: resolve connection failed", "component", "autoscan", "source_id", src.ID, "err", cerr)
				s.failPoll(ctx, src, eventID, marker, cerr.Error())
				continue
			}
			conn = resolved
			connRow = &row
		}
		changes, next, perr := s.provider.PollChanges(ctx, src.PluginID, src.CapabilityID, marker, conn, src.SourceConfig)
		if perr != nil {
			slog.WarnContext(ctx, "autoscan: poll changes failed", "component", "autoscan", "source_id", src.ID, "err", perr)
			s.failPoll(ctx, src, eventID, marker, pollErrorMessage(perr))
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
			Connection:    connRow,
		})
	}
	return nil
}

// missingConnectionMessage is recorded for a poll source whose descriptor
// requires a connection when none is bound. It is written for the operator,
// who sees it on the source row and in poll activity.
const missingConnectionMessage = "No server selected. Edit the source and choose a server."

// failPoll records a poll that ended before the provider returned changes: the
// source's last_error and the event both carry msg, and the marker is held so
// the next poll re-reads the same window.
func (s *Service) failPoll(ctx context.Context, src Source, eventID int64, marker, msg string) {
	if rerr := s.store.RecordError(ctx, src.ID, msg); rerr != nil {
		slog.WarnContext(ctx, "autoscan: record error failed", "component", "autoscan", "source_id", src.ID, "err", rerr)
	}
	s.finishEvent(ctx, eventID, EventFinish{
		Status:       EventStatusError,
		ErrorMessage: msg,
		MarkerAfter:  marker,
	})
}

// sourceIdentity is the (plugin, capability) pair a source is created against.
type sourceIdentity struct {
	pluginID     string
	capabilityID string
}

// connectionRequirements maps each discoverable scan-source identity to its
// resolved descriptor's connection requirement. It reads the same normalized
// descriptors as the source write check (ListAvailableScanSources), so a
// source the write path accepted is judged the same way here. A missing lister
// or a listing failure yields nil, which leaves every source to the plugin's
// own judgement: a transient listing fault must not stop otherwise working
// sources polling.
func (s *Service) connectionRequirements(ctx context.Context) map[sourceIdentity]ConnectionRequirement {
	if s.lister == nil {
		return nil
	}
	discovered, err := s.ListAvailableScanSources(ctx)
	if err != nil {
		slog.WarnContext(ctx, "autoscan: list scan sources for connection requirements failed", "component", "autoscan", "err", err)
		return nil
	}
	out := make(map[sourceIdentity]ConnectionRequirement, len(discovered))
	for _, d := range discovered {
		out[sourceIdentity{d.PluginID, d.CapabilityID}] = d.Descriptor.Connection
	}
	return out
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
	// Connection is the connection row the poll read from (nil when the
	// source has none); the marker advance is conditional on it.
	Connection *Connection
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
	records := newChangeRecords(changes, rewritten)
	targets, claimed, resolvedAny, stats := s.resolveAndClaim(ctx, rewritten, records, opts.TTL)
	result := consumeResult{Stats: stats, ResolvedAny: resolvedAny}
	// finish stamps the bounded change log onto every terminal event update.
	finish := func(f EventFinish) {
		f.Changes, f.ChangesTruncated = boundChangeRecords(records)
		s.finishEvent(ctx, opts.EventID, f)
	}
	if len(targets) > maxAutoscanTargetsPerPoll {
		collapsed := collapseTargetsToLibraryScans(targets)
		collapsePendingToLibraries(records)
		slog.WarnContext(ctx, "autoscan: collapsed large scan target batch to library scans", "component", "autoscan",
			"source_id", src.ID,
			"targets", len(targets),
			"collapsed_targets", len(collapsed),
			"limit", maxAutoscanTargetsPerPoll,
		)
		targets = collapsed
	}
	if len(targets) > 0 {
		enqueue, outcomes, eerr := s.enqueueScanTargets(ctx, targets, opts.EventID)
		result.Enqueue = enqueue
		if eerr != nil {
			s.releaseClaims(ctx, claimed)
			failPending(records)
			slog.WarnContext(ctx, "autoscan: enqueue failed", "component", "autoscan", "source_id", src.ID, "err", eerr)
			result.Status = EventStatusError
			finish(EventFinish{
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
		applyEnqueueOutcomes(records, targets, outcomes)
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
		finish(EventFinish{
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
	var statusMsg, unresolvedMsg string
	markerAfter := opts.NextMarker
	if len(changes) > 0 && !resolvedAny {
		status = EventStatusUnresolved
		unresolvedMsg = fmt.Sprintf("returned %d path(s) but none matched a Silo library folder", len(changes))
		statusMsg = unresolvedMsg
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
		advanced, aerr := s.store.AdvanceMarker(ctx, MarkerAdvance{
			Source:     src,
			Connection: opts.Connection,
			NextMarker: opts.NextMarker,
		})
		if aerr != nil {
			slog.WarnContext(ctx, "autoscan: advance marker failed", "component", "autoscan", "source_id", src.ID, "err", aerr)
			result.Status = EventStatusError
			finish(EventFinish{
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
		if !advanced {
			// The source changed while this poll ran: an admin reset its
			// marker by changing its connection, config or upstream. The
			// window belongs to the old upstream, so the next poll starts
			// from the reset marker instead, and the event must not claim
			// an advance the source never stored.
			slog.DebugContext(ctx, "autoscan: source changed during poll; not storing its marker", "component", "autoscan", "source_id", src.ID)
			markerAfter = opts.Marker
			statusMsg = unresolvedMsg
			if statusMsg != "" {
				statusMsg += "; "
			}
			statusMsg += markerNotStoredMessage
		}
	}
	result.Status = status
	finish(EventFinish{
		Status:          status,
		ChangesReturned: len(changes),
		ChangesResolved: stats.ChangesResolved,
		TargetsClaimed:  stats.TargetsClaimed,
		ScansCreated:    result.Enqueue.Created,
		ScansReused:     result.Enqueue.Reused,
		ScansSuppressed: stats.Suppressed,
		ErrorMessage:    statusMsg,
		MarkerAfter:     markerAfter,
	})
	return result, nil
}

// markerNotStoredMessage notes on a poll event that the source changed while
// it ran, so the window's marker was dropped rather than stored.
const markerNotStoredMessage = "source changed during the poll; its marker was not stored"

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

// enqueueScanTargets enqueues targets in bounded chunks. With an event it also
// returns one outcome per target, aligned with targets; event-less enqueues
// report no per-target outcomes.
func (s *Service) enqueueScanTargets(ctx context.Context, targets []scantrigger.Target, eventID int64) (EnqueueResult, []scantrigger.EnqueueOutcome, error) {
	var result EnqueueResult
	if len(targets) == 0 {
		return result, nil, nil
	}
	var outcomes []scantrigger.EnqueueOutcome
	for start := 0; start < len(targets); start += maxAutoscanTargetsPerPoll {
		end := start + maxAutoscanTargetsPerPoll
		if end > len(targets) {
			end = len(targets)
		}
		chunk := targets[start:end]
		if eventID != 0 {
			chunkOutcomes, err := s.queue.EnqueueAutoscanScans(ctx, chunk, eventID)
			if err != nil {
				return result, nil, err
			}
			for _, outcome := range chunkOutcomes {
				if outcome.Created {
					result.Created++
				} else {
					result.Reused++
				}
			}
			outcomes = append(outcomes, chunkOutcomes...)
			continue
		}
		if err := s.queue.EnqueueScans(ctx, chunk); err != nil {
			return result, nil, err
		}
		result.Created += len(chunk)
	}
	return result, outcomes, nil
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

// currentPollSource re-reads src just before it is polled. It reports false
// when the source is gone, is no longer an enabled poll source, or cannot be
// read. A failed read skips the source for this cycle rather than polling the
// listed row, whose marker may belong to an upstream an admin has since
// replaced; its last_run_at is untouched, so the next cycle retries it.
func (s *Service) currentPollSource(ctx context.Context, src Source) (Source, bool) {
	current, err := s.store.GetSource(ctx, src.ID)
	if errors.Is(err, ErrNotFound) {
		return Source{}, false
	}
	if err != nil {
		slog.WarnContext(ctx, "autoscan: re-read source before poll failed; skipping it this cycle", "component", "autoscan", "source_id", src.ID, "err", err)
		return Source{}, false
	}
	if !current.Enabled || current.DeliveryMode == DeliveryModeWebhook {
		return Source{}, false
	}
	return current, true
}

// resolveConnection loads a source's connection and resolves it to
// credentials, returning both the row and the credentials.
func (s *Service) resolveConnection(ctx context.Context, connectionID string) (Connection, ResolvedConnection, error) {
	conn, err := s.store.GetConnection(ctx, connectionID)
	if err != nil {
		return Connection{}, ResolvedConnection{}, err
	}
	resolved, err := s.connres.Resolve(ctx, conn)
	return conn, resolved, err
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
// resolve exact subtree paths even when the path no longer exists. records is
// aligned with changes and receives each change's target and outcome; claimed
// changes are left pending until the enqueue reports which run covers them.
//
// Every claim is keyed on the path the source reported, not the scan target,
// together with that path's observed state (see Suppressor). A video file
// resolves to a scan of its directory, and two different files landing in one
// directory a few seconds apart are two events that both need serving; the
// queue coalesces the second into the running scan and owes it a follow-up,
// which a suppression keyed on the directory would have swallowed. Likewise a
// file deleted or replaced shortly after it was imported is a new change, not
// a duplicate of the import.
func (s *Service) resolveAndClaim(ctx context.Context, changes []Change, records []ChangeRecord, ttl time.Duration) (targets []scantrigger.Target, claimed []suppressClaim, resolvedAny bool, stats resolveStats) {
	seenTargets := make(map[string]struct{})

	var legacyPaths []string
	legacyByDir := make(map[string][]int)
	for i, change := range changes {
		rec := &records[i]
		switch change.Scope {
		case ChangeScopeFile, ChangeScopeSubtree:
			target, ok := s.resolveChange(ctx, change, &stats, rec)
			if !ok {
				continue
			}
			resolvedAny = true
			stats.ChangesResolved++
			rec.setTarget(*target)
			if !s.claimTarget(ctx, *target, filepath.Clean(change.SourcePath), ttl, seenTargets, &targets, &claimed) {
				stats.Suppressed++
				rec.setOutcome(ChangeOutcomeSuppressed, "", "")
				continue
			}
			rec.pendingTarget = scanTargetKey(*target)
		default:
			if change.SourcePath == "" {
				rec.setOutcome(ChangeOutcomeIgnored, string(scantrigger.ReasonPathRequired), "")
				continue
			}
			legacyPaths = append(legacyPaths, change.SourcePath)
			dir := filepath.Dir(change.SourcePath)
			legacyByDir[dir] = append(legacyByDir[dir], i)
		}
	}

	for _, group := range groupByParentDir(legacyPaths) {
		dir := group.Dir
		indexes := legacyByDir[dir]
		apply := func(fn func(*ChangeRecord)) {
			for _, i := range indexes {
				fn(&records[i])
			}
		}
		target, rerr := s.resolver.Resolve(ctx, scantrigger.Request{Path: dir, Trigger: scanTrigger})
		primaryErr := rerr
		var fallbackErr error
		if isRequestError(rerr) {
			// The directory may have been removed (e.g. a deleted movie
			// folder). Fall back to a reconciling scan of the vanished path so
			// its files are marked missing promptly. Paths outside Silo's
			// media folders still resolve to nothing and are skipped below.
			target, rerr = s.resolver.ResolveVanishedPath(ctx, dir, scanTrigger)
			fallbackErr = rerr
		}
		if rerr != nil {
			var reqErr *scantrigger.RequestError
			if errors.As(rerr, &reqErr) {
				// Path outside Silo's media folders (or otherwise unresolvable)
				// — an expected skip, not an error worth logging every cycle.
				apply(func(rec *ChangeRecord) { rec.setUnresolved(primaryErr, fallbackErr) })
				continue
			}
			stats.TransientErrors++
			slog.WarnContext(ctx, "autoscan: resolve failed", "component", "autoscan", "path", dir, "err", rerr)
			apply(func(rec *ChangeRecord) {
				rec.setOutcome(ChangeOutcomeError, ChangeReasonResolveFailed, rerr.Error())
			})
			continue
		}
		if target == nil || target.Folder == nil {
			apply(func(rec *ChangeRecord) {
				rec.setOutcome(ChangeOutcomeUnresolved, string(scantrigger.ReasonNoLibraryMatch), "")
			})
			continue
		}
		resolvedAny = true
		// The directory scan serves every reported path in it; each path is
		// claimed on its own so a new change to one file is not debounced by
		// an earlier report of a sibling.
		key := scanTargetKey(*target)
		claimedPaths := make(map[string]bool, len(group.Paths))
		for _, path := range group.Paths {
			stats.ChangesResolved++
			claimedPaths[path] = s.claimTarget(ctx, *target, filepath.Clean(path), ttl, seenTargets, &targets, &claimed)
			if !claimedPaths[path] {
				stats.Suppressed++
			}
		}
		for _, i := range indexes {
			rec := &records[i]
			rec.setTarget(*target)
			if claimedPaths[changes[i].SourcePath] {
				rec.pendingTarget = key
			} else {
				rec.setOutcome(ChangeOutcomeSuppressed, "", "")
			}
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

func (s *Service) resolveChange(ctx context.Context, change Change, stats *resolveStats, rec *ChangeRecord) (*scantrigger.Target, bool) {
	if change.SourcePath == "" {
		rec.setOutcome(ChangeOutcomeIgnored, string(scantrigger.ReasonPathRequired), "")
		return nil, false
	}
	var (
		target      *scantrigger.Target
		err         error
		primaryErr  error
		fallbackErr error
	)
	switch change.Scope {
	case ChangeScopeSubtree:
		target, err = s.resolver.ResolveMissingSubtree(ctx, change.SourcePath, scanTrigger)
	case ChangeScopeFile:
		target, err = s.resolver.Resolve(ctx, scantrigger.Request{Path: change.SourcePath, Trigger: scanTrigger})
		// A file change resolves to the file itself or, for video, to its
		// directory. A change that resolves to a whole library was not a file
		// path and is dropped rather than turned into a full scan.
		if err == nil && target != nil && target.Mode == scantrigger.ModeLibrary {
			rec.setTarget(*target)
			rec.setOutcome(ChangeOutcomeIgnored, ChangeReasonResolvesToLibrary, "")
			return nil, false
		}
		if isRequestError(err) {
			// The file may have been deleted (upgrade/replacement). Fall back
			// to a reconciling scan so the stale row is marked missing
			// promptly instead of lingering until the next full library scan.
			primaryErr = err
			target, err = s.resolver.ResolveVanishedPath(ctx, change.SourcePath, scanTrigger)
			fallbackErr = err
		}
	default:
		target, err = s.resolver.Resolve(ctx, scantrigger.Request{Path: change.SourcePath, Trigger: scanTrigger})
	}
	if err != nil {
		var reqErr *scantrigger.RequestError
		if errors.As(err, &reqErr) {
			if primaryErr == nil {
				primaryErr = err
			}
			rec.setUnresolved(primaryErr, fallbackErr)
			return nil, false
		}
		stats.TransientErrors++
		slog.WarnContext(ctx, "autoscan: resolve failed", "component", "autoscan", "path", change.SourcePath, "scope", change.Scope, "err", err)
		rec.setOutcome(ChangeOutcomeError, ChangeReasonResolveFailed, err.Error())
		return nil, false
	}
	if target == nil || target.Folder == nil {
		rec.setOutcome(ChangeOutcomeUnresolved, string(scantrigger.ReasonNoLibraryMatch), "")
		return nil, false
	}
	return target, true
}

// claimTarget claims one scan target for this cycle. debouncePath is the
// local path the suppression window is keyed on: the change the source
// reported, which may be narrower than target.Path. The claim records the
// path's observed state, so only a repeat report of an unchanged path is
// suppressed; a path whose state cannot prove a repeat (a directory) is never
// suppressed and replaces any earlier claim on the path. Within one cycle, changes that widen to the same target are
// still collapsed to one enqueue; the queue's own dedupe handles the
// cross-cycle case.
func (s *Service) claimTarget(
	ctx context.Context,
	target scantrigger.Target,
	debouncePath string,
	ttl time.Duration,
	seenTargets map[string]struct{},
	targets *[]scantrigger.Target,
	claimed *[]suppressClaim,
) bool {
	if ttl > 0 { // a zero window disables debouncing; skip the stat
		key := fmt.Sprintf("%d|%s", target.Folder.ID, debouncePath)
		state, debounce := s.observe(debouncePath)
		if debounce {
			ok, serr := s.suppress.ShouldScan(ctx, key, state, ttl)
			if serr != nil || !ok {
				return false
			}
			*claimed = append(*claimed, suppressClaim{key: key, state: state})
		} else {
			// This report always scans, but it must still replace an earlier
			// claim on the path: a stale "absent" claim would otherwise drop
			// the next delete of a path that was re-created in between.
			_, _ = s.suppress.ShouldScan(ctx, key, stateUnobserved, ttl)
		}
	}

	targetKey := scanTargetKey(target)
	if _, seen := seenTargets[targetKey]; seen {
		// Claimed, but already enqueued this cycle under another change that
		// resolved to the same target. Not a suppression: the enqueue covers
		// it, because the target has not started running yet.
		return true
	}
	seenTargets[targetKey] = struct{}{}
	target.Trigger = scanTrigger
	*targets = append(*targets, target)
	return true
}

// suppressClaim is one debounce claim this cycle wrote.
type suppressClaim struct{ key, state string }

// releaseClaims drops suppression claims (used when the scan enqueue fails so a
// later cycle can retry the same targets).
func (s *Service) releaseClaims(ctx context.Context, claimed []suppressClaim) {
	for _, c := range claimed {
		if rerr := s.suppress.Release(ctx, c.key, c.state); rerr != nil {
			slog.WarnContext(ctx, "autoscan: release claim failed", "component", "autoscan", "key", c.key, "err", rerr)
		}
	}
}
