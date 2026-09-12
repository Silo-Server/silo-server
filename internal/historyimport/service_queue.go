package historyimport

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/workmetrics"
)

func (s *Service) wakeImportQueue() {
	select {
	case s.queueWake <- struct{}{}:
	default:
	}
}

func (s *Service) startImportQueue() {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if err := s.repo.reconcileUndispatchedRuns(s.bgContext); err != nil {
				slog.WarnContext(s.bgContext, "history import: queued reconciliation failed", "error", err)
			}
			s.dispatchQueuedRuns()
			select {
			case <-s.bgContext.Done():
				return
			case <-s.queueWake:
			case <-ticker.C:
			}
		}
	}()
}

// Capacity is held before touching a queued row, so another node can claim it
// while this node is busy. Queued work has no process-local goroutine/provider.
func (s *Service) dispatchQueuedRuns() {
	for {
		if s.bgContext.Err() != nil {
			return
		}
		select {
		case s.runSemaphore <- struct{}{}:
		default:
			return
		}
		run, claim, err := s.repo.claimQueuedRun(s.bgContext)
		if err != nil || run == nil {
			<-s.runSemaphore
			if err != nil {
				slog.WarnContext(s.bgContext, "history import: queue claim failed", "error", err)
			}
			return
		}
		if run.Status != RunStatusRunning {
			// The claimer can quarantine structurally invalid credentials without
			// starting work; continue so one damaged row cannot starve the queue.
			<-s.runSemaphore
			s.notifyRun(run)
			continue
		}
		go func() {
			defer func() { <-s.runSemaphore; s.wakeImportQueue() }()
			provider, err := s.providerForClaim(s.bgContext, run, claim)
			if err != nil {
				s.failClaim(s.bgContext, claim, ExecutionSummary{}, err)
				return
			}
			s.executeRunWithClaim(run, provider, claim, true)
		}()
	}
}

func (s *Service) startClaimHeartbeat(ctx context.Context, claim RunClaim, cancel context.CancelCauseFunc) context.CancelFunc {
	hbCtx, stop := context.WithCancel(ctx)
	go heartbeatLoop(hbCtx, historyImportHeartbeatInterval, func(ctx context.Context) error {
		err := s.repo.touchClaimHeartbeat(ctx, claim)
		if err != nil {
			cancel(err)
		}
		return err
	})
	return stop
}

func (s *Service) failClaim(ctx context.Context, claim RunClaim, summary ExecutionSummary, cause error) {
	if cancelledCause := context.Cause(ctx); cancelledCause != nil {
		cause = cancelledCause
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	// Cancellation is durable: worker acknowledgement wins over a simultaneous
	// provider error, and no summary/terminal write may erase the request.
	if err := s.repo.acknowledgeRunCancellation(finishCtx, claim); err == nil {
		workmetrics.FinishContext(ctx, "canceled")
		s.notifyRunByID(finishCtx, claim.RunID)
		return
	}
	if errors.Is(cause, ErrRunClaimLost) {
		return
	}
	message := userFacingRunError(summary, cause)
	if errors.Is(cause, ErrRunConfigurationChanged) {
		message = ErrRunConfigurationChanged.Error()
	}
	if err := s.repo.failRun(finishCtx, claim, summary, message); err != nil {
		if !errors.Is(err, ErrRunNotFound) {
			slog.WarnContext(finishCtx, "history import: terminal write failed", "run_id", claim.RunID, "error", err)
		}
		return
	}
	workmetrics.FinishContext(ctx, "error")
	s.notifyRunByID(finishCtx, claim.RunID)
}

func (s *Service) ListAdminRunsPage(ctx context.Context, sourceID *int, after *RunKey, limit int) ([]Run, bool, error) {
	return s.repo.ListAdminRunsPage(ctx, sourceID, after, limit)
}

// A run-scoped personal credential is loaded only under its exact claim. The
// execution loop validates again before Fetch and each user-state effect.
func (s *Service) providerForClaim(ctx context.Context, run *Run, claim RunClaim) (Provider, error) {
	if err := s.repo.validateRunClaim(ctx, claim); err != nil {
		return nil, err
	}
	if claim.DispatchKind == dispatchKindPersonal {
		credential, err := s.repo.readPersonalRunCredentials(ctx, claim)
		if err != nil {
			return nil, err
		}
		switch run.SourceType {
		case SourceTypeEmby:
			return NewEmbyProvider(s.emby, embyLocalAuth{BaseURL: credential.BaseURL, UserID: credential.ExternalUserID, AccessToken: credential.ServerToken}), nil
		case SourceTypeJellyfin:
			return NewJellyfinProvider(s.jellyfin, jellyfinLocalAuth{BaseURL: credential.BaseURL, UserID: credential.ExternalUserID, AccessToken: credential.ServerToken}), nil
		case SourceTypePlex:
			return NewPlexServerProvider(s.plex, credential.BaseURL, credential.ServerToken).WithAccountToken(credential.AccountToken), nil
		default:
			return nil, ErrPersonalCredentialsUnavailable
		}
	}
	source, token, err := s.repo.GetSourceWithAdminToken(ctx, claim.SourceID)
	if err != nil {
		return nil, err
	}
	if source.Revision != claim.SourceRevision {
		return nil, ErrRunConfigurationChanged
	}
	return s.buildAdminProvider(source, token, claim.ExternalUserID)
}
