package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/google/uuid"
)

func initialReconciliationBinding(t *testing.T, f *initialHTTPFixture) playback.InitialActivationBindingV3 {
	t.Helper()
	ctx := t.Context()
	source := f.source.Source()
	var admissionID string
	if err := f.pool.QueryRow(ctx, `SELECT admission_id::text FROM playback_source_registrations WHERE user_id=$1`, f.userID).Scan(&admissionID); err != nil {
		t.Fatal(err)
	}
	reserved, err := f.flow.Control.ReserveAttempt(ctx, playback.AttemptReservationRequestV3{ExpectedAdmissionID: admissionID, PlaybackAttemptID: f.request.PlaybackAttemptID, UserID: f.userID, ProfileID: f.request.ProfileID, RequestedMediaFileID: f.request.FileID, RequestDigest: "reconciliation-fixture", NormalizedRequest: f.request, OwnerID: f.flow.OwnerID, LeaseDuration: time.Minute, Retention: time.Hour})
	if err != nil || !reserved.Owned {
		t.Fatalf("reserve: %+v %v", reserved, err)
	}
	binding := playback.InitialActivationBindingV3{Source: source, AdmissionID: admissionID, Scope: userstore.PlaybackProgressScope{ProfileID: f.request.ProfileID, SessionID: uuid.NewString(), MediaItemID: f.itemID}, Fence: userstore.PlaybackProgressFence{AttemptID: f.request.PlaybackAttemptID, Incarnation: reserved.Authority.Incarnation, OwnerID: reserved.Authority.OwnerID, Epoch: reserved.Authority.Epoch}, IntentID: uuid.NewString(), Progress: userstore.PlaybackProgressSample{DurationSeconds: 1000}}
	if _, err := f.flow.Control.BeginInitialActivation(ctx, binding); err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestInitialPlaybackReconcileExpiredIntent(t *testing.T) {
	for _, scenario := range []string{"pending", "installed", "different terminal", "source unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			f := newInitialHTTPFixture(t)
			binding := initialReconciliationBinding(t, f)
			ctx := t.Context()
			var original userstore.PlaybackProgressState
			if scenario != "pending" && scenario != "source unavailable" {
				if _, err := f.source.InstallPlaybackAuthority(ctx, userstore.InstallPlaybackAuthorityRequest{Scope: binding.Scope, Next: binding.Fence}); err != nil {
					t.Fatal(err)
				}
				receipt, err := playback.ReadInitialActivationReceiptV3(ctx, binding, f.source)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.flow.Control.AcknowledgeInitialInstallation(ctx, binding, receipt); err != nil {
					t.Fatal(err)
				}
				if scenario == "different terminal" {
					result, err := f.source.StopPlaybackProgress(ctx, userstore.StopPlaybackProgressRequest{Scope: binding.Scope, Fence: binding.Fence, StopID: uuid.NewString()})
					if err != nil {
						t.Fatal(err)
					}
					original = result.State
				}
			}
			if scenario == "source unavailable" {
				if _, err := f.pool.Exec(ctx, `UPDATE playback_source_markers SET gate='sealed' WHERE user_id=$1`, f.userID); err != nil {
					t.Fatal(err)
				}
			}
			// The retained intent remains eligible even after both original time bounds.
			if _, err := f.pool.Exec(ctx, `UPDATE playback_v3_attempts SET control_lease_expires_at=clock_timestamp()-interval '2 minutes',expires_at=clock_timestamp()-interval '1 minute' WHERE playback_attempt_id=$1`, f.request.PlaybackAttemptID); err != nil {
				t.Fatal(err)
			}
			result, err := f.handler.ReconcileInitialPlayback(ctx, "", 10)
			if scenario == "source unavailable" || scenario == "different terminal" {
				if err == nil || result.Pending != 1 || result.Completed != 0 {
					t.Fatalf("unavailable source: %+v %v", result, err)
				}
				state, err := f.flow.Control.ReadInitialActivation(ctx, binding)
				if err != nil || state.Phase != playback.InitialActivationAbortingV3 || state.Terminal != nil {
					t.Fatalf("intent not retained: %+v %v", state, err)
				}
				if scenario == "different terminal" {
					source, readErr := f.source.ReadPlaybackProgress(ctx, binding.Scope)
					if readErr != nil || !reflect.DeepEqual(source, original) {
						t.Fatal("reconciliation changed a conflicting terminal receipt")
					}
				}
				return
			}
			if err != nil || result.Completed != 1 || result.Pending != 0 {
				t.Fatalf("reconcile: %+v %v", result, err)
			}
			state, err := f.flow.Control.ReadInitialActivation(ctx, binding)
			if err != nil || state.Phase != playback.InitialActivationAbortedV3 || state.Terminal == nil {
				t.Fatalf("control terminal: %+v %v", state, err)
			}
			source, err := f.source.ReadPlaybackProgress(ctx, binding.Scope)
			if err != nil || source.Stop == nil || source.Last != nil || source.Stop.History != nil {
				t.Fatalf("source terminal: %+v %v", source, err)
			}
			if scenario == "different terminal" && (!reflect.DeepEqual(source, original) || state.AbortID == source.Stop.StopID) {
				t.Fatal("reconciliation replaced different terminal receipt")
			}
		})
	}
}

