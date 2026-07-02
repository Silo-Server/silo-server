package main

import (
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/worker"
)

// buildLiveSessionSync converts an in-memory playback session into the shared
// live admin session snapshot. For live admin views, play_method tracks the
// current transport method rather than the preserved semantic/base method used
// by player-facing responses and playback history.
func buildLiveSessionSync(s *playback.Session, reportingNode string) worker.SessionSync {
	if s == nil {
		return worker.SessionSync{ReportingNode: reportingNode}
	}

	return worker.SessionSync{
		SessionID:            s.ID,
		UserID:               s.UserID,
		ProfileID:            s.ProfileID,
		MediaFileID:          s.MediaFileID,
		RequestedMediaFileID: s.RequestedMediaFileID,
		PlayMethod:           string(s.PlayMethod),
		ReportingNode:        reportingNode,
		ClientIP:             s.ClientIP,
		ClientName:           s.ClientName,
		ClientVersion:        s.ClientVersion,
		ClientUserAgent:      s.ClientUserAgent,
		AudioTrackIndex:      s.AudioTrackIndex,
		TranscodeAudio:       s.TranscodeAudio,
		StreamBitrateKbps:    s.StreamBitrateKbps,
		TranscodeNodeURL:     s.TranscodeNodeURL,
		TargetResolution:     s.TargetResolution,
		TargetVideoCodec:     s.TargetVideoCodec,
		TargetAudioCodec:     s.TargetAudioCodec,
		TargetBitrateKbps:    s.TargetBitrateKbps,
		TranscodeHWAccel:     s.TranscodeHWAccel,
		StartedAt:            s.StartedAt,
		UpdatedAt:            s.UpdatedAt,
		PositionSeconds:      s.Position,
		IsPaused:             s.IsPaused,
		HasWebSocket:         s.HasWebSocket,
	}
}
