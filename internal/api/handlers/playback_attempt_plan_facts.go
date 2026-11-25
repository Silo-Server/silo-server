package handlers

import (
	"github.com/Silo-Server/silo-server/internal/playback"
)

// applyAttemptPlanFacts reconciles a detached session (whose live row may be
// gone, leaving only start_facts) with the attempt's latest committed plan and
// frozen recipe. When a replan changed the play method, delivery transport, or
// container — or when no live row survived a replan — runtime-only executor
// fields from the earlier plan are cleared rather than attributed to the new
// delivery.
func applyAttemptPlanFacts(session *playback.Session, record *playback.AttemptRecordV3, fromLiveRow bool) {
	if session == nil || record == nil || record.CurrentPlanID == "" {
		return
	}
	if effID := record.EffectiveMediaFileID; effID > 0 {
		session.MediaFileID = effID
	} else if record.CurrentPlan.EffectiveMediaFileID > 0 {
		session.MediaFileID = record.CurrentPlan.EffectiveMediaFileID
	}
	if reqID := record.RequestedMediaFileID; reqID > 0 {
		session.RequestedMediaFileID = reqID
	} else if record.CurrentPlan.RequestedMediaFileID > 0 {
		session.RequestedMediaFileID = record.CurrentPlan.RequestedMediaFileID
	}
	if record.CurrentPlan.SelectedTracks.Audio != nil && record.CurrentPlan.SelectedTracks.Audio.Index != nil {
		session.AudioTrackIndex = *record.CurrentPlan.SelectedTracks.Audio.Index
	}
	session.Subtitle = playback.SessionSubtitleFromPlanV3(&record.CurrentPlan)
	recipe := record.FrozenRecipe
	if recipe.PlanID != record.CurrentPlanID || recipe.PlayMethod == "" {
		return
	}
	out := playback.ResolvePlannedOutputV3(
		record.CurrentPlan,
		recipe.PlayMethod,
		recipe.SourceVideoCodec,
		recipe.TargetVideoCodec,
		"",
	)
	if detachedSessionRouteStale(session, record, recipe, out, fromLiveRow) {
		clearDetachedRouteExecutor(session)
	}
	session.PlanID = record.CurrentPlanID
	session.PlayMethod = recipe.PlayMethod
	session.TranscodeAudio = recipe.TranscodeAudio
	if recipe.PlayMethod == playback.PlayDirect {
		clearDirectPlayTargets(session)
		return
	}
	session.TargetVideoCodec = out.TargetVideoCodec
	session.TargetResolution = recipe.TargetResolution
	session.TargetAudioCodec = recipe.TargetAudioCodec
	session.TargetAudioChannels = recipe.TargetAudioChannels
	session.TargetBitrateKbps = recipe.TargetBitrateKbps
	if out.ClearsVideoTarget {
		session.TargetResolution = ""
		session.TargetBitrateKbps = 0
	}
	session.ToneMapMode = recipe.ToneMapMode
	session.AdaptationReasons = playback.NormalizeAdaptationReasons(recipe.AdaptationReasons)
	if out.OutputProtocol != "" {
		session.OutputProtocol = out.OutputProtocol
	}
	if out.OutputContainer != "" {
		session.OutputContainer = out.OutputContainer
	}
}

// detachedSessionRouteStale reports whether runtime executor/routing fields on
// a detached session may belong to a plan older than record.CurrentPlanID.
func detachedSessionRouteStale(
	session *playback.Session,
	record *playback.AttemptRecordV3,
	recipe playback.ExecutableRecipeV3,
	out playback.PlannedOutputV3,
	fromLiveRow bool,
) bool {
	if session.PlayMethod != "" && session.PlayMethod != recipe.PlayMethod {
		return true
	}
	if out.OutputProtocol != "" && session.OutputProtocol != "" && session.OutputProtocol != out.OutputProtocol {
		return true
	}
	if out.OutputContainer != "" && session.OutputContainer != "" && session.OutputContainer != out.OutputContainer {
		return true
	}
	if !fromLiveRow {
		return record.CurrentReplanRequestID != ""
	}
	if record.CurrentPlanID == "" {
		return false
	}
	if session.PlanID != "" {
		return session.PlanID != record.CurrentPlanID
	}
	return record.CurrentReplanRequestID != ""
}

func clearDetachedRouteExecutor(session *playback.Session) {
	session.TranscodeHWAccel = ""
	session.OutputContainer = ""
	session.OutputProtocol = ""
	session.RoutingExecution = ""
	session.RoutingExecutionNodeID = 0
	session.RoutingEgress = ""
	session.RoutingEgressNodeID = 0
}

func clearDirectPlayTargets(session *playback.Session) {
	session.TargetVideoCodec = ""
	session.TargetResolution = ""
	session.TargetAudioCodec = ""
	session.TargetAudioChannels = 0
	session.TargetBitrateKbps = 0
	session.TranscodeHWAccel = ""
	session.ToneMapMode = ""
	session.AdaptationReasons = nil
	session.OutputProtocol = playback.OutputProtocolHTTP
}
