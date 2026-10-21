package handlers

import (
	"context"
	"errors"
	"log/slog"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// A header-authenticated attempt publishes no stream token, so after a restart
// (or on another API replica) nothing the client sends describes the session.
// The API keeps the committed recipe card in SessionRecipeStore instead and
// rebuilds the playback session from it for the session's owner, which is what
// the token does for every other attempt. The durable v3 attempt row stays the
// authority on whether the card may still be used: it must name the same plan,
// belong to the caller and selected profile, and not be stopped.

// sessionRecipeStoreV3 is the session-recipe key space (*noderecipe.Store): a
// recipe store whose reads also report when Redis could not be read, so an
// outage is not answered as a missing session.
type sessionRecipeStoreV3 interface {
	recipeCardStoreV3
	Lookup(ctx context.Context, key string) (*playback.RecipeCard, bool, error)
}

// recordSessionRecipeV3 stores the recovery card of a header-authenticated
// transport that has just committed under planID. The builders attach the base
// card; the routing comes from the live session the commit produced, read under
// its lifecycle lock, so a stop or an executor move that lands between the
// commit and this write is not overwritten with the transport's earlier view.
//
// A proxy-served transport gets no card, and loses any earlier one: the proxy
// serves it from its grant, and a playback session the API rebuilt for it
// would be reaped on API-side activity alone while the proxy is still serving.
//
// Best effort, like the node recipe: a failed write only costs this session its
// restart recovery, and the plan binding keeps the card it failed to replace
// from rebuilding the superseded transport.
func (h *PlaybackHandler) recordSessionRecipeV3(ctx context.Context, planID string, transport *preparedTransportV3) {
	if h == nil || transport == nil || transport.sessionRecipe == nil || planID == "" ||
		h.SessionRecipeStore == nil || !h.SessionRecipeStore.Enabled() {
		return
	}
	card := *transport.sessionRecipe
	unlock := h.tm.LockSessionLifecycle(card.SessionID)
	defer unlock()
	current, err := h.sessionMgr.GetSession(card.SessionID)
	if err != nil || current.TranscodeTransportID != transport.transportID {
		return
	}
	if current.RoutingEgress == string(noderouting.EgressProxy) {
		h.deleteSessionRecipeV3(ctx, card.SessionID)
		return
	}
	card.PlanID = planID
	card.RequireMediaAuthorization = true
	card.RequestedMediaFileID = current.RequestedMediaFileID
	card.DisableProgressPersistence = current.DisableProgressPersistence
	stampLiveRouteV3(&card, current)
	h.putSessionRecipeV3(ctx, card)
}

// stampLiveRouteV3 copies the executor and routing a live session runs on onto
// its recovery card, which a rebuilt session relays and enforces from.
func stampLiveRouteV3(card *playback.RecipeCard, current *playback.Session) {
	card.TranscodeNodeURL = current.TranscodeNodeURL
	card.TranscodeTransportID = current.TranscodeTransportID
	card.RoutingWorkload = current.RoutingWorkload
	card.RoutingExecution = current.RoutingExecution
	card.RoutingExecutionNodeID = current.RoutingExecutionNodeID
	card.RoutingEgress = current.RoutingEgress
	card.RoutingEgressNodeID = current.RoutingEgressNodeID
}

// sessionRecipeCardV3 is a builder's base card for the recovery copy, with the
// client metadata stored cards carry so a rebuilt session keeps its label in
// the admin views.
func sessionRecipeCardV3(card playback.RecipeCard, session *playback.Session) *playback.RecipeCard {
	card.ClientName = session.ClientName
	card.ClientVersion = session.ClientVersion
	card.ClientBuild = session.ClientBuild
	card.ClientChannel = session.ClientChannel
	card.ClientUserAgent = session.ClientUserAgent
	return &card
}

// clientRecipeCard is the card a serve loader returned when the client carried
// it in a stream token (claims are only decoded from one), or nil for the API's
// own recovery card. A transcode move treats a client card as authoritative and
// leaves the node store alone; a tokenless session must keep that store current.
func clientRecipeCard(card *playback.RecipeCard, claims *streamtoken.Claims) *playback.RecipeCard {
	if claims == nil {
		return nil
	}
	return card
}

func (h *PlaybackHandler) putSessionRecipeV3(ctx context.Context, card playback.RecipeCard) {
	putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeRecipeWriteTimeoutV3)
	defer cancel()
	if err := h.SessionRecipeStore.Put(putCtx, card.SessionID, card); err != nil {
		slog.WarnContext(ctx, "persist session recipe failed; this session cannot survive an API restart",
			"component", "api", "playback_session_id", card.SessionID, "error", err)
	}
}

// reusedSessionRecipeV3 is the card a sidecar-only replan carries forward. The
// transport and its bytes are unchanged, but the plan id is new, so the stored
// card has to be recorded again under it or recovery would refuse it.
func (h *PlaybackHandler) reusedSessionRecipeV3(ctx context.Context, sessionID, currentPlanID string) *playback.RecipeCard {
	if h == nil || h.SessionRecipeStore == nil || !h.SessionRecipeStore.Enabled() {
		return nil
	}
	card, ok := h.SessionRecipeStore.Get(ctx, sessionID)
	if !ok || card == nil || card.SessionID != sessionID || card.PlanID != currentPlanID {
		return nil
	}
	return card
}

