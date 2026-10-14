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
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type workerReportPlanReader interface {
	GetAttempt(context.Context, string) (*playback.AttemptRecordV3, error)
}

type workerReportSink struct {
	plans       workerReportPlanReader
	rdb         *redis.Client
	nodes       workercontrolcore.NodeRegistry
	recipes     *noderecipe.Store
	assignments workercontrolcore.AssignmentStore
	service     func() *workercontrolcore.Service
	mu          sync.Mutex
	trackers    map[int]*nodesessions.Tracker
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
	if s.trackers == nil {
		s.trackers = make(map[int]*nodesessions.Tracker)
		s.publishers = make(map[int]*streamtelemetry.RedisStore)
	}
	tracker := s.trackers[nodeID]
	if tracker == nil || tracker.NodeURL() != node.URL {
		tracker = nodesessions.NewTracker(s.rdb, node.URL, "HTTP worker", "transcode")
		s.trackers[nodeID] = tracker
	}
	s.mu.Unlock()
	groups := make(map[string][]workerwire.SessionReport, len(batch.Sessions))
	transports := make(map[string]struct{}, len(batch.Sessions))
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
		if _, duplicate := transports[item.WorkID]; duplicate {
			return errors.New("duplicate reported transport")
		}
		transports[item.WorkID] = struct{}{}
		item.Info = info
		groups[info.SessionID] = append(groups[info.SessionID], item)
	}
	sessions := make(map[string]workerwire.SessionReport, len(groups))
	ambiguous := make(map[string]bool)
	// Leave time to publish unrelated sessions if plan storage is unavailable.
	planCtx, cancelPlans := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancelPlans()
	for id, reports := range groups {
		if len(reports) == 1 {
			sessions[id] = reports[0]
			continue
		}
		ambiguous[id] = true
		if current, found := s.currentReportedTransport(planCtx, node, id, reports); found {
			sessions[id] = current
		}
	}
	infos := make([]nodesessions.SessionInfo, 0, len(sessions))
	for _, item := range sessions {
		infos = append(infos, item.Info)
	}
	lease, ok := ctx.Value(workerReportLeaseKey{}).(workerReportLease)
	if !ok {
		return errors.New("worker report lease unavailable")
	}
	if err := tracker.ReplaceSnapshot(ctx, "http-worker-"+strconv.Itoa(nodeID), nodesessions.SnapshotFence{Key: lease.key, Value: lease.owner}, infos); err != nil {
		return err
	}
	if batch.Observations == nil {
		return nil
	}
	o := batch.Observations
	scopes := make(map[string]workerwire.ObservationScope)
	for _, scope := range o.Scopes {
		selected, found := sessions[scope.SessionID]
		if found && selected.WorkID != scope.WorkID || !found && ambiguous[scope.SessionID] {
			continue
		}
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
	s.mu.Lock()
	publisher := s.publishers[nodeID]
	if publisher == nil {
		cfg := streamtelemetry.ConfigFromEnv(snapshot.NodeID)
		cfg.PublisherID = snapshot.PublisherID
		// API replicas share this publisher without sharing delta state.
		cfg.FullResyncEvery = 1
		publisher = streamtelemetry.NewRedisStore(s.rdb, cfg, slog.Default())
		s.publishers[nodeID] = publisher
	}
	s.mu.Unlock()
	if err := publisher.PublishWithLease(ctx, snapshot, lease.key, lease.owner); err != nil {
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

// Worker timestamps cannot distinguish the committed live transport from a
// prepared replacement or one being torn down. Resolve overlaps from the
// durable attempt, shared by every API replica, after validating every entry.
func (s *workerReportSink) currentReportedTransport(ctx context.Context, node workercontrolcore.Node, id string, reports []workerwire.SessionReport) (workerwire.SessionReport, bool) {
	if s.plans == nil {
		return workerwire.SessionReport{}, false
	}
	attempt, err := s.plans.GetAttempt(ctx, id)
	if err != nil || attempt == nil || attempt.SessionID != id || attempt.StoppedAt != nil || !attempt.ExpiresAt.After(time.Now()) || !workeridentity.SameOrigin(attempt.FrozenRecipe.TranscodeNodeURL, node.URL, node.AllowHTTP) {
		return workerwire.SessionReport{}, false
	}
	for _, report := range reports {
		if report.WorkID == attempt.FrozenRecipe.TranscodeTransportID && report.Info.AuthUserID == attempt.UserID && report.Info.ProfileID == attempt.ProfileID && report.Info.MediaFileID == attempt.EffectiveMediaFileID {
			return report, true
		}
	}
	return workerwire.SessionReport{}, false
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
