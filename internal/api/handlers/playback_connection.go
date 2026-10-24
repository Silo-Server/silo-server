package handlers

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"time"
	"unicode/utf8"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
)

// connectionDeviceV3 is the X-Silo-Device-Id a connection observation is kept
// under. A value that is not valid UTF-8 cannot be stored, so it is no device
// identity: the request neither records nor applies a limit.
func connectionDeviceV3(header string) string {
	device := clampHeaderValue(header, 128)
	if !utf8.ValidString(device) {
		return ""
	}
	return device
}

func (h *PlaybackHandler) PlaybackConnection(ctx context.Context, device string, reset bool) (connectionstore.State, error) {
	device = connectionDeviceV3(device)
	key, label := connectionstore.Key(ctx, h.JWTSecret)
	state := connectionstore.State{Connection: label}
	if reset {
		return state, h.Connections.Clear(ctx, apimw.GetUserID(ctx), device, key)
	}
	c, err := h.Connections.Connection(ctx, apimw.GetUserID(ctx), device, key)
	if err != nil || c == nil {
		return state, err
	}
	state.Probe, state.PlaybackSample, state.LearnedLimit = c.Probe, c.PlaybackSample, c.LearnedLimit
	// Both readings are returned for display whatever their age; only those
	// within the reading lifetime count toward the budget.
	probe, sample := h.currentReadings(state.Probe, state.PlaybackSample)
	state.AutoBudgetKbps = connectionstore.Budget(probe, sample, state.LearnedLimit, requestBitrateCapV3(ctx))
	return state, nil
}

// currentReadings keeps the readings younger than the reading lifetime.
func (h *PlaybackHandler) currentReadings(probe *connectionstore.Probe, sample *connectionstore.Sample) (*connectionstore.Probe, *connectionstore.Sample) {
	maxAge := h.playbackConfig().BandwidthProbeMaxAge()
	if probe != nil && time.Since(probe.MeasuredAt) > maxAge {
		probe = nil
	}
	if sample != nil && time.Since(sample.MeasuredAt) > maxAge {
		sample = nil
	}
	return probe, sample
}

// applyConnectionReading gives an Auto request without an estimate the
// connection's current reading, the one GET /api/v2/playback/connection
// reports, as a probe estimate: a client that starts before it has read its
// reading still plans under it. Call it only for the request the client sent;
// a seek replay keeps the stored request. A request that ignores the learned
// limit gets none, because a Cast start sends ignore and no estimate on
// purpose: the sender's reading is not the receiver's link. Nothing is
// recorded.
func (h *PlaybackHandler) applyConnectionReading(r *http.Request, req *playback.StartRequestV3) error {
	if !isNativeAPIV2(r.Context()) || !playback.HasFeatureV3(req.ClientFeatures, playback.FeatureBufferingQualityV1) ||
		req.QualityPreference != playback.QualityAutoV3 || req.BandwidthEstimateKbps != nil || req.LearnedLimit == playback.LearnedLimitIgnoreV3 {
		return nil
	}
	key, _ := connectionstore.Key(r.Context(), h.JWTSecret)
	device, _ := r.Context().Value(playbackConnectionDeviceContextKey{}).(string)
	c, err := h.Connections.Connection(r.Context(), apimw.GetUserID(r.Context()), connectionDeviceV3(device), key)
	if err != nil || c == nil {
		return err
	}
	if kbps := connectionstore.Reading(h.currentReadings(c.Probe, c.PlaybackSample)); kbps > 0 {
		req.BandwidthEstimateKbps, req.BandwidthEstimateSource = &kbps, playback.BandwidthEstimateProbeV3
	}
	return nil
}

