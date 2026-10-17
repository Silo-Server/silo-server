package handlers

import (
	"context"
	"net/http"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
)

func (h *PlaybackHandler) PlaybackConnection(ctx context.Context, device string, reset bool) (connectionstore.State, error) {
	device = clampHeaderValue(device, 128)
	key, label := connectionstore.Key(ctx, h.JWTSecret)
	state := connectionstore.State{Connection: label}
	if reset {
		return state, h.Connections.Clear(ctx, apimw.GetUserID(ctx), device, key)
	}
	limit, err := h.Connections.Get(ctx, apimw.GetUserID(ctx), device, key)
	state.LearnedLimit = limit
	return state, err
}

func (h *PlaybackHandler) applyLearnedLimit(r *http.Request, req *playback.StartRequestV3) (*playback.BandwidthLimitV3, error) {
	if !isNativeAPIV2(r.Context()) || !playback.HasFeatureV3(req.ClientFeatures, playback.FeatureBufferingQualityV1) {
		return nil, nil
	}
	key, _ := connectionstore.Key(r.Context(), h.JWTSecret)
	user := apimw.GetUserID(r.Context())
	device, _ := r.Context().Value(playbackConnectionDeviceContextKey{}).(string)
	device = clampHeaderValue(device, 128)
	if req.LearnedLimit == playback.LearnedLimitClearV3 {
		return nil, h.Connections.Clear(r.Context(), user, device, key)
	}
	if req.LearnedLimit == playback.LearnedLimitIgnoreV3 {
		return nil, nil
	}
	if req.BandwidthEstimateSource == playback.BandwidthEstimateObservedRebufferV3 && req.BandwidthEstimateKbps != nil && req.QualityPreference == playback.QualityAutoV3 {
		if err := h.Connections.Observe(r.Context(), user, device, key, *req.BandwidthEstimateKbps); err != nil {
			return nil, err
		}
	}
	if req.QualityPreference != playback.QualityAutoV3 {
		return nil, nil
	}
	limit, err := h.Connections.Get(r.Context(), user, device, key)
	if err != nil || limit == nil {
		return nil, err
	}
	if req.BandwidthEstimateKbps != nil && *req.BandwidthEstimateKbps < limit.Kbps {
		// The stricter probe wins, but a learned stall still requires trying
		// fitting originals before encoding. It did not itself lower the plan.
		req.BandwidthEstimateSource = playback.BandwidthEstimateObservedRebufferV3
		return nil, nil
	}
	req.BandwidthEstimateKbps = new(limit.Kbps)
	req.BandwidthEstimateSource = playback.BandwidthEstimateObservedRebufferV3
	return &playback.BandwidthLimitV3{Kbps: limit.Kbps, Source: playback.BandwidthEstimateObservedRebufferV3, ExpiresAt: limit.ExpiresAt.UTC().Format(time.RFC3339)}, nil
}

func markLearnedLimit(result *playback.PlannerResultV3, limit *playback.BandwidthLimitV3, source *models.MediaFile, request playback.StartRequestV3, serverCap int) {
	if limit == nil || result.Plan == nil || source == nil {
		return
	}
	descriptor := playback.SourceDescriptorFromFileV3(source, 0)
	original := descriptor.BitrateKbps
	request.BandwidthEstimateKbps = nil
	if serverCap > 0 && (request.BandwidthCapKbps == nil || serverCap < *request.BandwidthCapKbps) {
		request.BandwidthCapKbps = new(serverCap)
	}
	baseline := playback.ResolveQualityPolicyV3(request, descriptor)
	delivered := result.Plan.Source.BitrateKbps
	if result.Plan.EffectiveRecipe.BitrateKbps != nil {
		delivered = *result.Plan.EffectiveRecipe.BitrateKbps
	}
	if original > limit.Kbps*8/10 && delivered > 0 && delivered < original && delivered < baseline.BitrateKbps {
		result.Plan.BandwidthLimit = limit
	}
}

// A measured stall budget first tries the existing, authorized edition order.
// Candidate planning proves that the client can copy the video, including HDR,
// audio and subtitle constraints; a matching bitrate alone is insufficient.
func (h *PlaybackHandler) lowerBufferedVersion(r *http.Request, req playback.StartRequestV3, base *models.MediaFile, audio int, settings playback.PlannerSettingsV3) (*models.MediaFile, playback.StartRequestV3, int) {
	if req.QualityPreference != playback.QualityAutoV3 || req.BandwidthEstimateSource != playback.BandwidthEstimateObservedRebufferV3 || req.BandwidthEstimateKbps == nil || !req.AllowsAlternateVersions() {
		return base, req, audio
	}
	budget := *req.BandwidthEstimateKbps * 8 / 10
	if req.BandwidthCapKbps != nil {
		budget = min(budget, *req.BandwidthCapKbps)
	}
	if serverCap := serverBitrateCapV3(r.Context()); serverCap > 0 {
		budget = min(budget, serverCap)
	}
	if source := playback.SourceDescriptorFromFileV3(base, audio); source.BitrateKbps <= budget {
		return base, req, audio
	}
	filter := requestAccessFilter(r)
	alternates, err := h.findAlternateFiles(r.Context(), base, filter)
	if err != nil {
		return base, req, audio
	}
	for _, candidate := range alternates {
		if candidate.PresentationPartIndex != base.PresentationPartIndex {
			continue
		}
		candidate = h.ensurePlaybackProbe(r.Context(), candidate)
		bitrate := playback.SourceDescriptorFromFileV3(candidate, 0).BitrateKbps
		if bitrate <= 0 || bitrate > budget || !catalog.FileAllowedByAccess(candidate, filter) {
			continue
		}
		next := req
		dropped, err := h.remapSubtitleSelectionV3(r.Context(), base, candidate, &next)
		if err != nil || dropped {
			continue
		}
		nextAudio := remapAudioIndexV3(base, candidate, audio)
		if err := preflightPlaybackFile(r.Context(), candidate, h.MissingMarker, h.EventsHub); err != nil {
			continue
		}
		result, _ := h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{Request: next, RequestedFile: base, EffectiveFile: candidate, AudioTrackIndex: nextAudio, Settings: settings, ServerBitrateCapKbps: serverBitrateCapV3(r.Context()), Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), candidate), Now: time.Now(), AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), candidate)})
		if result.Plan != nil && result.Plan.Delivery != playback.DeliveryTranscodeHLSV3 {
			return candidate, next, nextAudio
		}
	}
	return base, req, audio
}
