package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const virtualPlaybackPrefix = "aiostreams://"

type VirtualPlaybackResolver interface {
	ResolveVirtualPlayback(ctx context.Context, virtualPath string, userID int, profileID string) (string, error)
}

func isVirtualPlaybackFile(file *models.MediaFile) bool {
	return file != nil && strings.HasPrefix(file.FilePath, virtualPlaybackPrefix)
}

func (h *PlaybackHandler) resolveVirtualPlayback(r *http.Request, file *models.MediaFile, profileID string) (string, error) {
	if !isVirtualPlaybackFile(file) {
		return "", nil
	}
	if h.VirtualPlaybackResolver == nil {
		return "", errors.New("virtual playback resolver is not configured")
	}
	return h.VirtualPlaybackResolver.ResolveVirtualPlayback(r.Context(), file.FilePath, apimw.GetUserID(r.Context()), profileID)
}

func (h *PlaybackHandler) startVirtualPlaybackV3(r *http.Request, req playback.StartRequestV3, requestDigest string, file *models.MediaFile, streamURL string) (playback.DecisionResponseV3, *transportErrorV3) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	ctx := playback.WithClientInfo(r.Context(), playbackClientInfoFromRequest(r))
	var session *playback.Session
	var err error
	if starter, ok := h.sessionMgr.(sessionStarterWithFilesContext); ok {
		session, err = starter.StartSessionWithFilesContext(ctx, userID, profileID, file.ID, file.ID, playback.PlayDirect, false)
	} else {
		session, err = h.sessionMgr.StartSessionWithFiles(userID, profileID, file.ID, file.ID, playback.PlayDirect, false)
	}
	if err != nil {
		return playback.DecisionResponseV3{}, sessionStartErrorV3(err)
	}
	abort := func() { _ = h.stopPlaybackSessionByID(context.WithoutCancel(r.Context()), session.ID, false) }
	position := floatOrZeroHandlerV3(req.StartPosition)
	if err := h.sessionMgr.UpdateProgress(session.ID, position, false); err != nil {
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to initialize virtual playback.", cause: err}
	}
	planHash := sha256.Sum256([]byte(req.PlaybackAttemptID + "\x00" + file.FilePath))
	planID := "virtual:" + hex.EncodeToString(planHash[:8])
	plan := playback.PlanV3{
		ProtocolVersion:      playback.ProtocolV3,
		PlanID:               planID,
		SessionID:            session.ID,
		ExpiresAt:            playback.NewPlanExpiryV3(time.Now()),
		Delivery:             playback.DeliveryOriginalHTTPV3,
		Engine:               playback.EngineMedia3DirectV3,
		DecisionReason:       "virtual_playback_resolver",
		RequestedMediaFileID: file.ID,
		EffectiveMediaFileID: file.ID,
		Source:               playback.SourceDescriptorV3{MediaFileID: file.ID},
		Stream:               playback.StreamV3{Protocol: playback.StreamHTTPProgressiveV3, URL: streamURL, Headers: map[string]string{}, HeaderRefresh: playback.HeaderRefreshSessionV3},
		Timeline:             playback.TimelineV3{SourceStartSeconds: position, PlayerStartSeconds: position, CanSeekAnywhere: true, SeekRestoration: "player_position"},
		Transformations:      []playback.TransformationV3{},
		AppliedQuirks:        []playback.AppliedQuirkV3{},
		RuntimeCorrections:   []string{},
		DegradationWarnings:  []playback.DegradationWarningV3{},
	}
	record := playback.AttemptRecordV3{
		PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID,
		UserID: userID, ProfileID: profileID,
		RequestedMediaFileID: file.ID, EffectiveMediaFileID: file.ID,
		CurrentPlanID: planID, CurrentPlan: plan, NormalizedRequest: req,
		RequestDigest: requestDigest, ExpiresAt: time.Now().Add(playback.MaxTokenTTL),
	}
	if err := h.PlanStoreV3.SaveAttempt(r.Context(), record); err != nil {
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to save virtual playback plan.", cause: err}
	}
	h.syncSessionsNow(r.Context(), "v3_virtual_start")
	return playback.DecisionResponseV3{
		ProtocolVersion: playback.ProtocolV3,
		ServerFeatures:  []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only},
		Outcome:         playback.OutcomePlayableV3,
		SessionID:       session.ID,
		PlaybackPlan:    &plan,
	}, nil
}
