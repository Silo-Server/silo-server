package autoscan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

const scanTrigger = "autoscan"

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
}

// Resolver maps a Silo-native path to a concrete scan target (library folder).
type Resolver interface {
	Resolve(ctx context.Context, req scantrigger.Request) (*scantrigger.Target, error)
}

// Queuer enqueues resolved scan targets.
type Queuer interface {
	EnqueueScans(ctx context.Context, targets []scantrigger.Target) error
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
// can offer them and PollOnce can skip orphaned source rows; it may be nil
// (the picker then returns an empty list and orphan-pruning is disabled).
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

// PollOnce runs one autoscan cycle. Per-source failures are logged and skipped;
// only settings/listing errors propagate. The opaque next marker returned by the
// provider is stored verbatim, but only when the cycle's work is genuinely
// consumed: the provider returned no paths, or it returned paths and at least one
// resolved+enqueued. When paths come back but NONE resolve to a library folder
// (e.g. a freshly-enabled source with unconfigured rewrites) the marker is held
// and an error recorded, so those imports aren't skipped forever.
func (s *Service) PollOnce(ctx context.Context) error {
	// Fetch the set of currently-installed scan_source capabilities so we can skip
	// orphaned source rows (their plugin was uninstalled). Listing failures are
	// non-fatal: a nil set means the installed set is unavailable, in which case
	// we do NOT prune orphans — every enabled source is assumed present.
	present, derr := s.installedScanSources(ctx)
	if derr != nil {
		slog.WarnContext(ctx, "autoscan: list installed scan sources failed", "err", derr)
		present = nil
	}

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
		// Skip orphaned sources: an enabled source whose scan_source plugin has
		// been uninstalled/disabled is no longer in the discovered set, so polling
		// it would error every cycle. Skip it quietly (no RecordError) so the spam
		// stops; the operator can delete it via the source delete endpoint. A nil
		// `present` set means discovery is unavailable — don't prune in that case.
		if present != nil {
			if _, ok := present[installedKey{InstallationID: src.InstallationID, CapabilityID: src.CapabilityID}]; !ok {
				continue
			}
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
				slog.WarnContext(ctx, "autoscan: resolve connection failed", "source_id", src.ID, "err", cerr)
				if rerr := s.store.RecordError(ctx, src.ID, cerr.Error()); rerr != nil {
					slog.WarnContext(ctx, "autoscan: record error failed", "source_id", src.ID, "err", rerr)
				}
				continue
			}
			conn = resolved
		}
		marker := ""
		if src.Marker != nil {
			marker = *src.Marker
		}
		paths, next, perr := s.provider.PollChanges(ctx, src.InstallationID, src.CapabilityID, marker, conn)
		if perr != nil {
			slog.WarnContext(ctx, "autoscan: poll changes failed", "source_id", src.ID, "err", perr)
			if rerr := s.store.RecordError(ctx, src.ID, perr.Error()); rerr != nil {
				slog.WarnContext(ctx, "autoscan: record error failed", "source_id", src.ID, "err", rerr)
			}
			continue // do NOT advance marker
		}

		// The merged scan_source contract returns RAW source-namespace paths;
		// rewrite ownership moved from the plugin to the host. Normalize Windows
		// separators and apply this source's per-source prefix rewrites to turn
		// each raw path Silo-native BEFORE dedupe/resolve/enqueue.
		rewritten := make([]string, 0, len(paths))
		for _, p := range paths {
			rewritten = append(rewritten, applyRewrites(normalizeSeparators(p), src.PathRewrites))
		}

		targets, claimed, resolvedAny := s.resolveAndClaim(ctx, rewritten, ttl)
		if len(targets) > 0 {
			if eerr := s.queue.EnqueueScans(ctx, targets); eerr != nil {
				s.releaseClaims(ctx, claimed)
				slog.WarnContext(ctx, "autoscan: enqueue failed", "source_id", src.ID, "err", eerr)
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
		//   - returned paths AND NOTHING resolved  → do NOT advance. This is the
		//     freshly-enabled-source state (path_rewrites not configured yet); the
		//     incoming paths don't map to any Silo library folder. Advancing here
		//     would skip those imports forever. Surface why so the operator can fix
		//     the rewrites, then a later poll re-reads the same window and resolves.
		// (Some-but-not-all resolving counts as resolved — the unresolved paths are
		// legitimately outside Silo's libraries, so advancing is correct.)
		//
		// NOTE: len(targets)==0 alone is NOT misconfiguration — paths can resolve
		// yet be fully suppressed. Gate the error on resolvedAny, not targets.
		if len(paths) > 0 && !resolvedAny {
			msg := fmt.Sprintf("returned %d path(s) but none matched a Silo library folder — check this source's path rewrites", len(paths))
			if rerr := s.store.RecordError(ctx, src.ID, msg); rerr != nil {
				slog.WarnContext(ctx, "autoscan: record error failed", "source_id", src.ID, "err", rerr)
			}
			continue // do NOT advance marker
		}
		if aerr := s.store.AdvanceMarker(ctx, src.ID, next); aerr != nil {
			slog.WarnContext(ctx, "autoscan: advance marker failed", "source_id", src.ID, "err", aerr)
		}
	}
	return nil
}

// resolveConnection loads and resolves a source's connection to credentials.
func (s *Service) resolveConnection(ctx context.Context, connectionID string) (ResolvedConnection, error) {
	conn, err := s.store.GetConnection(ctx, connectionID)
	if err != nil {
		return ResolvedConnection{}, err
	}
	return s.connres.Resolve(ctx, conn)
}

// resolveAndClaim dedupes the changed paths to parent directories, resolves each
// to a scan target, and atomically claims it via the suppressor. It returns the
// targets to enqueue, the suppression keys claimed for them (so they can be
// released if the enqueue fails), and resolvedAny — whether ANY path resolved to
// a Silo library folder, independent of suppression. resolvedAny lets PollOnce
// distinguish "nothing resolved → misconfiguration" from "resolved but all
// suppressed → debounced, work effectively done." This is the PR #43 salvage
// loop, generalized to take already-Silo-native paths.
func (s *Service) resolveAndClaim(ctx context.Context, paths []string, ttl time.Duration) (targets []scantrigger.Target, claimed []string, resolvedAny bool) {
	for _, dir := range uniqueParentDirs(paths) {
		target, rerr := s.resolver.Resolve(ctx, scantrigger.Request{Path: dir, Trigger: scanTrigger})
		if rerr != nil {
			var reqErr *scantrigger.RequestError
			if errors.As(rerr, &reqErr) {
				// Path outside Silo's media folders (or otherwise unresolvable)
				// — an expected skip, not an error worth logging every cycle.
				continue
			}
			slog.WarnContext(ctx, "autoscan: resolve failed", "path", dir, "err", rerr)
			continue
		}
		if target == nil || target.Folder == nil {
			continue
		}
		// This path maps to a real Silo library folder, regardless of whether the
		// suppressor lets us claim it below.
		resolvedAny = true
		// Key on (folder, path) to match the scanqueue dedup granularity so two
		// distinct subtrees under one library folder are not collapsed.
		key := fmt.Sprintf("%d|%s", target.Folder.ID, target.Path)
		ok, serr := s.suppress.ShouldScan(ctx, key, ttl)
		if serr != nil || !ok {
			continue
		}
		target.Trigger = scanTrigger
		targets = append(targets, *target)
		claimed = append(claimed, key)
	}
	return targets, claimed, resolvedAny
}

// releaseClaims drops suppression claims (used when the scan enqueue fails so a
// later cycle can retry the same targets).
func (s *Service) releaseClaims(ctx context.Context, claimed []string) {
	for _, k := range claimed {
		if rerr := s.suppress.Release(ctx, k); rerr != nil {
			slog.WarnContext(ctx, "autoscan: release claim failed", "key", k, "err", rerr)
		}
	}
}