// applyLearnedLimit plans Auto under the device's learned limit and returns the
// limit when it replaced the request's estimate. clientSent says whether req is
// the request the client just sent; only then are its clear and its
// observed_rebuffer estimate written to the store. A seek-scoped replan replays
// the stored start request instead, and this function may have rewritten that
// request's estimate to the limit, so recording it would renew the limit.
func (h *PlaybackHandler) applyLearnedLimit(r *http.Request, req *playback.StartRequestV3, clientSent bool) (*playback.BandwidthLimitV3, error) {
	if !isNativeAPIV2(r.Context()) || !playback.HasFeatureV3(req.ClientFeatures, playback.FeatureBufferingQualityV1) {
		return nil, nil
	}
	if observed := req.ObservedThroughputKbps; observed != nil && req.BandwidthEstimateKbps != nil && *observed < *req.BandwidthEstimateKbps {
		// The plan that kept buffering received less than the client's step
		// down from it: that throughput is what the link carried.
		req.BandwidthEstimateKbps = new(*observed)
	}
	key, _ := connectionstore.Key(r.Context(), h.JWTSecret)
	user := apimw.GetUserID(r.Context())
	device, _ := r.Context().Value(playbackConnectionDeviceContextKey{}).(string)
	device = connectionDeviceV3(device)
	if req.LearnedLimit == playback.LearnedLimitClearV3 {
		if !clientSent {
			return nil, nil
		}
		return nil, h.Connections.Clear(r.Context(), user, device, key)
	}
	if req.LearnedLimit == playback.LearnedLimitIgnoreV3 {
		return nil, nil
	}
	if clientSent && req.BandwidthEstimateSource == playback.BandwidthEstimateObservedRebufferV3 && req.BandwidthEstimateKbps != nil && req.QualityPreference == playback.QualityAutoV3 {
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
		// The stricter request estimate wins; the limit did not lower the plan.
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
	delivered := deliveredKbpsV3(result.Plan)
	if original > playback.StreamBudgetKbpsV3(limit.Kbps) && delivered > 0 && delivered < original && delivered < baseline.BitrateKbps {
		result.Plan.BandwidthLimit = limit
	}
}

// deliveredKbpsV3 is the bitrate a plan sends: its recipe's when it sets one,
// else its source's.
func deliveredKbpsV3(plan *playback.PlanV3) int {
	if plan.EffectiveRecipe.BitrateKbps != nil {
		return *plan.EffectiveRecipe.BitrateKbps
	}
	return plan.Source.BitrateKbps
}

// withoutObservedEstimateV3 is req as it plans without the learned limit. A
// stall the request reports becomes the limit, so its estimate goes too; a
// probe estimate stays.
func withoutObservedEstimateV3(req playback.StartRequestV3) playback.StartRequestV3 {
	if req.BandwidthEstimateSource == playback.BandwidthEstimateObservedRebufferV3 {
		req.BandwidthEstimateKbps, req.BandwidthEstimateSource = nil, ""
	}
	return req
}

// An Auto budget, whatever set it (a speed test, the connection's reading, a
// stall or a learned limit), first tries a lower original of the same edition:
// it costs the server nothing and keeps its picture. Of the versions within
// the budget it tries those that keep the source's dynamic range first, so an
// HDR source stays HDR when a lower HDR version plays as it is, then the
// highest bitrate. Candidate planning proves that the client can copy the
// video, including HDR, audio and subtitle constraints; a matching bitrate
// alone is insufficient.
func (h *PlaybackHandler) lowerBufferedVersion(r *http.Request, req playback.StartRequestV3, base *models.MediaFile, audio int, settings playback.PlannerSettingsV3) (*models.MediaFile, playback.StartRequestV3, int) {
	return h.lowerBufferedVersionForIntent(r, req, base, audio, settings, nil)
}

// lowerBufferedVersionForIntent is lowerBufferedVersion for a replan with the
// attempt's desired tracks: a candidate gets the viewer's chosen audio and
// subtitle, not a remap of what base happens to be playing.
func (h *PlaybackHandler) lowerBufferedVersionForIntent(r *http.Request, req playback.StartRequestV3, base *models.MediaFile, audio int, settings playback.PlannerSettingsV3, desired *playback.DesiredTracksV3) (*models.MediaFile, playback.StartRequestV3, int) {
	if req.QualityPreference != playback.QualityAutoV3 || req.BandwidthEstimateKbps == nil || !req.AllowsAlternateVersions() {
		return base, req, audio
	}
	budget := playback.StreamBudgetKbpsV3(*req.BandwidthEstimateKbps)
	if req.BandwidthCapKbps != nil {
		budget = min(budget, *req.BandwidthCapKbps)
	}
	if serverCap := serverBitrateCapV3(r.Context()); serverCap > 0 {
		budget = min(budget, serverCap)
	}
	source := playback.SourceDescriptorFromFileV3(base, audio)
	if source.BitrateKbps <= budget {
		return base, req, audio
	}
	filter := requestAccessFilter(r)
	alternates, err := h.findAlternateFiles(r.Context(), base, filter)
	if err != nil {
		return base, req, audio
	}
	type fittingVersion struct {
		file       *models.MediaFile
		keepsRange bool
		bitrate    int
	}
	var fitting []fittingVersion
	for _, candidate := range alternates {
		if candidate.PresentationPartIndex != base.PresentationPartIndex {
			continue
		}
		candidate = h.ensurePlaybackProbe(r.Context(), candidate)
		descriptor := playback.SourceDescriptorFromFileV3(candidate, 0)
		if descriptor.BitrateKbps <= 0 || descriptor.BitrateKbps > budget || !catalog.FileAllowedByAccess(candidate, filter) {
			continue
		}
		keepsRange := isHDRRangeV3(descriptor.DynamicRange) == isHDRRangeV3(source.DynamicRange)
		fitting = append(fitting, fittingVersion{file: candidate, keepsRange: keepsRange, bitrate: descriptor.BitrateKbps})
	}
	slices.SortStableFunc(fitting, func(a, b fittingVersion) int {
		if a.keepsRange != b.keepsRange {
			if a.keepsRange {
				return -1
			}
			return 1
		}
		return cmp.Compare(b.bitrate, a.bitrate)
	})
	for _, version := range fitting {
		candidate := version.file
		next, nextAudio, ok := h.bufferedCandidateTracksV3(r, req, base, candidate, audio, desired)
		if !ok {
			continue
		}
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

// bufferedCandidateTracksV3 moves req's audio and subtitle to candidate, from
// desired when the replan has it. ok is false when candidate would drop the
// subtitle being shown, or the selection cannot be moved.
func (h *PlaybackHandler) bufferedCandidateTracksV3(r *http.Request, req playback.StartRequestV3, base, candidate *models.MediaFile, audio int, desired *playback.DesiredTracksV3) (playback.StartRequestV3, int, bool) {
	next := req
	if desired != nil {
		showsSubtitle := req.SubtitleTrackIndex != nil || req.SubtitleTrackID != ""
		dropped, err := h.applyDesiredTracksV3(r.Context(), desired, candidate, &next)
		if err != nil || (dropped && showsSubtitle) {
			return next, 0, false
		}
		return next, requestAudioIndexV3(candidate, next), true
	}
	dropped, err := h.remapSubtitleSelectionV3(r.Context(), base, candidate, &next)
	if err != nil || dropped {
		return next, 0, false
	}
	// The returned request is the one later replans resolve against, so
	// its audio selection moves to the candidate with the file.
	if err := remapAudioSelectionV3(base, candidate, &next); err != nil {
		return next, 0, false
	}
	return next, remapAudioIndexV3(base, candidate, audio), true
}

func isHDRRangeV3(dynamicRange string) bool {
	return dynamicRange != "" && dynamicRange != playback.DynamicRangeSDRV3
}

func (h *PlaybackHandler) ReportPlaybackProbe(ctx context.Context, device string, result connectionstore.ProbeResult) error {
	key, _ := connectionstore.Key(ctx, h.JWTSecret)
	return h.Connections.ReportProbe(ctx, apimw.GetUserID(ctx), connectionDeviceV3(device), key, result)
}
func (h *PlaybackHandler) AdminPlaybackConnections(ctx context.Context, user int, device string) ([]connectionstore.AdminConnection, error) {
	return h.Connections.List(ctx, user, device)
}
func (h *PlaybackHandler) ClearAdminPlaybackLimit(ctx context.Context, user int, device, key string) error {
	return h.Connections.Clear(ctx, user, device, key)
}