func TestInitialPlaybackReconcileNormalStopRequiresReceipt(t *testing.T) {
	for _, hasReceipt := range []bool{false, true} {
		name := "without receipt"
		if hasReceipt {
			name = "committed receipt"
		}
		t.Run(name, func(t *testing.T) {
			f := newInitialHTTPFixture(t)
			ctx := t.Context()
			status, data := f.call(t, http.MethodPost, "/start", f.request)
			if status != 201 {
				t.Fatalf("start: %d %s", status, data)
			}
			var decision playback.DecisionResponseV3
			if err := json.Unmarshal(data, &decision); err != nil {
				t.Fatal(err)
			}
			active, err := f.flow.Control.GetActivatedPlaybackAuthority(ctx, f.userID, f.request.ProfileID, decision.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			stopID := uuid.NewString()
			if _, err := f.flow.Control.BeginBoundStop(ctx, active.Binding, stopID); err != nil {
				t.Fatal(err)
			}
			before, err := f.source.ReadPlaybackProgress(ctx, active.Binding.Scope)
			if err != nil {
				t.Fatal(err)
			}
			if hasReceipt {
				sample := active.Binding.Progress
				sample.Sequence = 7
				sample.PositionSeconds = 200
				result, err := f.source.StopPlaybackProgress(ctx, userstore.StopPlaybackProgressRequest{Scope: active.Binding.Scope, Fence: active.Binding.Fence, StopID: stopID, FinalSample: &sample})
				if err != nil {
					t.Fatal(err)
				}
				before = result.State
			}
			result, err := f.handler.ReconcileInitialPlayback(ctx, "", 10)
			after, readErr := f.source.ReadPlaybackProgress(ctx, active.Binding.Scope)
			if readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("reconciler mutated source: before=%+v after=%+v err=%v", before, after, readErr)
			}
			state, readErr := f.flow.Control.ReadInitialActivation(ctx, active.Binding)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if hasReceipt {
				if err != nil || result.Completed != 1 || state.Phase != playback.InitialActivationStoppedV3 {
					t.Fatalf("receipt completion: %+v %+v %v", result, state, err)
				}
			} else {
				if err == nil || result.Pending != 1 || state.Phase != playback.InitialActivationStoppingV3 || state.Terminal != nil {
					t.Fatalf("missing receipt synthesized: %+v %+v %v", result, state, err)
				}
			}
		})
	}
}

