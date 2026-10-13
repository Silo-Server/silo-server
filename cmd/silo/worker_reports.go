package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type workerReportSink struct {
	rdb         *redis.Client
	nodes       workercontrolcore.NodeRegistry
	recipes     *noderecipe.Store
	assignments workercontrolcore.AssignmentStore
	service     func() *workercontrolcore.Service
	mu          sync.Mutex
	trackers    map[int]*nodesessions.Tracker
	previous    map[int]map[string]struct{}
	publishers  map[int]*streamtelemetry.RedisStore
}

func (s *workerReportSink) accept(ctx context.Context, nodeID int, raw json.RawMessage) error {
	var batch workerwire.Report
	if err := json.Unmarshal(raw, &batch); err != nil {
		return err
	}
	if batch.BootID == "" || len(batch.BootID) > 128 || batch.Sequence == 0 || len(batch.Sessions) > 128 {
		return errors.New("invalid worker report bounds")
	}
	if o := batch.Observations; o != nil && (len(o.Scopes) > 128 || len(o.Snapshot.Sessions) > 128 || len(o.Snapshot.Transfers) != 0 || len(o.Logs) > 256) {
		return errors.New("invalid worker observation bounds")
	}
	node, ok, err := s.nodes.ByID(ctx, nodeID)
	if err != nil || !ok || !node.Enabled {
		return errors.New("worker report identity unavailable")
	}
	ctx, release, publish, err := lockWorkerReport(ctx, s.rdb, nodeID, batch)
	if err != nil {
		return err
	}
	defer release()
	if !publish {
		return nil
	}
	valid := func(work string, generation uint64) bool {
		return work != "" && generation > 0 && s.service().WorkActive(ctx, nodeID, work, generation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.trackers == nil {
		s.trackers = make(map[int]*nodesessions.Tracker)
		s.previous = make(map[int]map[string]struct{})
		s.publishers = make(map[int]*streamtelemetry.RedisStore)
	}
	tracker := s.trackers[nodeID]
	if tracker == nil || tracker.NodeURL() != node.URL {
		tracker = nodesessions.NewTracker(s.rdb, node.URL, "HTTP worker", "transcode")
		s.trackers[nodeID] = tracker
	}
	present := make(map[string]struct{})
	for _, item := range batch.Sessions {
		if !valid(item.WorkID, item.Generation) {
			continue
		}
		info := item.Info
		if info.SessionID == "" || len(info.SessionID) > 256 {
			return errors.New("invalid reported session")
		}
		info.AuthUserID = 0
		info.ProfileID = ""
		info.MediaFileID = 0
		info.UserID = ""
		info.MediaItemID = ""
		info.MediaTitle = ""
		if card, found := s.recipes.Get(ctx, item.WorkID); found && card != nil {
			if !workeridentity.SameOrigin(card.TranscodeNodeURL, node.URL, node.AllowHTTP) {
				continue
			}
			if info.SessionID != item.WorkID && info.SessionID != card.SessionID {
				continue
			}
			info.AuthUserID = card.UserID
			info.ProfileID = card.ProfileID
			info.MediaFileID = card.MediaFileID
			info.SessionID = card.SessionID
		} else if info.SessionID != item.WorkID {
			assignment, found, err := s.assignments.Get(ctx, nodeID, item.WorkID)
			if err != nil || !found || assignment.Generation != item.Generation || assignment.Path != workerPreparePath {
				continue
			}
			var identity struct {
				LogSessionID string `json:"log_session_id"`
			}
			if json.Unmarshal(assignment.Body, &identity) != nil || identity.LogSessionID != info.SessionID {
				continue
			}
		}
		info.NodeURL = node.URL
		info.NodeName = "HTTP worker"
		tracker.Track(ctx, info)
		present[info.SessionID] = struct{}{}
	}
	for id := range s.previous[nodeID] {
		if _, ok := present[id]; !ok {
			tracker.Remove(ctx, id)
		}
	}
	s.previous[nodeID] = present
	if batch.Observations == nil {
		return nil
	}
	o := batch.Observations
	scopes := make(map[string]workerwire.ObservationScope)
	for _, scope := range o.Scopes {
		if valid(scope.WorkID, scope.Generation) {
			scopes[scope.SessionID] = scope
		}
	}
	snapshot := o.Snapshot
	snapshot.PublisherID = "http-worker-" + strconv.Itoa(nodeID)
	snapshot.NodeID = snapshot.PublisherID
	snapshot.Sequence = batch.Sequence
	snapshot.CapturedAt = time.Now().UTC()
	snapshot.Transfers = nil
	allowed := snapshot.Sessions[:0]
	for _, view := range snapshot.Sessions {
		scope, ok := scopes[view.SessionID]
		if !ok {
			continue
		}
		card, found := s.recipes.Get(ctx, scope.WorkID)
		if !found || card == nil || card.SessionID != view.SessionID || !workeridentity.SameOrigin(card.TranscodeNodeURL, node.URL, node.AllowHTTP) {
			continue
		}
		view.Subject = streamtelemetry.Subject{Kind: streamtelemetry.SubjectUser, ID: strconv.Itoa(card.UserID)}
		view.ProfileID = card.ProfileID
		view.MediaFileID = card.MediaFileID
		view.ViewerIPs = nil
		view.DeviceIDs = nil
		view.UserAgents = nil
		view.ClientVariants = nil
		view.IdentityConflicts = nil
		view.TokenIssuedAts = nil
		view.MediaFileIDs = nil
		view.PlayMethods = nil
		if len(view.Routes) > 16 {
			return errors.New("worker route observation bound exceeded")
		}
		for i, route := range view.Routes {
			if !workerObservationRoute(route.Method, route.Pattern) {
				return errors.New("worker reported undeclared route")
			}
			view.Routes[i].Role = streamtelemetry.RoleInternalRelay
			view.Routes[i].CapRelevant = false
			view.Routes[i].Class = streamtelemetry.ClassPlayback
			if route.Pattern == "/transcode/{session_id}/master.m3u8" {
				view.Routes[i].Class = streamtelemetry.ClassManifest
			}
			if route.Pattern == "/downloads/artifacts/{artifact_id}" {
				view.Routes[i].Class = streamtelemetry.ClassTransfer
			}
		}
		allowed = append(allowed, view)
	}
	snapshot.Sessions = allowed
	publisher := s.publishers[nodeID]
	if publisher == nil {
		cfg := streamtelemetry.ConfigFromEnv(snapshot.NodeID)
		cfg.PublisherID = snapshot.PublisherID
		publisher = streamtelemetry.NewRedisStore(s.rdb, cfg, slog.Default())
		s.publishers[nodeID] = publisher
	}
	if err := publisher.Publish(ctx, snapshot); err != nil {
		return err
	}
	for _, line := range o.Logs {
		if len(line.Message) > 1024 || !valid(line.WorkID, line.Generation) {
			continue
		}
		slog.InfoContext(ctx, "worker ffmpeg", "component", "ffmpeg", "node_id", strconv.Itoa(nodeID), "playback_session_id", line.WorkID, "ffmpeg_line", workerwire.SanitizeLogMessage(line.Message), "execution_mode", line.ExecutionMode, "restart_count", line.RestartCount, "line_index", line.LineIndex, "dropped_lines", line.DroppedLines)
	}
	return nil
}

func workerObservationRoute(method, pattern string) bool {
	if method != "GET" && method != "HEAD" {
		return false
	}
	switch pattern {
	case "/transcode/{session_id}/master.m3u8", "/transcode/{session_id}/segment/{name}", "/remux/{session_id}", "/downloads/artifacts/{artifact_id}":
		return true
	}
	return false
}
