package handlers

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// recoverOrdinaryInitialAbortV3 resolves only the original pre-publication
// cancellation. LookupInitialRecovery has already checked the authenticated
// account/profile and the normalized request including its device identity.
// No live owner, catalog lookup, admission, grant, or source restoration is used.
func (h *PlaybackHandler) recoverOrdinaryInitialAbortV3(ctx context.Context, state playback.InitialActivationV3) (playback.DecisionResponseV3, error) {
	fail := func(err error) (playback.DecisionResponseV3, error) { return playback.DecisionResponseV3{}, err }
	if state.AbortReason != "" || state.AbortID == "" || state.StopID != "" {
		return fail(playback.ErrInitialActivationConflictV3)
	}
	if state.Phase == playback.InitialActivationAbortingV3 {
		// Observe the captured fence; never reinstall authority if it is absent or
		// has changed. Any uncertainty retains the original START for exact retry.
		sink, err := h.initialFlow.Sources.OpenPlaybackSink(ctx, state.Binding.Source)
		if err != nil {
			return fail(err)
		}
		defer sink.Close() //nolint:errcheck
		observed, err := playback.ReadInitialActivationReceiptV3(ctx, state.Binding, sink)
		if err != nil {
			return fail(err)
		}
		receipt, err := observed.StateFor(state.Binding)
		if err != nil {
			return fail(err)
		}
		if receipt.Last != nil {
			return fail(playback.ErrInitialActivationConflictV3)
		}
		var stopErr error
		if receipt.Stop == nil {
			_, stopErr = sink.StopPlaybackProgress(ctx, userstore.StopPlaybackProgressRequest{Scope: state.Binding.Scope, Fence: state.Binding.Fence, StopID: state.AbortID})
			// A lost stop reply can follow commit. Only an exact source read proves it.
			observed, err = playback.ReadInitialActivationReceiptV3(ctx, state.Binding, sink)
			if err != nil {
				return fail(errors.Join(stopErr, err))
			}
		}
		// The durable store enforces the captured drain deadline using its clock.
		// Before drain completion this remains a 503; it does not publish a 202 or terminal.
		state, err = h.initialFlow.Control.CompleteInitialAbort(ctx, state.Binding, state.AbortID, observed)
		if err != nil {
			return fail(errors.Join(stopErr, err))
		}
	}
	return ordinaryInitialAbortResponseV3(state)
}

func ordinaryInitialAbortResponseV3(state playback.InitialActivationV3) (playback.DecisionResponseV3, error) {
	fail := func(err error) (playback.DecisionResponseV3, error) { return playback.DecisionResponseV3{}, err }
	if state.Phase != playback.InitialActivationAbortedV3 || state.Terminal == nil {
		return fail(playback.ErrInitialActivationConflictV3)
	}
	if err := playback.ValidateInitialTerminalV3(state.Binding, *state.Terminal); err != nil {
		return fail(err)
	}
	// The ordinary terminal START wire carries no accepted sample or STOP receipt.
	// Use it only for this server-owned abort with no accepted playback; preserve
	// any richer receipt for a separately coordinated recovery protocol.
	if state.Terminal.Stop.StopID != state.AbortID || state.Terminal.Last != nil || state.Terminal.Stop.Accepted != nil || state.Terminal.Stop.History != nil {
		return fail(playback.ErrInitialActivationConflictV3)
	}
	return playback.NewTerminalResponseV3("playback_start_aborted", "Playback could not start. Start playback again explicitly.", false), nil
}