func TestInitialPlaybackReconcileClosesMatchingTranscode(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("local runtime cleanup requires ffmpeg")
	}
	f := newInitialHTTPFixture(t)
	ctx := t.Context()
	command := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "12", "-c:v", "mpeg4", "-c:a", "aac", f.file.FilePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("synthetic media: %v %s", err, output)
	}
	f.file.CodecVideo = "mpeg4"
	f.file.Duration = 12
	f.file.Resolution = "180p"
	f.file.VideoTracks = []models.VideoTrack{{Codec: "mpeg4", Width: 320, Height: 180, FrameRate: "24/1", BitDepth: 8, VideoRange: "SDR"}}
	root := t.TempDir()
	f.handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{TranscodeEnabled: true, HWAccel: "none", FFmpegPath: ffmpeg, TranscodeDir: root}
	}
	f.request.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	status, data := f.call(t, http.MethodPost, "/start", f.request)
	if status != 201 {
		t.Fatalf("start: %d %s", status, data)
	}
	var decision playback.DecisionResponseV3
	if err := json.Unmarshal(data, &decision); err != nil {
		t.Fatal(err)
	}
	runtime := f.handler.TranscodeManager().GetTranscodeSession(decision.SessionID)
	if runtime == nil || runtime.ExecutorNamespace() == nil {
		t.Fatal("missing bound runtime")
	}
	t.Cleanup(func() { _ = runtime.Close() })
	outputDir, err := runtime.ExecutorNamespace().OutputDir(root)
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.flow.Control.GetActivatedPlaybackAuthority(ctx, f.userID, f.request.ProfileID, decision.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	stopID := uuid.NewString()
	if _, err := f.flow.Control.BeginBoundStop(ctx, active.Binding, stopID); err != nil {
		t.Fatal(err)
	}
	sample := active.Binding.Progress
	sample.Sequence = 1
	sample.PositionSeconds = 3
	stopped, err := f.source.StopPlaybackProgress(ctx, userstore.StopPlaybackProgressRequest{Scope: active.Binding.Scope, Fence: active.Binding.Fence, StopID: stopID, FinalSample: &sample})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		result, err := f.handler.ReconcileInitialPlayback(ctx, "", 10)
		if err == nil && result.Completed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("drain did not complete: %+v %v", result, err)
		}
	}
	if f.handler.TranscodeManager().GetTranscodeSession(decision.SessionID) != nil {
		t.Fatal("reconciled runtime remains registered")
	}
	if _, err := os.Stat(outputDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reconciled executor output remains: %v", err)
	}
	after, err := f.source.ReadPlaybackProgress(ctx, active.Binding.Scope)
	if err != nil || !reflect.DeepEqual(after, stopped.State) {
		t.Fatalf("cleanup rewrote terminal receipt: %+v %v", after, err)
	}
}

func TestInitialPlaybackTerminalCleanupPreservesOtherBinding(t *testing.T) {
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	h.initialFlow = &InitialPlaybackFlowV3{}
	binding := playback.InitialActivationBindingV3{Scope: userstore.PlaybackProgressScope{SessionID: "logical"}, Fence: userstore.PlaybackProgressFence{AttemptID: "attempt", Epoch: 1}}
	retained := &initialPendingPublicationV3{binding: binding}
	h.initialFlow.pending.Store(binding.Scope.SessionID, retained)
	other := binding
	other.Fence.Epoch = 2
	h.closeInitialRuntimeV3(other)
	if value, ok := h.initialFlow.pending.Load(binding.Scope.SessionID); !ok || value != retained {
		t.Fatal("cleanup discarded unresolved different binding")
	}
	h.closeInitialRuntimeV3(binding)
	if _, ok := h.initialFlow.pending.Load(binding.Scope.SessionID); ok {
		t.Fatal("terminal binding remains retained")
	}
}

func TestInitialPlaybackCapabilitiesExcludeUnsupportedActions(t *testing.T) {
	features := initialServerFeaturesV3()
	if !slices.Contains(features, "sequenced_progress_v1") || !slices.Contains(features, playback.FeaturePlaybackPlanV3) {
		t.Fatal("required initial capabilities missing")
	}
	for _, unsupported := range []string{"seek_reanchor_v1", "output_change_v1", "playback_route_diagnostics"} {
		if slices.Contains(features, unsupported) {
			t.Fatalf("unsupported action advertised: %s", unsupported)
		}
	}
}

func TestInitialPlaybackCapabilitiesFollowTranscodeConfiguration(t *testing.T) {
	f := newInitialHTTPFixture(t)
	f.flow.InstallationID = uuid.NewString()
	ctx := apimw.SetProfileID(apimw.SetClaims(t.Context(), &auth.Claims{UserID: f.userID}), f.request.ProfileID)
	f.handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{TranscodeEnabled: false} }
	direct, err := f.handler.PlaybackCapabilities(ctx, f.userID, f.request.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if !direct.Allowed || !slices.Equal(direct.Deliveries, []playback.DeliveryV3{playback.DeliveryOriginalHTTPV3}) {
		t.Fatalf("direct capability: %+v", direct)
	}
	f.handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{TranscodeEnabled: true} }
	encoded, err := f.handler.PlaybackCapabilities(ctx, f.userID, f.request.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(encoded.Deliveries, playback.DeliveryTranscodeHLSV3) || encoded.Revision == direct.Revision {
		t.Fatal("transcode policy change must change deliveries and capability revision")
	}
}

