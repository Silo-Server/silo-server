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

const (
	pollOverlap = 2 * time.Minute // re-poll a small overlap so boundary events aren't missed
	maxLookback = 24 * time.Hour  // cap the window so a long outage can't produce an oversized response
)

type Store interface {
	GetSettings(ctx context.Context) (Settings, error)
	ListEnabledSources(ctx context.Context) ([]Source, error)
	AdvanceLastPoll(ctx context.Context, integrationID string, at time.Time) error
	GetSource(ctx context.Context, integrationID string) (*Source, error)
}

type Resolver interface {
	Resolve(ctx context.Context, req scantrigger.Request) (*scantrigger.Target, error)
}

type Queuer interface {
	EnqueueScans(ctx context.Context, targets []scantrigger.Target) error
}

type SecretResolver interface {
	Get(ctx context.Context, key string) (string, error)
}

type Service struct {
	store       Store
	history     HistoryClient
	resolver    Resolver
	queue       Queuer
	suppress    Suppressor
	secrets     SecretResolver
	now         func() time.Time
	rootFolders RootFolderClient
	folders     FolderLister
}

func NewService(store Store, history HistoryClient, resolver Resolver, queue Queuer, suppress Suppressor, secrets SecretResolver) *Service {
	return &Service{
		store: store, history: history, resolver: resolver, queue: queue,
		suppress: suppress, secrets: secrets,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// PollOnce runs one autoscan cycle. Per-source failures are logged and skipped;
// only settings/listing errors propagate.
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
		cycleStart := s.now()
		// First enable: last_poll_at null -> floor at cycleStart (don't replay history).
		since := cycleStart
		if src.LastPollAt != nil {
			since = *src.LastPollAt
		}
		// Overlap buffer absorbs clock skew so boundary events aren't missed.
		since = since.Add(-pollOverlap)
		// Floor the window so a long outage can't produce an oversized history
		// response that arrclient truncates (permanent stall otherwise).
		if floor := cycleStart.Add(-maxLookback); since.Before(floor) {
			since = floor
		}

		apiKey := src.APIKeyRef
		if s.secrets != nil && apiKey != "" {
			resolved, rerr := s.secrets.Get(ctx, apiKey)
			if rerr != nil {
				slog.WarnContext(ctx, "autoscan: resolve api key failed", "integration_id", src.IntegrationID, "err", rerr)
				continue
			}
			if resolved != "" {
				apiKey = resolved
			}
		}

		paths, perr := s.history.ImportedPaths(ctx, src.BaseURL, apiKey, since)
		if perr != nil {
			slog.WarnContext(ctx, "autoscan: source poll failed", "integration_id", src.IntegrationID, "err", perr)
			continue
		}

		rewritten := make([]string, 0, len(paths))
		for _, p := range paths {
			// Normalize Windows separators so an arr running on Windows (reporting
			// C:\Media\... paths) still rewrites/resolves on the Linux host. Path
			// rewrites and Silo media folders are expected to use forward slashes.
			rewritten = append(rewritten, applyRewrites(normalizeSeparators(p), src.PathRewrites))
		}
		var targets []scantrigger.Target
		var claimed []string
		for _, dir := range uniqueParentDirs(rewritten) {
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
		if len(targets) > 0 {
			if eerr := s.queue.EnqueueScans(ctx, targets); eerr != nil {
				slog.WarnContext(ctx, "autoscan: enqueue failed", "integration_id", src.IntegrationID, "err", eerr)
				for _, k := range claimed {
					if rerr := s.suppress.Release(ctx, k); rerr != nil {
						slog.WarnContext(ctx, "autoscan: release claim failed", "key", k, "err", rerr)
					}
				}
				continue
			}
		}
		if aerr := s.store.AdvanceLastPoll(ctx, src.IntegrationID, cycleStart); aerr != nil {
			slog.WarnContext(ctx, "autoscan: advance last_poll failed", "integration_id", src.IntegrationID, "err", aerr)
		}
	}
	return nil
}

// PollIntervalMinutes reads the configured interval, defaulting to 10 on error.
func (s *Service) PollIntervalMinutes(ctx context.Context) int {
	settings, err := s.store.GetSettings(ctx)
	if err != nil || settings.PollIntervalMinutes <= 0 {
		return 10
	}
	return settings.PollIntervalMinutes
}

// SetRewriteResolvers wires the deps used by SuggestRewrites (optional; only the
// admin-facing service needs them).
func (s *Service) SetRewriteResolvers(rootFolders RootFolderClient, folders FolderLister) {
	s.rootFolders = rootFolders
	s.folders = folders
}

// SuggestRewrites matches an instance's arr root folders to Silo media folders.
func (s *Service) SuggestRewrites(ctx context.Context, integrationID string) (RewriteSuggestions, error) {
	if s.rootFolders == nil || s.folders == nil {
		return RewriteSuggestions{}, fmt.Errorf("autoscan: rewrite suggestion not configured")
	}
	src, err := s.store.GetSource(ctx, integrationID)
	if err != nil {
		return RewriteSuggestions{}, err
	}
	apiKey := src.APIKeyRef
	if s.secrets != nil && apiKey != "" {
		if resolved, rerr := s.secrets.Get(ctx, apiKey); rerr == nil && resolved != "" {
			apiKey = resolved
		}
	}
	arrRoots, err := s.rootFolders.RootFolders(ctx, src.BaseURL, apiKey)
	if err != nil {
		return RewriteSuggestions{}, fmt.Errorf("autoscan: list arr root folders: %w", err)
	}
	siloPaths, err := s.folders.ListFolderPaths(ctx)
	if err != nil {
		return RewriteSuggestions{}, fmt.Errorf("autoscan: list silo folders: %w", err)
	}
	return suggestRewrites(arrRoots, siloPaths, src.PathRewrites), nil
}
