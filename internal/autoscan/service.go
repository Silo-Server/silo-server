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

type Service struct {
	store    Store
	provider ScanSourceProvider
	connres  connectionResolver
	resolver Resolver
	queue    Queuer
	suppress Suppressor
}

// NewService builds the autoscan engine. The provider supplies changed paths
// from scan_source plugins; connres resolves a source's connection to concrete
// credentials; resolver/queue/suppress drive the resolve→suppress→enqueue loop.
func NewService(
	store Store,
	provider ScanSourceProvider,
	connres connectionResolver,
	resolver Resolver,
	queue Queuer,
	suppress Suppressor,
) *Service {
	return &Service{
		store:    store,
		provider: provider,
		connres:  connres,
		resolver: resolver,
		queue:    queue,
		suppress: suppress,
	}
}

// PollOnce runs one autoscan cycle. Per-source failures are logged and skipped;
// only settings/listing errors propagate. The opaque next marker returned by the
// provider is stored verbatim, but only after a successful enqueue.
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

	for _, src := range sources {
		conn, cerr := s.resolveConnection(ctx, src.ConnectionID)
		if cerr != nil {
			slog.WarnContext(ctx, "autoscan: resolve connection failed", "source_id", src.ID, "err", cerr)
			continue
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

		targets, claimed := s.resolveAndClaim(ctx, paths, ttl)
		if len(targets) > 0 {
			if eerr := s.queue.EnqueueScans(ctx, targets); eerr != nil {
				s.releaseClaims(ctx, claimed)
				slog.WarnContext(ctx, "autoscan: enqueue failed", "source_id", src.ID, "err", eerr)
				continue // do NOT advance marker
			}
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
// targets to enqueue and the suppression keys claimed for them (so they can be
// released if the enqueue fails). This is the PR #43 salvage loop, generalized
// to take already-Silo-native paths.
func (s *Service) resolveAndClaim(ctx context.Context, paths []string, ttl time.Duration) ([]scantrigger.Target, []string) {
	var targets []scantrigger.Target
	var claimed []string
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
	return targets, claimed
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