// followSessionRecipeMoveV3 points the stored recovery card at the executor a
// transcode moved to. A rebuilt session relays to the node named here, so a
// card left on the dead node would send the next restart back to it. moved is
// the card the move started the transport from; the plan binding, client label
// and progress policy stay those of the stored card, and the route is the live
// session's.
func (h *PlaybackHandler) followSessionRecipeMoveV3(ctx context.Context, sessionID string, moved playback.RecipeCard) {
	if h == nil || h.SessionRecipeStore == nil || !h.SessionRecipeStore.Enabled() {
		return
	}
	unlock := h.tm.LockSessionLifecycle(sessionID)
	defer unlock()
	current, err := h.sessionMgr.GetSession(sessionID)
	if err != nil || remoteTransportID(current) != moved.TranscodeTransportID {
		return
	}
	stored, ok := h.SessionRecipeStore.Get(ctx, sessionID)
	if !ok || stored == nil || stored.TranscodeTransportID != moved.TranscodeTransportID {
		return
	}
	next := moved
	next.SessionID = sessionID
	next.PlanID = stored.PlanID
	next.RequireMediaAuthorization = true
	next.RequestedMediaFileID = stored.RequestedMediaFileID
	next.DisableProgressPersistence = stored.DisableProgressPersistence
	next.ClientName = stored.ClientName
	next.ClientVersion = stored.ClientVersion
	next.ClientBuild = stored.ClientBuild
	next.ClientChannel = stored.ClientChannel
	next.ClientUserAgent = stored.ClientUserAgent
	stampLiveRouteV3(&next, current)
	h.putSessionRecipeV3(ctx, next)
}

// deleteSessionRecipeV3 drops a session's recovery card where the session
// ends: on a user's stop and on an abort. A replica that ends an attempt it does
// not hold, or a teardown that ends the attempt without either, leaves the card
// to its TTL; the stopped attempt refuses it everywhere regardless.
func (h *PlaybackHandler) deleteSessionRecipeV3(ctx context.Context, sessionID string) {
	if h != nil {
		deleteSessionRecipeV3(ctx, h.SessionRecipeStore, sessionID)
	}
}

func deleteSessionRecipeV3(ctx context.Context, recipes recipeCardStoreV3, sessionID string) {
	if recipes == nil || sessionID == "" {
		return
	}
	if err := recipes.Delete(context.WithoutCancel(ctx), sessionID); err != nil {
		slog.WarnContext(ctx, "failed to drop the session recipe",
			"component", "api", "playback_session_id", sessionID, "error", err)
	}
}

// storedSessionRecipeV3 returns the recovery card for a header-authenticated
// session this process has no live copy of, or nil. It answers only an
// authenticated caller, and only while the durable attempt still names the
// card's plan, belongs to that caller and selected profile, negotiated
// header-authenticated media, has not expired, and has not been stopped.
// Everything it checks happens before a session is registered or an executor
// started. An error means Redis or the attempt row could not be read: the
// caller answers a retryable failure rather than telling the client its session
// is gone.
func storedSessionRecipeV3(ctx context.Context, recipes sessionRecipeStoreV3, attempts playback.PlanStoreV3, sessionID string, userID int) (*playback.RecipeCard, error) {
	if userID == 0 || sessionID == "" || recipes == nil || !recipes.Enabled() || attempts == nil {
		return nil, nil
	}
	card, ok, err := recipes.Lookup(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !ok || card == nil || card.SessionID != sessionID || card.UserID != userID ||
		!card.RequireMediaAuthorization || card.PlanID == "" {
		return nil, nil
	}
	record, err := attempts.GetAttempt(ctx, sessionID)
	if err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			return nil, nil
		}
		slog.WarnContext(ctx, "could not read the playback attempt to recover its session",
			"component", "api", "playback_session_id", sessionID, "error", err)
		return nil, err
	}
	if record == nil || record.StoppedAt != nil || record.CurrentPlanID != card.PlanID ||
		record.UserID != userID || record.ProfileID != card.ProfileID ||
		(!record.ExpiresAt.IsZero() && time.Now().After(record.ExpiresAt)) ||
		!playback.HasFeatureV3(record.NormalizedRequest.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3) {
		return nil, nil
	}
	if profileID := apimw.GetProfileID(ctx); profileID != "" && profileID != record.ProfileID {
		return nil, nil
	}
	return card, nil
}

func (h *PlaybackHandler) storedSessionRecipeV3(ctx context.Context, sessionID string, userID int) (*playback.RecipeCard, error) {
	return storedSessionRecipeV3(ctx, h.SessionRecipeStore, h.PlanStoreV3, sessionID, userID)
}

func (h *StreamHandler) storedSessionRecipeV3(ctx context.Context, sessionID string, userID int) (*playback.RecipeCard, error) {
	return storedSessionRecipeV3(ctx, h.SessionRecipeStore, h.PlanStoreV3, sessionID, userID)
}
