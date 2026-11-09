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
// on a transcode node, as last read from the node by remoteDeliveryCache.
type sessionDelivery struct {
	local  hlsDeliverySource
	remote map[string]remoteDelivery
}

func (d sessionDelivery) hls(s *playback.Session, now time.Time) (playback.HLSDeliveryProgress, bool) {
	if s.TranscodeNodeURL != "" {
		r, ok := d.remote[s.ID]
		// A session moved to another node or transport since the read must not
		// show the values of the one it left.
		if !ok || r.nodeURL != s.TranscodeNodeURL || r.transportID != transportSessionID(s) {
			return playback.HLSDeliveryProgress{}, false
		}
		return r.progress, true
	}
	if d.local == nil {
		return playback.HLSDeliveryProgress{}, false
	}
	return d.local.DeliveryProgress(s.ID, now)
}

// remoteDelivery is one session's progress as read from its transcode node,
// with the node and transport id it was read for.
type remoteDelivery struct {
	nodeURL     string
	transportID string
	progress    playback.HLSDeliveryProgress
}

// transportSessionID is the id the session's transcode node knows it by.
func transportSessionID(s *playback.Session) string {
	if s.TranscodeTransportID != "" {
		return s.TranscodeTransportID
	}
	return s.ID
}

// remoteDeliveryCache holds the latest node read. Several session syncs run
// on client request paths, so only the periodic reconciler tick reads the
// nodes (before its own sync) and every sync takes the values from here.
type remoteDeliveryCache struct {
	mu      sync.Mutex
	entries map[string]remoteDelivery
}

// refresh replaces the cached progress with a fresh read of the nodes.
func (c *remoteDeliveryCache) refresh(ctx context.Context, sessions []*playback.Session, fetch nodeDeliveryFetcher) {
	entries := fetchRemoteDelivery(ctx, sessions, fetch)
	c.mu.Lock()
	c.entries = entries
	c.mu.Unlock()
}

// snapshot returns the latest read. refresh replaces the map rather than
// changing it, so callers may read it without the lock.
func (c *remoteDeliveryCache) snapshot() map[string]remoteDelivery {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries
}

// nodeDeliveryFetcher reads the HLS delivery progress of every session on the
// transcode node at nodeURL, keyed by transport session id.
type nodeDeliveryFetcher func(ctx context.Context, nodeURL string) (map[string]playback.HLSDeliveryProgress, error)

// remoteDeliveryTimeout bounds one read of the nodes. Delivery progress is
// display data: a slow or hung node costs its sessions the values until the
// next read, never the sync.
const remoteDeliveryTimeout = 2 * time.Second

// fetchRemoteDelivery reads, one call per node and all nodes at once, the
// delivery progress of the HLS sessions this process hands to transcode
// nodes, keyed by API session id.
func fetchRemoteDelivery(ctx context.Context, sessions []*playback.Session, fetch nodeDeliveryFetcher) map[string]remoteDelivery {
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
		out = make(map[string]remoteDelivery)
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
				transportID := transportSessionID(s)
				if p, ok := listing[transportID]; ok {
					out[s.ID] = remoteDelivery{nodeURL: nodeURL, transportID: transportID, progress: p}
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
		SessionID:                    s.ID,
		UserID:                       s.UserID,
		ProfileID:                    s.ProfileID,
		MediaFileID:                  s.MediaFileID,
		RequestedMediaFileID:         s.RequestedMediaFileID,
		PlayMethod:                   string(s.PlayMethod),
		ReportingNode:                reportingNode,
		ClientIP:                     s.ClientIP,
		StreamLocation:               s.StreamLocation,
		ClientName:                   s.ClientName,
		ClientVersion:                s.ClientVersion,
		ClientBuild:                  s.ClientBuild,
		ClientChannel:                s.ClientChannel,
		ClientUserAgent:              s.ClientUserAgent,
		DeviceID:                     s.DeviceID,
		DevicePlatform:               s.DevicePlatform,
		AudioTrackIndex:              s.AudioTrackIndex,
		TranscodeAudio:               s.TranscodeAudio,
		StreamBitrateKbps:            s.StreamBitrateKbps,
		TranscodeNodeURL:             s.TranscodeNodeURL,
		TargetResolution:             s.TargetResolution,
		TargetVideoCodec:             s.TargetVideoCodec,
		OutputContainer:              s.OutputContainer,
		OutputProtocol:               s.OutputProtocol,
		TargetAudioCodec:             s.TargetAudioCodec,
		TargetAudioChannels:          s.TargetAudioChannels,
		TargetBitrateKbps:            s.TargetBitrateKbps,
		TranscodeHWAccel:             s.TranscodeHWAccel,
		ToneMapMode:                  string(s.ToneMapMode),
		RoutingNetworkProvider:       s.RoutingNetworkProvider,
		RoutingWorkload:              s.RoutingWorkload,
		RoutingExecution:             s.RoutingExecution,
		RoutingExecutionNodeID:       s.RoutingExecutionNodeID,
		RoutingExecutionNodeURL:      s.RoutingExecutionNodeURL,
		RoutingEgress:                s.RoutingEgress,
		RoutingEgressNodeID:          s.RoutingEgressNodeID,
		RoutingEgressNodeURL:         s.RoutingEgressNodeURL,
		StartedAt:                    s.StartedAt,
		UpdatedAt:                    s.UpdatedAt,
		PositionSeconds:              s.Position,
		IsPaused:                     s.IsPaused,
		HasWebSocket:                 s.HasWebSocket,
		IsJellyfinCompat:             s.IsJellyfinCompat,
		DeliveredThroughSeconds:      delivered,
		ProducedThroughSeconds:       produced,
		ClientBufferedThroughSeconds: liveClientBuffer(s),
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

// liveClientBuffer is the media time the client last reported buffered
// through, in whole seconds like the observed progress.
func liveClientBuffer(s *playback.Session) *float64 {
	if through, ok := s.ClientBufferedThroughSeconds(); ok {
		return wholeSeconds(through)
	}
	return nil
}

func wholeSeconds(seconds float64) *float64 {
	return new(math.Round(seconds))
}
