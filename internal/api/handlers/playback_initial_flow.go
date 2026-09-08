package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type InitialPlaybackControlV3 interface {
	playback.GrantPlanStoreV3
	playback.InitialActivationStoreV3
	playback.BoundPlaybackControlStoreV3
	playback.ExecutorRecipePlanStoreV3
	CancelInitialActivation(context.Context, playback.InitialActivationBindingV3, string) (playback.InitialActivationV3, error)
	RenewAttemptLease(context.Context, playback.AttemptAuthorityV3, time.Duration) (playback.AttemptLeaseV3, error)
}

type InitialPlaybackRecipesV3 interface {
	PutImmutable(context.Context, playback.RecipeCard) (playback.ExecutorRecipeLocatorV3, error)
}

// InitialPlaybackFlowV3 uses source authority established by first admission.
type InitialPlaybackFlowV3 struct {
	AuxiliaryEnabled   bool
	ResolveAuxiliary   playback.AuxiliaryRecipeResolverV3
	AcquireAuxiliary   playback.ExecutorAuxiliaryGrantProviderV3
	TimelineResolver   playback.ClientPlaybackTimelineResolverV3
	InstallationID     string
	Control            InitialPlaybackControlV3
	Sources            userstore.PlaybackSourceProvider
	Recipes            InitialPlaybackRecipesV3
	OwnerID            string
	Context            context.Context
	Clock              playback.RuntimeGrantClockV3
	Policy             playback.RuntimeGrantPolicyV3
	OpenOutputTransfer playback.ExecutorOutputTransferProviderV3
	AcquireGrant       func(context.Context, string, playback.ExecutorNamespaceV3, playback.AttemptGrantPurposeV3) (*playback.RuntimeGrantV3, error)
	ResolveRecipe      func(context.Context, string, playback.ExecutorNamespaceV3) (*playback.RecipeCard, error)
	pending            sync.Map
	owners             sync.Map
	shutdownMu         sync.Mutex
	shuttingDown       bool
	work               sync.WaitGroup
	shutdownDone       chan struct{}
}

func (h *PlaybackHandler) ConfigureInitialPlaybackV3(flow *InitialPlaybackFlowV3) error {
	if flow == nil || flow.Control == nil || flow.Sources == nil || flow.Recipes == nil || flow.Context == nil || flow.Clock == nil || flow.AcquireGrant == nil || flow.ResolveRecipe == nil {
		return errors.New("initial playback dependencies required")
	}
	if flow.AuxiliaryEnabled && (flow.ResolveAuxiliary == nil || flow.AcquireAuxiliary == nil) {
		return errors.New("initial auxiliary callbacks required")
	}
	id, err := uuid.Parse(flow.OwnerID)
	if err != nil || id == uuid.Nil || id.String() != flow.OwnerID {
		return errors.New("initial playback boot owner required")
	}
	if err := flow.Policy.Validate(); err != nil {
		return err
	}
	if _, ok := h.sessionMgr.(initialSessionManagerV3); !ok {
		return errors.New("staged session manager required")
	}
	flow.startShutdownJoin()
	h.initialFlow = flow
	h.PlanStoreV3 = flow.Control
	h.tm.ExecuteGrants = flow.AcquireGrant
	h.tm.OpenOutputTransfer = flow.OpenOutputTransfer
	h.tm.ResolveExecutorRecipe = flow.ResolveRecipe
	return nil
}

type initialSessionManagerV3 interface {
	StageInitialSession(context.Context, playback.InitialActivationBindingV3, int, int, playback.PlayMethod, bool) (*playback.Session, error)
	PublishInitialSession(context.Context, playback.InitialActivationBindingV3, playback.Session) (*playback.Session, error)
	DiscardInitialSession(context.Context, playback.InitialActivationBindingV3) error
}

