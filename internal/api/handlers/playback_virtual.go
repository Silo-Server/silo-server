package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const virtualPlaybackPrefix = "virtual://"

const maxVirtualPlaybackStreams = 50

type VirtualPlaybackResolver interface {
	ResolveVirtualPlayback(ctx context.Context, virtualPath string, userID int, profileID string) (string, error)
}

// VirtualPlaybackStream is the provider-neutral candidate shape used by the
// just-in-time picker. Implementations must never expose provider URLs here.
type VirtualPlaybackStream struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	URI        string `json:"uri"`
	Resolution string `json:"resolution,omitempty"`
	CodecVideo string `json:"codec_video,omitempty"`
	CodecAudio string `json:"codec_audio,omitempty"`
	HDR        string `json:"hdr,omitempty"`
	SourceType string `json:"source_type,omitempty"`
	FileSize   int64  `json:"file_size,omitempty"`
	Container  string `json:"container,omitempty"`
}

type VirtualPlaybackStreamLister interface {
	ListVirtualPlaybackStreams(ctx context.Context, virtualPath string) ([]VirtualPlaybackStream, error)
}

type VirtualPlaybackStreamListerFunc func(context.Context, string) ([]VirtualPlaybackStream, error)

func (f VirtualPlaybackStreamListerFunc) ListVirtualPlaybackStreams(ctx context.Context, path string) ([]VirtualPlaybackStream, error) {
	return f(ctx, path)
}

// VirtualPlaybackStreamSink persists JIT candidates as selectable virtual
// files.
type VirtualPlaybackStreamSink func(context.Context, *models.MediaFile, []VirtualPlaybackStream) error

func isVirtualPlaybackFile(file *models.MediaFile) bool {
	return file != nil && strings.HasPrefix(file.FilePath, virtualPlaybackPrefix)
}

func isHLSVirtualStreamURL(streamURL string) bool {
	u, err := url.Parse(streamURL)
	if err != nil {
		return strings.Contains(strings.ToLower(streamURL), ".m3u8")
	}
	return strings.Contains(strings.ToLower(u.Path), ".m3u8") || strings.Contains(strings.ToLower(u.RawQuery), ".m3u8")
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

func (h *PlaybackHandler) loadVirtualPlaybackCandidates(ctx context.Context, file *models.MediaFile) {
	if h.VirtualPlaybackStreamLister == nil || h.VirtualPlaybackStreamSink == nil || file == nil {
		return
	}
	// A result URI is already an explicit selection and its sibling results were
	// populated when the parent profile/base URI was played. Profile URIs still
	// need listing: the resolver has already fetched the provider response, and
	// the plugin returns that cached response without another provider request.
	if parsed, err := url.Parse(file.FilePath); err == nil {
		query := parsed.Query()
		if strings.TrimSpace(query.Get("result")) != "" {
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	// ResolveVirtualPlayback has already performed the provider request. Keep
	// this bounded and synchronous so the response contains the extra versions
	// immediately; the plugin-side cache makes this listing a host-local call.
	listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	streams, err := h.VirtualPlaybackStreamLister.ListVirtualPlaybackStreams(listCtx, file.FilePath)
	if err != nil || len(streams) == 0 {
		return
	}
	if len(streams) > maxVirtualPlaybackStreams {
		streams = streams[:maxVirtualPlaybackStreams]
	}
	_ = h.VirtualPlaybackStreamSink(listCtx, file, streams)
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
	h.loadVirtualPlaybackCandidates(r.Context(), file)
	planHash := sha256.Sum256([]byte(req.PlaybackAttemptID + "\x00" + file.FilePath))
	planID := "virtual:" + hex.EncodeToString(planHash[:8])

	engine := playback.EngineMedia3DirectV3
	streamProtocol := playback.StreamHTTPProgressiveV3
	if isHLSVirtualStreamURL(streamURL) {
		engine = playback.EngineMedia3HLSV3
		streamProtocol = playback.StreamHLSV3
	}

	plan := playback.PlanV3{
		ProtocolVersion:      playback.ProtocolV3,
		PlanID:               planID,
		SessionID:            session.ID,
		ExpiresAt:            playback.NewPlanExpiryV3(time.Now()),
		Delivery:             playback.DeliveryOriginalHTTPV3,
		Engine:               engine,
		DecisionReason:       "virtual_playback_resolver",
		RequestedMediaFileID: file.ID,
		EffectiveMediaFileID: file.ID,
		Source:               playback.SourceDescriptorV3{MediaFileID: file.ID},
		Stream:               playback.StreamV3{Protocol: streamProtocol, URL: streamURL, Headers: map[string]string{}, HeaderRefresh: playback.HeaderRefreshSessionV3},
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
