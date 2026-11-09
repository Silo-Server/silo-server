package main

import (
	"math"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/worker"
)

// hlsDeliverySource reports the delivery progress of a transcode this process
// runs; *playback.LocalTranscodes is the production source.
type hlsDeliverySource interface {
	DeliveryProgress(sessionID string, now time.Time) (playback.HLSDeliveryProgress, bool)
}

// buildLiveSessionSyncs builds the live admin snapshot for a node. A play the
// client reported stopped without an ID that could end it stays out of the
// snapshot, while its session keeps its pause state and idle grace (#1454).
func buildLiveSessionSyncs(sessions []*playback.Session, reportingNode string, transcodes hlsDeliverySource) []worker.SessionSync {
	syncs := make([]worker.SessionSync, 0, len(sessions))
	for _, s := range sessions {
		if s == nil || s.StopReported {
			continue
		}
		syncs = append(syncs, buildLiveSessionSync(s, reportingNode, transcodes))
	}
	return syncs
}

// buildLiveSessionSync converts an in-memory playback session into the shared
// live admin session snapshot. For live admin views, play_method tracks the
// current transport method rather than the preserved semantic/base method used
// by player-facing responses and playback history.
func buildLiveSessionSync(s *playback.Session, reportingNode string, transcodes hlsDeliverySource) worker.SessionSync {
	if s == nil {
		return worker.SessionSync{ReportingNode: reportingNode}
	}

	delivered, produced := liveDeliveryProgress(s, transcodes)
	return worker.SessionSync{
		SessionID:               s.ID,
		UserID:                  s.UserID,
		ProfileID:               s.ProfileID,
		MediaFileID:             s.MediaFileID,
		RequestedMediaFileID:    s.RequestedMediaFileID,
		PlayMethod:              string(s.PlayMethod),
		ReportingNode:           reportingNode,
		ClientIP:                s.ClientIP,
		StreamLocation:          s.StreamLocation,
		ClientName:              s.ClientName,
		ClientVersion:           s.ClientVersion,
		ClientBuild:             s.ClientBuild,
		ClientChannel:           s.ClientChannel,
		ClientUserAgent:         s.ClientUserAgent,
		DeviceID:                s.DeviceID,
		DevicePlatform:          s.DevicePlatform,
		AudioTrackIndex:         s.AudioTrackIndex,
		TranscodeAudio:          s.TranscodeAudio,
		StreamBitrateKbps:       s.StreamBitrateKbps,
		TranscodeNodeURL:        s.TranscodeNodeURL,
		TargetResolution:        s.TargetResolution,
		TargetVideoCodec:        s.TargetVideoCodec,
		OutputContainer:         s.OutputContainer,
		OutputProtocol:          s.OutputProtocol,
		TargetAudioCodec:        s.TargetAudioCodec,
		TargetAudioChannels:     s.TargetAudioChannels,
		TargetBitrateKbps:       s.TargetBitrateKbps,
		TranscodeHWAccel:        s.TranscodeHWAccel,
		ToneMapMode:             string(s.ToneMapMode),
		RoutingNetworkProvider:  s.RoutingNetworkProvider,
		RoutingWorkload:         s.RoutingWorkload,
		RoutingExecution:        s.RoutingExecution,
		RoutingExecutionNodeID:  s.RoutingExecutionNodeID,
		RoutingExecutionNodeURL: s.RoutingExecutionNodeURL,
		RoutingEgress:           s.RoutingEgress,
		RoutingEgressNodeID:     s.RoutingEgressNodeID,
		RoutingEgressNodeURL:    s.RoutingEgressNodeURL,
		StartedAt:               s.StartedAt,
		UpdatedAt:               s.UpdatedAt,
		PositionSeconds:         s.Position,
		IsPaused:                s.IsPaused,
		HasWebSocket:            s.HasWebSocket,
		IsJellyfinCompat:        s.IsJellyfinCompat,
		DeliveredThroughSeconds: delivered,
		ProducedThroughSeconds:  produced,
	}
}

// liveDeliveryProgress reports how far a session served by this process has
// been delivered ahead of its position and, for a transcode, produced. Values
// are whole seconds so sampling noise does not make every sync a change.
// Streams this process does not serve itself report neither.
func liveDeliveryProgress(s *playback.Session, transcodes hlsDeliverySource) (delivered, produced *float64) {
	if s.PlayMethod == playback.PlayDirect {
		if through, ok := s.DeliveredThroughSeconds(); ok {
			delivered = wholeSeconds(through)
		}
		return delivered, nil
	}
	if s.TranscodeNodeURL != "" || transcodes == nil {
		return nil, nil
	}
	progress, ok := transcodes.DeliveryProgress(s.ID, time.Now())
	if !ok {
		return nil, nil
	}
	if through, ok := progress.FetchedThroughAt(s.Position); ok {
		delivered = wholeSeconds(through)
	}
	if progress.ProducedOK {
		produced = wholeSeconds(progress.ProducedThrough)
	}
	return delivered, produced
}

func wholeSeconds(seconds float64) *float64 {
	return new(math.Round(seconds))
}