func (h *PlaybackHandler) startInitialPlaybackV3(r *http.Request, userID int, profileID string, req playback.StartRequestV3, digests playbackStartRequestDigestsV3, requested, effective *models.MediaFile, audioIndex int, result playback.PlannerResultV3, clientInfo playback.ClientInfo) (playback.DecisionResponseV3, *transportErrorV3) {
	operationStage := "shutdown"
	fail := func(err error) (playback.DecisionResponseV3, *transportErrorV3) {
		attrs := []any{"component", "playback", "request_id", chimw.GetReqID(r.Context()), "playback_attempt_id", req.PlaybackAttemptID, "stage", operationStage, "error_class", fmt.Sprintf("%T", err)}
		if pgerr, ok := errors.AsType[*pgconn.PgError](err); ok {
			attrs = append(attrs, "sqlstate", pgerr.Code)
		}
		slog.ErrorContext(r.Context(), "playback authority operation failed", attrs...)
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: policyErrorUnavailable, message: "Playback authority is temporarily unavailable.", retryable: true, cause: err}
	}
	flow := h.initialFlow
	if !flow.beginWork() {
		return fail(errors.New("initial playback is shutting down"))
	}
	defer flow.work.Done()
	// Shutdown also cancels pre-owner source lookup/reservation work. Cleanup
	// already uses bounded contexts independent of the canceled request.
	requestCtx, cancelRequest := context.WithCancel(r.Context())
	stopAppCancellation := context.AfterFunc(flow.Context, cancelRequest)
	defer stopAppCancellation()
	defer cancelRequest()
	r = r.WithContext(requestCtx)
	operationStage = "transport_selection"
	isTranscode := result.Plan != nil && ((result.PlayMethod == playback.PlayTranscode && result.Plan.Delivery == playback.DeliveryTranscodeHLSV3) || (result.PlayMethod == playback.PlayRemux && result.Plan.Delivery == playback.DeliveryRemuxHLSV3))
	if result.Plan == nil || (!isTranscode && (result.PlayMethod != playback.PlayDirect || result.Plan.Delivery != playback.DeliveryOriginalHTTPV3)) {
		return fail(errors.New("initial flow requires supported direct or HLS"))
	}
	operationStage = "executor_signing"
	if h.JWTSecret == "" {
		return fail(errors.New("signed executor reference is required"))
	}
	operationStage = "source_lookup"
	source, err := flow.Control.GetAdmittedPlaybackSource(r.Context(), userID)
	if errors.Is(err, playback.ErrInitialActivationUnavailableV3) {
		source, err = flow.Control.EnsureAdmittedPlaybackSource(r.Context(), userID)
	}
	if err != nil {
		return fail(err)
	}
	operationStage = "reserve_attempt"
	reservation, err := flow.Control.ReserveAttempt(r.Context(), playback.AttemptReservationRequestV3{ExpectedAdmissionID: source.AdmissionID, PlaybackAttemptID: req.PlaybackAttemptID, UserID: userID, ProfileID: profileID, RequestedMediaFileID: requested.ID, RequestDigest: digests.current, NormalizedRequest: req, OwnerID: flow.OwnerID, LeaseDuration: flow.Policy.MaxDuration, Retention: playback.MaxTokenTTL})
	if err != nil {
		return fail(err)
	}
	operationStage = "activation"
	if !reservation.Owned {
		if reservation.Record != nil {
			if response, err := h.recoverInitialPublicationV3(r.Context(), reservation.Record); err == nil {
				return response, nil
			}
		}
		return fail(errors.New("initial playback is already reserved"))
	}
	var clientTimeline playback.ClientPlaybackTimelineV3
	if req.ProgressPersistence == playback.ProgressPersistenceClientBoundV3 {
		if !h.SupportsBoundClientTimeline() {
			return fail(playback.ErrClientPlaybackTimelineV3)
		}
		manifest, manifestErr := flow.TimelineResolver.ResolveClientPlaybackManifest(r.Context(), userID, profileID, requested.ID)
		if manifestErr != nil || manifest.Validate() != nil {
			return fail(playback.ErrClientPlaybackTimelineV3)
		}
		if manifest.TimelineID != req.TimelineID {
			// This exact reservation has not installed a sink or staged a route.
			// Retain the non-executable decision before reporting safe rejection.
			response := playback.NewTerminalResponseV3("client_timeline_changed", "Playback timeline changed; discover the current manifest for a new playback request.", false)
			response.ServerFeatures = initialServerFeaturesV3()
			record := playback.AttemptRecordV3{PlaybackAttemptID: req.PlaybackAttemptID, UserID: userID, ProfileID: profileID, RequestedMediaFileID: requested.ID, EffectiveMediaFileID: effective.ID, NormalizedRequest: req, RequestDigest: digests.current, StartResponse: response}
			if err := flow.Control.PublishAttempt(r.Context(), reservation.Authority, record); err != nil {
				return fail(err)
			}
			return response, nil
		}
		var timelineErr error
		clientTimeline, timelineErr = manifest.SelectFile(requested.ID)
		if timelineErr != nil || effective.ID != requested.ID || req.StartPosition == nil {
			return fail(playback.ErrClientPlaybackTimelineV3)
		}
		if _, timelineErr = clientTimeline.GlobalPosition(req.TimelineID, *req.StartPosition); timelineErr != nil {
			return fail(timelineErr)
		}
	}
	target := playbackProgressTarget(requested)
	if target == "" || (clientTimeline != (playback.ClientPlaybackTimelineV3{}) && clientTimeline.MediaItemID != target) {
		return fail(errors.New("playback progress target missing"))
	}
	identity := userstore.WatchIdentity{}
	if h.StableIdentityResolver != nil {
		identity = h.StableIdentityResolver.ResolveHistoryIdentity(r.Context(), target)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		return fail(err)
	}
	binding := playback.InitialActivationBindingV3{Source: source.Source, AdmissionID: source.AdmissionID, IntentID: uuid.NewString(), Scope: userstore.PlaybackProgressScope{ProfileID: profileID, SessionID: uuid.NewString(), MediaItemID: target}, Fence: userstore.PlaybackProgressFence{AttemptID: req.PlaybackAttemptID, Incarnation: reservation.Authority.Incarnation, OwnerID: reservation.Authority.OwnerID, Epoch: reservation.Authority.Epoch}, Progress: userstore.PlaybackProgressSample{DurationSeconds: float64(requested.Duration), PersistenceDisabled: req.ProgressPersistence == playback.ProgressPersistenceClientV3 || !sessionOwnsResumeTimelineV3(effective), Thresholds: h.playbackThresholds(r.Context()), Hints: userstore.VersionHints{FileID: requested.ID, Resolution: requested.Resolution, HDR: requested.HDR, CodecVideo: requested.CodecVideo, EditionKey: requested.EditionKey}}, HistoryIdentityJSON: string(identityJSON)}
	if clientTimeline != (playback.ClientPlaybackTimelineV3{}) {
		binding.ClientTimeline = clientTimeline
		binding.Scope.MediaItemID = clientTimeline.MediaItemID
		binding.Progress.DurationSeconds = clientTimeline.DurationSeconds
		binding.Progress.PersistenceDisabled = false
	}
	owner, err := playback.AcquireRuntimeOwnerLeaseV3(flow.Context, flow.Control.RenewAttemptLease, flow.Clock, flow.Policy, reservation.Authority)
	if err != nil {
		return fail(err)
	}
	startupCtx, cancelStartup := context.WithCancel(r.Context())
	defer cancelStartup()
	stopOwnerCancellation := context.AfterFunc(owner.Context(), cancelStartup)
	defer stopOwnerCancellation()
	r = r.WithContext(startupCtx)
	retained := false
	defer func() {
		if !retained {
			owner.Close()
			<-owner.Done()
		}
	}()
	manager, ok := h.sessionMgr.(initialSessionManagerV3)
	if !ok {
		return fail(errors.New("staged session manager unavailable"))
	}
	stage, err := manager.StageInitialSession(playback.WithClientInfo(r.Context(), clientInfo), binding, effective.ID, requested.ID, result.PlayMethod, result.TranscodeAudio)
	if err != nil {
		return fail(err)
	}
	defer func() {
		if !retained {
			if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
				releaser.ReleaseSession(stage.ID)
			}
		}
	}()

	published := false
	retainStage := false
	defer func() {
		if !published && !retainStage {
			_ = manager.DiscardInitialSession(context.WithoutCancel(r.Context()), binding)
		}
	}()
	if _, err = flow.Control.BeginInitialActivation(r.Context(), binding); err != nil {
		if errors.Is(err, playback.ErrClientPlaybackTimelineBusyV3) {
			return playback.DecisionResponseV3{}, &transportErrorV3{reason: "timeline_part_active", message: "The previous timeline part must finish stopping before another part can start.", cause: err}
		}
		return fail(err)
	}
	abortID := uuid.NewString()
	abort := func(abortStage string, cause error) (playback.DecisionResponseV3, *transportErrorV3) {
		logInitialAbortV3(r.Context(), abortStage, req.PlaybackAttemptID, stage.ID, cause)
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		state, cancelErr := flow.Control.CancelInitialActivation(cleanup, binding, abortID)
		if cancelErr == nil && (state.Phase == playback.InitialActivationAbortingV3 || state.Phase == playback.InitialActivationAbortedV3) {
			// Drain is judged by PostgreSQL, never by the API host's wall clock.
			// The bounded cleanup owns this cancellation even after HTTP disconnect.
			cancelErr = h.finishInitialAbortV3(cleanup, binding, abortID)
			if cancelErr == nil {
				state, cancelErr = flow.Control.ReadInitialActivation(cleanup, binding)
				if cancelErr == nil {
					response, responseErr := ordinaryInitialAbortResponseV3(state)
					if responseErr == nil {
						return response, nil
					}
					cancelErr = responseErr
				}
			}
		}
		logInitialAbortV3(cleanup, "abort_completion", req.PlaybackAttemptID, stage.ID, cancelErr)
		return fail(cause)
	}
	sink, err := flow.Sources.OpenPlaybackSink(r.Context(), binding.Source)
	if err != nil {
		return abort("source_open", err)
	}
	defer sink.Close() //nolint:errcheck
	if err = owner.Check(); err != nil {
		return abort("owner_before_install", err)
	}
	if _, err = sink.InstallPlaybackAuthority(r.Context(), userstore.InstallPlaybackAuthorityRequest{Scope: binding.Scope, Next: binding.Fence}); err != nil {
		// A failed reply may follow commit. Read the exact source before deciding.
		if _, readErr := playback.ReadInitialActivationReceiptV3(r.Context(), binding, sink); readErr != nil {
			return abort("source_install", err)
		}
	}
	observed, err := playback.ReadInitialActivationReceiptV3(r.Context(), binding, sink)
	if err != nil {
		return abort("source_receipt", err)
	}
	if _, err = flow.Control.AcknowledgeInitialInstallation(r.Context(), binding, observed); err != nil {
		return abort("installation_ack", err)
	}
	mode := headerAuthenticatedMediaV3(req.ClientFeatures)
	stage.RequireMediaAuthorization = mode.headerAuth
	stage.Position = floatOrZeroHandlerV3(req.StartPosition)
	stage.AudioTrackIndex = audioIndex
	stage.DisableProgressPersistence = binding.Progress.PersistenceDisabled
	executor := playback.ExecutorNamespaceV3{Incarnation: binding.Fence.Incarnation, Epoch: binding.Fence.Epoch, ExecutorID: uuid.NewString()}
	stage.Executor = &executor
	stage.TranscodeTransportID = uuid.NewString()
	result.Plan.SessionID = stage.ID
	var card playback.RecipeCard
	var transcodeOpts playback.TranscodeOpts
	var proxyNode *nodepool.Node
	if isTranscode {
		decision := h.resolveHLSRouteWithPolicyV3(r.Context(), stage, result, h.playbackRoutingPolicyForContextV3(r.Context()), !mode.headerAuth || mode.proxyEgress, nil, nil)
		if err = applyInitialRoutingV3(stage, decision); err != nil {
			return abort("hls_route", err)
		}
		proxyNode = decision.Plan.ProxyNode
		if stage.TranscodeNodeURL != "" && stage.RoutingEgressNodeID == 0 && flow.OpenOutputTransfer == nil {
			return abort("output_transfer", errors.New("API worker output transfer unavailable"))
		}
		card, transcodeOpts, err = h.prepareInitialTranscodeV3(r.Context(), stage, effective, result)
		if err != nil {
			return abort("transcode_prepare", err)
		}
		stage.TranscodeHWAccel = transcodeOpts.HWAccel
		stage.ToneMapMode = transcodeOpts.ToneMapMode
		stage.TargetResolution = transcodeOpts.TargetResolution
		stage.TargetVideoCodec = transcodeOpts.TargetCodecVideo
		stage.TargetAudioCodec = transcodeOpts.TargetCodecAudio
		stage.SourceAudioChannels = transcodeOpts.SourceAudioChannels
		stage.TargetAudioChannels = transcodeOpts.TargetAudioChannels
		stage.TargetAudioBitrateKbps = transcodeOpts.TargetAudioBitrateKbps
		stage.TargetBitrateKbps = transcodeOpts.TargetBitrateKbps
		stage.StreamBitrateKbps = transcodeOpts.TargetBitrateKbps
		stage.SubtitleTrackIndex = transcodeOpts.SubtitleTrackIndex
		stage.SubtitleBurnIn = transcodeOpts.SubtitleBurnIn
		stage.SegmentDuration = transcodeOpts.SegmentDuration
	} else {
		decision, routeErr := h.resolveIdentityRouteV3(r, stage.ID, result, mode, h.playbackRoutingPolicyForContextV3(r.Context()), nil)
		if routeErr != nil {
			return abort("identity_route", routeErr)
		}
		if err = applyInitialRoutingV3(stage, decision); err != nil {
			return abort("identity_route_apply", err)
		}
		proxyNode = decision.Plan.ProxyNode
		card = playback.NewDirectRecipeCard(stage.ID, userID, profileID, effective.ID)
		card.InputPath = effective.FilePath
		card.OriginalStartedAt = stage.StartedAt
		card.RoutingEgressNodeID = stage.RoutingEgressNodeID
		card.Executor = &executor
		card.TranscodeTransportID = stage.TranscodeTransportID
		card.RoutingWorkload = stage.RoutingWorkload
		card.RoutingExecution = stage.RoutingExecution
		card.RoutingEgress = stage.RoutingEgress
	}
	// Proxy auxiliary producers are a separate prerequisite. Do not publish
	// inventory URLs whose selected egress cannot yet honor their authority.
	if proxyNode != nil && !flow.AuxiliaryEnabled {
		for _, subtitle := range result.Plan.Subtitle.Inventory {
			if subtitle.URL != "" || subtitle.FontBundleURL != "" {
				return abort("subtitle_delivery", errors.New("initial proxy subtitle delivery unavailable"))
			}
		}
		if result.Plan.Subtitle.Artifact != nil {
			return abort("subtitle_artifact", errors.New("initial proxy subtitle artifact unavailable"))
		}
	}

	token, err := streamtoken.Sign(card.ToClaims(), h.JWTSecret, playback.MaxTokenTTL)
	if err != nil {
		return abort("stream_token", err)
	}
	result.Plan.SessionID = stage.ID
	result.Plan.Stream.URL = "/api/v1/stream/" + stage.ID + "?st=" + url.QueryEscape(token)
	if isTranscode {
		result.Plan.Stream.URL = "/api/v1/playback/transcode/" + stage.ID + "/master.m3u8?st=" + url.QueryEscape(token)
	}
	if proxyNode != nil {
		path := "/stream/direct/" + token
		if isTranscode {
			path = "/stream/transcode/" + token + "/master.m3u8"
		}
		result.Plan.Stream.URL = nodepool.NodeEndpoint(proxyNode.ClientURL(), path)
	}

	recipe, err := h.freezeExecutableRecipeV3(r.Context(), effective, result)
	if err != nil {
		return abort("recipe_freeze", err)
	}
	// A rendered or converted sidecar is published only after the recipe is
	// frozen: its URL is served by the bound subtitle producer, which admits a
	// request through the same signed executor reference as the media bytes.
	if err = h.attachSubtitleArtifactV3(r.Context(), stage.ID, effective, result.Plan, result.SubtitleTrackIndex, &recipe); err != nil {
		return abort("subtitle_attach", err)
	}
	bindInitialSubtitleURLsV3(result.Plan, token)
	if proxyNode != nil && flow.AuxiliaryEnabled {
		if err := bindInitialProxyAuxiliaryURLsV3(result.Plan, profileID); err != nil {
			return abort("auxiliary_projection", err)
		}
	}
	if mode.headerAuth {
		if err := projectInitialHeaderMediaV3(result.Plan, stage, mode); err != nil {
			return abort("header_projection", err)
		}
	}
	response := playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: initialServerFeaturesV3(), Outcome: playback.OutcomePlayableV3, SessionID: stage.ID, PlaybackPlan: result.Plan}
	if clientTimeline != (playback.ClientPlaybackTimelineV3{}) {
		response.ProgressTimeline = &clientTimeline
		response.ServerFeatures = append(response.ServerFeatures, playback.FeatureBoundClientTimelineV3)
	}
	record := playback.AttemptRecordV3{PlaybackAttemptID: req.PlaybackAttemptID, SessionID: stage.ID, UserID: userID, ProfileID: profileID, RequestedMediaFileID: requested.ID, EffectiveMediaFileID: effective.ID, CurrentPlanID: result.Plan.PlanID, CurrentPlan: *result.Plan, FrozenRecipe: recipe, NormalizedRequest: req, StartResponse: response, RequestDigest: digests.current, ExpiresAt: reservation.Record.ExpiresAt}
	route := playback.AttemptGrantRouteV3{Executor: executor, TransportID: stage.TranscodeTransportID, ExecutionNodeID: stage.RoutingExecutionNodeID, EgressNodeID: stage.RoutingEgressNodeID}
	if err = flow.Control.StageAttemptRoute(r.Context(), owner.Authority(), record, route); err != nil {
		return abort("route_stage", err)
	}
	locator, err := flow.Recipes.PutImmutable(r.Context(), card)
	if err != nil {
		return abort("recipe_store", err)
	}
	if err = flow.Control.PublishAttemptRecipeLocator(r.Context(), owner.Authority(), nil, locator); err != nil {
		return abort("recipe_publish", err)
	}
	if err = owner.Check(); err != nil {
		return abort("owner_before_start", err)
	}
	var runtime *playback.TranscodeSession
	if isTranscode && stage.TranscodeNodeURL != "" {
		request, requestErr := transcodenode.BoundTranscodeStartRequest(card)
		if requestErr != nil {
			return abort("worker_request", requestErr)
		}
		_, status, startErr := h.startRemotePlaybackTransport(r.Context(), stage.TranscodeNodeURL, request)
		if startErr != nil || status != http.StatusAccepted {
			return abort("worker_start", errors.Join(startErr, errors.New("selected worker did not confirm initial readiness")))
		}
		if err = owner.Check(); err != nil {
			return abort("owner_after_worker_start", err)
		}
	} else if isTranscode {
		transcodeOpts.ExecuteGrants = flow.AcquireGrant
		var startup *localTransportStartupFailureV3
		runtime, startup = h.startReadyLocalPlaybackTransportV3(r.Context(), transcodeOpts)
		if startup != nil {
			return abort("local_start", startup.cause)
		}
		if !h.tm.SwapTranscodeSessionIf(stage.ID, nil, runtime) {
			_ = runtime.Close()
			return abort("local_register", errors.New("initial executor already registered"))
		}
		defer func() {
			if !retained {
				h.tm.CloseTranscodeSessionIf(stage.ID, runtime, "")
			}
		}()
		if err = owner.Check(); err != nil {
			return abort("owner_after_local_start", err)
		}
	}
	retainStage = true
	if _, err = flow.Control.PublishInitialActivation(r.Context(), binding, record); err != nil {
		state, readErr := flow.Control.ReadInitialActivation(r.Context(), binding)
		if readErr != nil {
			retained = true
			h.retainInitialOwnerV3(binding, stage, owner, record)
			return fail(err)
		}
		if state.Phase != playback.InitialActivationActivatedV3 {
			retainStage = false
			return abort("activation_publish", err)
		}
	}
	// Durable publication precedes local visibility. Never roll back its sink.
	retained = true
	h.retainInitialOwnerV3(binding, stage, owner, record)
	if _, err = manager.PublishInitialSession(r.Context(), binding, *stage); err != nil {
		return fail(err)
	}
	published = true
	return response, nil
}

