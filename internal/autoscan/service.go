package autoscan

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

const scanTrigger = "autoscan"

type Store interface {
	GetSettings(ctx context.Context) (Settings, error)
	ListEnabledSources(ctx context.Context) ([]Source, error)
	AdvanceLastPoll(ctx context.Context, integrationID string, at time.Time) error
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
	store    Store
	history  HistoryClient
	resolver Resolver
	queue    Queuer
	suppress Suppressor
	secrets  SecretResolver
	now      func() time.Time
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
			rewritten = append(rewritten, applyRewrites(p, src.PathRewrites))
		}
		var targets []scantrigger.Target
		var claimed []int
		for _, dir := range uniqueParentDirs(rewritten) {
			target, rerr := s.resolver.Resolve(ctx, scantrigger.Request{Path: dir, Trigger: scanTrigger})
			if rerr != nil {
				slog.WarnContext(ctx, "autoscan: resolve failed", "path", dir, "err", rerr)
				continue
			}
			if target == nil || target.Folder == nil {
				continue
			}
			ok, serr := s.suppress.ShouldScan(ctx, target.Folder.ID, ttl)
			if serr != nil || !ok {
				continue
			}
			target.Trigger = scanTrigger
			targets = append(targets, *target)
			claimed = append(claimed, target.Folder.ID)
		}
		if len(targets) > 0 {
			if eerr := s.queue.EnqueueScans(ctx, targets); eerr != nil {
				slog.WarnContext(ctx, "autoscan: enqueue failed", "integration_id", src.IntegrationID, "err", eerr)
				for _, folderID := range claimed {
					if rerr := s.suppress.Release(ctx, folderID); rerr != nil {
						slog.WarnContext(ctx, "autoscan: release claim failed", "folder_id", folderID, "err", rerr)
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