func TestInitialPlaybackAutomaticallyAdmitsOrdinaryAccount(t *testing.T) {
	for _, discover := range []bool{true, false} {
		t.Run(fmt.Sprint(discover), func(t *testing.T) {
			f := newInitialHTTPFixture(t)
			f.flow.InstallationID = "ab860a0a-7da8-408d-a8de-5eb0fcd482d2"
			// Remove only this fixture's unused setup binding through the recovery service.
			var intent pgstore.FirstAdmissionIntent
			intent.AccountID = f.userID
			intent.InstallationID = f.flow.InstallationID
			intent.Backend = "postgres"
			if _, err := f.pool.Exec(t.Context(), `INSERT INTO server_settings(key,value) VALUES('diagnostics.server_instance_id',$1),('userdb.backend','postgres') ON CONFLICT(key) DO UPDATE SET value=excluded.value`, intent.InstallationID); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(t.Context(), `SELECT u.username,r.source_id::text,r.admission_id::text FROM users u JOIN playback_source_registrations r ON r.user_id=u.id WHERE u.id=$1`, f.userID).Scan(&intent.ExpectedUsername, &intent.SourceID, &intent.IntentID); err != nil {
				t.Fatal(err)
			}
			if _, err := pgstore.NewPostgresProvider(f.pool).DiscardUnreceiptedAdmission(t.Context(), intent, true); err != nil {
				t.Fatal(err)
			}
			if discover {
				ctx := apimw.SetProfileID(apimw.SetClaims(t.Context(), &auth.Claims{UserID: f.userID}), f.request.ProfileID)
				first, err := f.handler.PlaybackCapabilities(ctx, f.userID, f.request.ProfileID)
				if err != nil || !first.Allowed || len(first.Features) == 0 || len(first.Deliveries) == 0 {
					t.Fatalf("capabilities: %+v %v", first, err)
				}
				retry, err := f.handler.PlaybackCapabilities(ctx, f.userID, f.request.ProfileID)
				if err != nil || !reflect.DeepEqual(first, retry) {
					t.Fatalf("capabilities changed on retry: %+v %v", retry, err)
				}
			}
			// Starting directly must also establish the source without prior discovery.
			status, data := f.call(t, http.MethodPost, "/start", f.request)
			if status != http.StatusCreated {
				t.Fatalf("ordinary start %d: %s", status, data)
			}
			var receipts int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM playback_first_admissions WHERE user_id=$1`, f.userID).Scan(&receipts); err != nil || receipts != 1 {
				t.Fatalf("first admission receipt: %d %v", receipts, err)
			}
		})
	}
}

// No account list and no browser request is needed after an API dies mid-start.
func TestInitialPlaybackBackgroundReconcilesAccountsAfterRestart(t *testing.T) {
	first, second := newInitialHTTPFixture(t), newInitialHTTPFixture(t)
	bindings := []playback.InitialActivationBindingV3{
		initialReconciliationBinding(t, first), initialReconciliationBinding(t, second),
	}
	for _, b := range bindings {
		if _, err := first.pool.Exec(t.Context(), `UPDATE playback_v3_attempts SET control_lease_expires_at=clock_timestamp()-interval '1 minute' WHERE playback_attempt_id=$1`, b.Fence.AttemptID); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); first.handler.RunInitialPlaybackReconciliation(ctx, 10*time.Millisecond) }()
	defer func() { cancel(); <-done }()
	deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var completed int
		if err := first.pool.QueryRow(t.Context(), `SELECT count(*) FROM playback_v3_attempts WHERE playback_attempt_id=ANY($1) AND control_state='stopped' AND control_activation->>'phase'='aborted' AND control_activation->'terminal' IS NOT NULL`, []string{bindings[0].Fence.AttemptID, bindings[1].Fence.AttemptID}).Scan(&completed); err != nil {
			t.Fatal(err)
		}
		if completed == 2 {
			return
		}
		select {
		case <-deadline.Done():
			t.Fatal("background recovery did not finish both accounts")
		case <-tick.C:
		}
	}
}

type deadlineReconciliationControl struct {
	InitialPlaybackControlV3
	cursors chan string
}

func (s deadlineReconciliationControl) ListInitialReconciliation(_ context.Context, after string, _ int) ([]playback.InitialActivationV3, error) {
	s.cursors <- after
	if after != "" {
		return nil, nil
	}
	return []playback.InitialActivationV3{
		{Phase: playback.InitialActivationPendingV3, Binding: playback.InitialActivationBindingV3{Fence: userstore.PlaybackProgressFence{AttemptID: "first"}}},
		{Phase: playback.InitialActivationPendingV3, Binding: playback.InitialActivationBindingV3{Fence: userstore.PlaybackProgressFence{AttemptID: "second"}}},
	}, nil
}

func (s deadlineReconciliationControl) AbortInitialActivation(ctx context.Context, _ playback.InitialActivationBindingV3, _ string) (playback.InitialActivationV3, error) {
	<-ctx.Done()
	return playback.InitialActivationV3{}, ctx.Err()
}

func TestInitialPlaybackReconciliationAdvancesAfterPageDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	control := deadlineReconciliationControl{cursors: make(chan string, 2)}
	h := &PlaybackHandler{initialFlow: &InitialPlaybackFlowV3{Control: control}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.RunInitialPlaybackReconciliation(ctx, time.Millisecond)
	}()
	for _, want := range []string{"", "first"} {
		select {
		case got := <-control.cursors:
			if got != want {
				t.Fatalf("cursor = %q, want %q; timed-out source starved later attempts", got, want)
			}
		case <-time.After(12 * time.Second):
			t.Fatal("reconciliation did not advance after its page deadline")
		}
	}
	cancel()
	<-done
}

func TestInitialPlaybackExpiredUndisplayedStartReleasesOwner(t *testing.T) {
	f := newInitialHTTPFixture(t)
	f.flow.InstallationID = uuid.NewString()
	f.flow.Policy = playback.RuntimeGrantPolicyV3{MaxDuration: 3 * time.Second, SafetyMargin: 100 * time.Millisecond, RenewBefore: time.Second, PollInterval: 10 * time.Millisecond}
	status, data := f.call(t, http.MethodPost, "/start", f.request)
	if status != http.StatusCreated {
		t.Fatalf("start: %d %s", status, data)
	}
	var decision playback.DecisionResponseV3
	if err := json.Unmarshal(data, &decision); err != nil {
		t.Fatal(err)
	}
	value, ok := f.flow.owners.Load(decision.SessionID)
	if !ok {
		t.Fatal("missing owner")
	}
	owner := value.(*playback.RuntimeOwnerLeaseV3)
	// A successful response can be lost before the browser ever opens media or
	// sends progress. Exercise the real idle cleanup hook without a fixed sleep.
	if expired := f.manager.CleanInactive(time.Nanosecond, time.Nanosecond); len(expired) != 1 {
		t.Fatalf("expired sessions = %d, want 1", len(expired))
	}
	select {
	case <-owner.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("expired session kept renewing its owner and blocked START recovery")
	}
	if _, ok := f.flow.pending.Load(decision.SessionID); ok {
		t.Fatal("expired publication remains boot-local")
	}
	ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: f.userID, Role: "user", TokenType: auth.TokenTypeAccess})
	ctx = apimw.SetProfileID(ctx, f.request.ProfileID)
	caller := PlaybackCaller{UserID: f.userID, ProfileID: f.request.ProfileID, InstallationID: f.flow.InstallationID}
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		// Replay itself can finish owner-loss once the retained lease expires.
		status, recovered, handled, err := f.handler.RecoverInitialPlaybackStart(ctx, caller, f.request)
		if err == nil && handled && status == http.StatusCreated {
			terminal, ok := recovered.(PlaybackOwnerLossStart)
			if !ok || terminal.Terminal == nil || terminal.Terminal.Reason != "playback_owner_lost" || terminal.Recovery.PlaybackAttemptID != f.request.PlaybackAttemptID || terminal.Recovery.SessionID != decision.SessionID || terminal.Recovery.State != "aborted" || terminal.Recovery.Accepted != nil {
				t.Fatalf("recovery replaced the original attempt or invented progress: %+v", recovered)
			}
			if _, err := f.manager.GetSession(decision.SessionID); !errors.Is(err, playback.ErrSessionNotFound) {
				t.Fatalf("recovery reconstructed expired session: %v", err)
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("expired START did not reach terminal: status=%d handled=%v err=%v body=%+v", status, handled, err, recovered)
		case <-tick.C:
		}
	}
}