// finishInitialAbortV3 waits only for the captured drain, within its caller's
// cleanup budget. Other failures stay durable for the normal reconciler.
func (h *PlaybackHandler) finishInitialAbortV3(ctx context.Context, binding playback.InitialActivationBindingV3, abortID string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := h.reconcileInitialAbortV3(ctx, binding, abortID)
		if !errors.Is(err, playback.ErrPlaybackRecoveryDrainingV3) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (h *PlaybackHandler) reconcileInitialAbortV3(ctx context.Context, binding playback.InitialActivationBindingV3, abortID string) error {
	flow := h.initialFlow
	sink, err := flow.Sources.OpenPlaybackSink(ctx, binding.Source)
	if err != nil {
		return err
	}
	defer sink.Close() //nolint:errcheck
	_, installErr := sink.InstallPlaybackAuthority(ctx, userstore.InstallPlaybackAuthorityRequest{Scope: binding.Scope, Next: binding.Fence})
	observed, err := playback.ReadInitialActivationReceiptV3(ctx, binding, sink)
	if err != nil {
		return errors.Join(installErr, err)
	}
	state, err := observed.StateFor(binding)
	if err != nil {
		return err
	}
	if state.Stop == nil {
		_, stopErr := sink.StopPlaybackProgress(ctx, userstore.StopPlaybackProgressRequest{Scope: binding.Scope, Fence: binding.Fence, StopID: abortID})
		observed, err = playback.ReadInitialActivationReceiptV3(ctx, binding, sink)
		if err != nil {
			return errors.Join(stopErr, err)
		}
	}

	_, err = flow.Control.CompleteInitialAbort(ctx, binding, abortID, observed)
	return err
}

type PlaybackProgressCommand struct {
	TimelineID string  `json:"timeline_id,omitempty"`
	Sequence   int64   `json:"sequence"`
	Position   float64 `json:"position"`
	IsPaused   bool    `json:"is_paused"`
}

type PlaybackStopCommand struct {
	TimelineID string   `json:"timeline_id,omitempty"`
	StopID     string   `json:"stop_id"`
	Sequence   int64    `json:"sequence"`
	Position   *float64 `json:"position,omitempty"`
	IsPaused   bool     `json:"is_paused"`
}

func (h *PlaybackHandler) handleInitialProgressV3(w http.ResponseWriter, r *http.Request) {
	var req initialProgressRequestV3
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPlaybackV3BodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "A positive progress sequence is required")
		return
	}
	result, err := h.applyInitialProgress(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), chi.URLParam(r, "session_id"), req)
	if err != nil {
		writePlaybackOperationError(w, err)
		return
	}
	status := http.StatusOK
	if result.Draining {
		status = http.StatusAccepted
	}
	writeJSON(w, status, result)
}

