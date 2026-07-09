package autoscan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/scantrigger"
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

		rewritten := rewriteChanges(changes, src.PathRewrites)
		targets, claimed, resolvedAny, stats := s.resolveAndClaim(ctx, rewritten, ttl)
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
		var enqueue EnqueueResult
		if len(targets) > 0 {
			var eerr error
			enqueue, eerr = s.enqueueScanTargets(ctx, targets, eventID)
			if eerr != nil {
				s.releaseClaims(ctx, claimed)
				slog.WarnContext(ctx, "autoscan: enqueue failed", "component", "autoscan", "source_id", src.ID, "err", eerr)
				s.finishEvent(ctx, eventID, EventFinish{
					Status:          EventStatusError,
					ChangesReturned: len(changes),
					ChangesResolved: stats.ChangesResolved,
					TargetsClaimed:  stats.TargetsClaimed,
					ScansSuppressed: stats.Suppressed,
					ErrorMessage:    eerr.Error(),
					MarkerAfter:     marker,
				})
				continue // do NOT advance marker
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
		// NOTE: len(targets)==0 alone is NOT misconfiguration — paths can resolve
		// yet be fully suppressed. Gate on resolvedAny, not targets.
		if stats.TransientErrors > 0 {
			msg := fmt.Sprintf("%d resolve attempt(s) failed internally (%d of %d path(s) resolved) — holding marker to retry", stats.TransientErrors, stats.ChangesResolved, len(changes))
			if rerr := s.store.RecordError(ctx, src.ID, msg); rerr != nil {
				slog.WarnContext(ctx, "autoscan: record error failed", "component", "autoscan", "source_id", src.ID, "err", rerr)
			}
			s.finishEvent(ctx, eventID, EventFinish{
				Status:          EventStatusError,
				ChangesReturned: len(changes),
				ChangesResolved: stats.ChangesResolved,
				TargetsClaimed:  stats.TargetsClaimed,
				ScansCreated:    enqueue.Created,
				ScansReused:     enqueue.Reused,
				ScansSuppressed: stats.Suppressed,
				ErrorMessage:    msg,
				MarkerAfter:     marker,
			})
			continue // do NOT advance marker
		}
		status := EventStatusSuccess
		var statusMsg string
		if len(changes) > 0 && !resolvedAny {
			status = EventStatusUnresolved
			statusMsg = fmt.Sprintf("returned %d path(s) but none matched a Silo library folder — advanced past them", len(changes))
			slog.WarnContext(ctx, "autoscan: returned paths matched no library folder — advancing marker",
				"source_id", src.ID, "changes", len(changes))
		}
		if aerr := s.store.AdvanceMarker(ctx, src.ID, next); aerr != nil {
			slog.WarnContext(ctx, "autoscan: advance marker failed", "component", "autoscan", "source_id", src.ID, "err", aerr)
			s.finishEvent(ctx, eventID, EventFinish{
				Status:          EventStatusError,
				ChangesReturned: len(changes),
				ChangesResolved: stats.ChangesResolved,
				TargetsClaimed:  stats.TargetsClaimed,
				ScansCreated:    enqueue.Created,
				ScansReused:     enqueue.Reused,
				ScansSuppressed: stats.Suppressed,
				ErrorMessage:    aerr.Error(),
				MarkerAfter:     marker,
			})
			continue
		}
		s.finishEvent(ctx, eventID, EventFinish{
			Status:          status,
			ChangesReturned: len(changes),
			ChangesResolved: stats.ChangesResolved,
			TargetsClaimed:  stats.TargetsClaimed,
			ScansCreated:    enqueue.Created,
			ScansReused:     enqueue.Reused,
			ScansSuppressed: stats.Suppressed,
			ErrorMessage:    statusMsg,
			MarkerAfter:     next,
		})
	}
	return nil
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
		path := applyRewrites(normalizeSeparators(change.SourcePath), rewrites)
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
