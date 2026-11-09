package main

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/worker"
)

// hlsDeliverySource reports the delivery progress of a transcode this process
// runs; *playback.LocalTranscodes is the production source.
type hlsDeliverySource interface {
	DeliveryProgress(sessionID string, now time.Time) (playback.HLSDeliveryProgress, bool)
}

// sessionDelivery is where one sync pass reads HLS delivery progress: local
// for transcodes this process runs, remote (by API session id) for sessions
// on a transcode node, read from the node at the start of the pass.
type sessionDelivery struct {
	local  hlsDeliverySource
	remote map[string]playback.HLSDeliveryProgress
}

func (d sessionDelivery) hls(s *playback.Session, now time.Time) (playback.HLSDeliveryProgress, bool) {
	if s.TranscodeNodeURL != "" {
		p, ok := d.remote[s.ID]
		return p, ok
	}
	if d.local == nil {
		return playback.HLSDeliveryProgress{}, false
	}
	return d.local.DeliveryProgress(s.ID, now)
}

// nodeDeliveryFetcher reads the HLS delivery progress of every session on the
// transcode node at nodeURL, keyed by transport session id.
type nodeDeliveryFetcher func(ctx context.Context, nodeURL string) (map[string]playback.HLSDeliveryProgress, error)

// remoteDeliveryTimeout bounds the node reads of one sync pass. Delivery
// progress is display data: a slow or hung node costs its sessions the values
// for that pass, never the pass itself.
const remoteDeliveryTimeout = 2 * time.Second

// fetchRemoteDelivery reads, one call per node and all nodes at once, the
// delivery progress of the HLS sessions this process hands to transcode
// nodes, keyed by API session id.
func fetchRemoteDelivery(ctx context.Context, sessions []*playback.Session, fetch nodeDeliveryFetcher) map[string]playback.HLSDeliveryProgress {
	byNode := make(map[string][]*playback.Session)
	for _, s := range sessions {
		if s == nil || s.StopReported || s.TranscodeNodeURL == "" || s.PlayMethod == playback.PlayDirect {
			continue
		}
		byNode[s.TranscodeNodeURL] = append(byNode[s.TranscodeNodeURL], s)
	}
	if len(byNode) == 0 || fetch == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, remoteDeliveryTimeout)
	defer cancel()

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = make(map[string]playback.HLSDeliveryProgress)
	)
	for nodeURL, onNode := range byNode {
		wg.Go(func() {
			listing, err := fetch(ctx, nodeURL)
			if err != nil {
				slog.DebugContext(ctx, "read transcode node delivery progress", "component", "app", "error", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, s := range onNode {
				transportID := s.TranscodeTransportID
				if transportID == "" {
					transportID = s.ID
				}
				if p, ok := listing[transportID]; ok {
					out[s.ID] = p
				}
			}
		})
	}
	wg.Wait()
	return out
}

// buildLiveSessionSyncs builds the live admin snapshot for a node. A play the
// client reported stopped without an ID that could end it stays out of the
// snapshot, while its session keeps its pause state and idle grace (#1454).
func buildLiveSessionSyncs(sessions []*playback.Session, reportingNode string, delivery sessionDelivery) []worker.SessionSync {
	syncs := make([]worker.SessionSync, 0, len(sessions))
	for _, s := range sessions {
		if s == nil || s.StopReported {
			continue
		}
		syncs = append(syncs, buildLiveSessionSync(s, reportingNode, delivery))
	}
	return syncs
}

// buildLiveSessionSync converts an in-memory playback session into the shared
// live admin session snapshot. For live admin views, play_method tracks the
// current transport method rather than the preserved semantic/base method used
// by player-facing responses and playback history.
func buildLiveSessionSync(s *playback.Session, reportingNode string, delivery sessionDelivery) worker.SessionSync {
	if s == nil {
		return worker.SessionSync{ReportingNode: reportingNode}
	}

	delivered, produced := liveDeliveryProgress(s, delivery)
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

// liveDeliveryProgress reports how far a session has been delivered ahead of
// its position and, for a transcode, produced. Values are whole seconds so
// sampling noise does not make every sync a change. Direct Play counts only
// what this process served; HLS comes from this process or the session's
// transcode node.
func liveDeliveryProgress(s *playback.Session, delivery sessionDelivery) (delivered, produced *float64) {
	if s.PlayMethod == playback.PlayDirect {
		if through, ok := s.DeliveredThroughSeconds(); ok {
			delivered = wholeSeconds(through)
		}
		return delivered, nil
	}
	progress, ok := delivery.hls(s, time.Now())
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