func (h *PlaybackHandler) applyInitialProgress(ctx context.Context, userID int, profileID, sessionID string, req initialProgressRequestV3) (PlaybackMutationView, error) {
	flow := h.initialFlow
	active, err := flow.Control.GetActivatedPlaybackAuthority(ctx, userID, profileID, sessionID)
	if err != nil || active.Activation.Phase != playback.InitialActivationActivatedV3 {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}

	if req.Sequence <= 0 || req.Position < 0 {
		return PlaybackMutationView{}, playbackOperationError(http.StatusBadRequest, "bad_request", "A positive progress sequence is required")
	}
	ownerValue, ok := flow.owners.Load(active.Binding.Scope.SessionID)
	if !ok {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	owner, ok := ownerValue.(*playback.RuntimeOwnerLeaseV3)
	if !ok {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	pendingValue, ok := flow.pending.Load(active.Binding.Scope.SessionID)
	if !ok {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	pending, ok := pendingValue.(*initialPendingPublicationV3)
	if !ok {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	// Keep the local projection ordered with the sink result for this owner.
	// The sink remains authoritative across retries and other server processes.
	pending.mu.Lock()
	defer pending.mu.Unlock()
	if pending.binding != active.Binding || pending.owner != owner {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	if owner.Authority().OwnerID != active.Binding.Fence.OwnerID || owner.Check() != nil {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	release := context.AfterFunc(owner.Context(), cancel)
	defer release()
	sink, err := flow.Sources.OpenPlaybackSink(ctx, active.Binding.Source)
	if err != nil {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	defer sink.Close() //nolint:errcheck
	sample, err := initialTimelineSample(active.Binding, req.TimelineID, req.Sequence, req.Position, req.IsPaused)
	if err != nil {
		return PlaybackMutationView{}, err
	}
	result, err := sink.ApplyPlaybackProgress(ctx, userstore.ApplyPlaybackProgressRequest{Scope: active.Binding.Scope, Fence: active.Binding.Fence, Sample: sample})
	if err != nil {
		if errors.Is(err, userstore.ErrPlaybackSinkConflict) {
			return PlaybackMutationView{}, playbackOperationError(http.StatusConflict, "progress_conflict", "The sequence already has different progress")
		} else {
			return PlaybackMutationView{}, playbackAuthorityOperationError()
		}
	}
	if owner.Check() != nil {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	response, err := initialTimelineMutationResponse(active.Binding, result, false)
	if err != nil {
		return PlaybackMutationView{}, err
	}
	if response.Accepted != nil {
		_ = h.sessionMgr.UpdateProgress(active.Binding.Scope.SessionID, response.Accepted.Position, response.Accepted.IsPaused)
	}
	return response, nil
}

func (h *PlaybackHandler) handleInitialStopV3(w http.ResponseWriter, r *http.Request) {
	var req initialStopRequestV3
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPlaybackV3BodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "A stable stop ID is required")
		return
	}
	result, err := h.stopInitialPlayback(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), chi.URLParam(r, "session_id"), req)
	if err != nil {
		writePlaybackOperationError(w, err)
		return
	}
	status := http.StatusOK
	if result.Draining {
		status = http.StatusAccepted
	}
	writeJSON(w, status, result)
}

func (h *PlaybackHandler) stopInitialPlayback(ctx context.Context, userID int, profileID, sessionID string, req initialStopRequestV3) (PlaybackMutationView, error) {
	flow := h.initialFlow
	active, err := flow.Control.GetActivatedPlaybackAuthority(ctx, userID, profileID, sessionID)
	if err != nil {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}

	id, err := uuid.Parse(req.StopID)
	if err != nil || id == uuid.Nil || id.String() != req.StopID || req.Sequence < 0 || req.Position != nil && *req.Position < 0 || (req.Position == nil) != (req.Sequence == 0) {
		return PlaybackMutationView{}, playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid stop identity or final sample")
	}
	if req.TimelineID != active.Binding.ClientTimeline.TimelineID {
		return PlaybackMutationView{}, playbackOperationError(http.StatusConflict, "timeline_changed", "Playback timeline does not match the captured session")
	}
	var final *userstore.PlaybackProgressSample
	if req.Position != nil {
		sample, sampleErr := initialTimelineSample(active.Binding, req.TimelineID, req.Sequence, *req.Position, req.IsPaused)
		if sampleErr != nil {
			return PlaybackMutationView{}, sampleErr
		}
		final = &sample
	}
	state, err := flow.Control.BeginBoundStop(ctx, active.Binding, req.StopID)
	if err != nil {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	h.closeInitialRuntimeV3(active.Binding)
	sink, err := flow.Sources.OpenPlaybackSink(ctx, active.Binding.Source)
	if err != nil {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	defer sink.Close() //nolint:errcheck
	var identity userstore.WatchIdentity
	if active.Binding.HistoryIdentityJSON != "" {
		if err = json.Unmarshal([]byte(active.Binding.HistoryIdentityJSON), &identity); err != nil {
			return PlaybackMutationView{}, playbackAuthorityOperationError()
		}
	}
	result, err := sink.StopPlaybackProgress(ctx, userstore.StopPlaybackProgressRequest{Scope: active.Binding.Scope, Fence: active.Binding.Fence, StopID: req.StopID, FinalSample: final, Identity: identity})
	if err != nil {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	observed, err := playback.ReadInitialActivationReceiptV3(ctx, active.Binding, sink)
	if err != nil {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	if _, err = flow.Control.CompleteBoundStop(ctx, active.Binding, req.StopID, observed); err != nil {
		current, readErr := flow.Control.ReadInitialActivation(ctx, active.Binding)
		if readErr != nil || current.Phase != playback.InitialActivationStoppedV3 {
			if readErr == nil && current.Phase == playback.InitialActivationStoppingV3 && current.StopID == state.StopID {
				return initialTimelineMutationResponse(active.Binding, result, true)
			}
			return PlaybackMutationView{}, playbackAuthorityOperationError()
		}
	}
	// Manager-only removal follows durable terminal state; no legacy writer runs.
	if err = h.sessionMgr.StopSession(active.Binding.Scope.SessionID); err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
		return PlaybackMutationView{}, playbackAuthorityOperationError()
	}
	if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
		releaser.ReleaseSession(active.Binding.Scope.SessionID)
	}
	return initialTimelineMutationResponse(active.Binding, result, false)
}

type PlaybackAcceptedProgress struct {
	ItemPosition *float64 `json:"item_position,omitempty"`
	TimelineID   string   `json:"timeline_id,omitempty"`
	Sequence     int64    `json:"sequence"`
	Position     float64  `json:"position"`
	IsPaused     bool     `json:"is_paused"`
}

type PlaybackMutationView struct {
	Draining  bool                      `json:"-"`
	Outcome   string                    `json:"outcome"`
	Accepted  *PlaybackAcceptedProgress `json:"accepted,omitempty"`
	StopID    string                    `json:"stop_id,omitempty"`
	HistoryID string                    `json:"history_id,omitempty"`
}

func initialMutationResponse(result userstore.PlaybackProgressResult, draining bool) initialMutationResponseV3 {
	response := initialMutationResponseV3{Outcome: result.Outcome}
	if result.State.Last != nil {
		sample := result.State.Last.Sample
		response.Accepted = &PlaybackAcceptedProgress{Sequence: sample.Sequence, Position: sample.PositionSeconds, IsPaused: sample.Paused}
	}
	if result.State.Stop != nil {
		response.StopID = result.State.Stop.StopID
		if result.State.Stop.History != nil {
			response.HistoryID = result.State.Stop.History.ID
		}
	}
	if draining {
		response.Outcome = "draining"
		response.Draining = true
	}
	return response
}

type initialProgressRequestV3 = PlaybackProgressCommand
type initialStopRequestV3 = PlaybackStopCommand
type initialMutationResponseV3 = PlaybackMutationView

func (h *PlaybackHandler) closeInitialRuntimeV3(binding playback.InitialActivationBindingV3) {
	flow := h.initialFlow
	if value, ok := flow.pending.Load(binding.Scope.SessionID); ok {
		if pending, ok := value.(*initialPendingPublicationV3); ok && pending.binding == binding {
			pending.mu.Lock()
			flow.pending.CompareAndDelete(binding.Scope.SessionID, pending)
			pending.mu.Unlock()
		}
	}
	if value, ok := flow.owners.Load(binding.Scope.SessionID); ok {
		if owner, ok := value.(*playback.RuntimeOwnerLeaseV3); ok {
			authority := owner.Authority()
			if authority.PlaybackAttemptID == binding.Fence.AttemptID && authority.Incarnation == binding.Fence.Incarnation && authority.OwnerID == binding.Fence.OwnerID && authority.Epoch == binding.Fence.Epoch && flow.owners.CompareAndDelete(binding.Scope.SessionID, owner) {
				owner.Close()
			}
		}
	}
	if runtime := h.tm.GetTranscodeSession(binding.Scope.SessionID); runtime != nil {
		opts := runtime.Opts()
		if opts.Executor != nil && opts.Executor.Incarnation == binding.Fence.Incarnation && opts.Executor.Epoch == binding.Fence.Epoch {
			h.tm.CloseTranscodeSessionIf(binding.Scope.SessionID, runtime, "")
		}
	}
}

// bindInitialSubtitleURLsV3 publishes the sidecar and font URLs of a bound plan
// in the form the bound subtitle producer admits: API-local, carrying the same
// signed executor reference as the media bytes. The legacy producer's
// session-relative URLs would resolve to the unbound handlers, which refuse a
// bound session. Burn-in-only tracks keep no URL.
func bindInitialSubtitleURLsV3(plan *playback.PlanV3, token string) {
	if plan == nil || token == "" {
		return
	}
	bind := func(raw string) string {
		if raw == "" {
			return raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.IsAbs() || u.Host != "" {
			return raw
		}
		path := strings.TrimPrefix(u.Path, "/api/v1")
		if !strings.HasPrefix(path, "/stream/") {
			return raw
		}
		u.Path, u.RawPath = "/api/v1"+path, ""
		query := u.Query()
		query.Set(streamTokenParam, token)
		u.RawQuery = query.Encode()
		return u.String()
	}
	for i := range plan.Subtitle.Inventory {
		plan.Subtitle.Inventory[i].URL = bind(plan.Subtitle.Inventory[i].URL)
		plan.Subtitle.Inventory[i].FontBundleURL = bind(plan.Subtitle.Inventory[i].FontBundleURL)
	}
	if plan.Subtitle.Artifact != nil {
		plan.Subtitle.Artifact.URL = bind(plan.Subtitle.Artifact.URL)
	}
}
