package handlers

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/google/uuid"
)

type InitialPlaybackReconciliation struct {
	Visited, Completed, Pending int
	NextAttemptID               string
}

// ReconcileInitialPlayback processes a bounded page of retained intents across accounts.
// It never enrolls sources, adopts active owners, or invents a final stop sample.
func (h *PlaybackHandler) ReconcileInitialPlayback(ctx context.Context, afterAttemptID string, limit int) (InitialPlaybackReconciliation, error) {
	var result InitialPlaybackReconciliation
	if h.initialFlow == nil {
		return result, playback.ErrInitialActivationUnavailableV3
	}
	inventory, ok := h.initialFlow.Control.(playback.InitialReconciliationStoreV3)
	if !ok {
		return result, playback.ErrInitialActivationUnavailableV3
	}
	states, err := inventory.ListInitialReconciliation(ctx, afterAttemptID, limit)
	if err != nil {
		return result, err
	}
	var failures []error
	for _, state := range states {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Visited++
		result.NextAttemptID = state.Binding.Fence.AttemptID
		switch state.Phase {
		case playback.InitialActivationPendingV3, playback.InitialActivationInstalledV3:
			state, err = h.initialFlow.Control.AbortInitialActivation(ctx, state.Binding, uuid.NewString())
			if err == nil {
				if state.AbortReason == playback.InitialAbortOwnerLostV3 {
					state, err = h.reconcileOwnerLossV3(ctx, state.Binding)
				} else {
					err = h.reconcileInitialAbortV3(ctx, state.Binding, state.AbortID)
				}
			}
		case playback.InitialActivationActivatedV3:
			state, err = h.reconcileOwnerLossV3(ctx, state.Binding)
		case playback.InitialActivationAbortingV3:
			if state.AbortReason == playback.InitialAbortOwnerLostV3 {
				state, err = h.reconcileOwnerLossV3(ctx, state.Binding)
			} else {
				err = h.reconcileInitialAbortV3(ctx, state.Binding, state.AbortID)
			}
		case playback.InitialActivationStoppingV3:
			err = h.reconcileInitialStopReceipt(ctx, state)
		default:
			err = playback.ErrInitialActivationConflictV3
		}
		if err != nil {
			result.Pending++
			failures = append(failures, err)
			continue
		}
		if state.AbortReason == playback.InitialAbortOwnerLostV3 && state.Phase != playback.InitialActivationAbortedV3 {
			result.Pending++
			continue
		}
		result.Completed++
	}
	return result, errors.Join(failures...)
}

func (h *PlaybackHandler) reconcileInitialStopReceipt(ctx context.Context, state playback.InitialActivationV3) error {
	sink, err := h.initialFlow.Sources.OpenPlaybackSink(ctx, state.Binding.Source)
	if err != nil {
		return err
	}
	defer sink.Close() //nolint:errcheck
	receipt, err := playback.ReadInitialActivationReceiptV3(ctx, state.Binding, sink)
	if err != nil {
		return err
	}
	// Without a source receipt, only the original persisted client request can
	// finish its exact stop payload. Reconciliation cannot replace that payload.
	if _, err = h.initialFlow.Control.CompleteBoundStop(ctx, state.Binding, state.StopID, receipt); err != nil {
		return err
	}
	h.closeInitialRuntimeV3(state.Binding)
	err = h.sessionMgr.StopSession(state.Binding.Scope.SessionID)
	if err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
		return err
	}
	if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
		releaser.ReleaseSession(state.Binding.Scope.SessionID)
	}
	return nil
}

// RunInitialPlaybackReconciliation closes durable orphaned work without client
// participation. Multiple API replicas may visit a row; the existing per-attempt
// CAS transitions and exact receipts serialize completion without new authority.
func (h *PlaybackHandler) RunInitialPlaybackReconciliation(ctx context.Context, interval time.Duration) {
	if ctx == nil || interval <= 0 {
		return
	}
	cursor := ""
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		pageCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		result, err := h.ReconcileInitialPlayback(pageCtx, cursor, 100)
		cancel()
		if err != nil && ctx.Err() == nil && result.Visited == 0 {
			slog.WarnContext(ctx, "playback reconciliation inventory unavailable", "component", "playback")
		}
		// A deadline can interrupt a full inventory page after only a prefix.
		// Advance past every visited row so a slow source cannot starve later accounts.
		if result.Visited == 0 {
			cursor = ""
		} else {
			cursor = result.NextAttemptID
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
