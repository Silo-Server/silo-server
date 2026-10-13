package workercontrol

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

const (
	maxObservationLogs         = 256
	maxObservationBatchLogs    = 128
	maxObservationMessageBytes = 1024
	maxObservationSessions     = 64
)

type queuedLog struct {
	sequence uint64
	record   workerwire.FFmpegObservation
}

type observations struct {
	runtime  *Runtime
	registry *streamtelemetry.Registry
	mu       sync.Mutex
	logs     []queuedLog
	sequence uint64
	pending  uint64
	dropped  uint64
}

func newObservations(r *Runtime) *observations {
	cfg := streamtelemetry.ConfigFromEnv(strconv.Itoa(r.options.Binding.NodeID))
	cfg.Distributed = false
	cfg.DistributedExplicit = true
	cfg.PublisherID = "http-worker:" + strconv.Itoa(r.options.Binding.NodeID) + ":" + r.bootID
	cfg.MaxSessions = min(cfg.MaxSessions, int64(maxObservationSessions))
	cfg.MaxTransfers = min(cfg.MaxTransfers, int64(maxObservationSessions))
	cfg.MaxObservations = min(cfg.MaxObservations, 512)
	cfg.MaxObservationsPerSession = min(cfg.MaxObservationsPerSession, 16)
	cfg.MaxViewerIPsPerSession = 1
	cfg.MaxDeviceIDsPerSession = 1
	cfg.MaxClientVariantsPerSession = 1
	cfg.MaxIdentityConflictsPerSession = 1
	cfg.MaxMediaFileIDsPerSession = 1
	cfg.MaxPlayMethodsPerSession = 1
	cfg.MaxTokenIssuedAtPerSession = 1
	cfg.MaxRoutesPerSession = 16
	return &observations{runtime: r, registry: streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)}
}

func (r *Runtime) initObservations() {
	r.observations = newObservations(r)
	r.server.SetStreamTelemetry(r.observations.registry)
	r.server.SetFFmpegLogSink(r.observations)
}

func (o *observations) WriteLine(ctx context.Context, sessionID string, attrs playback.FFmpegLogAttrs, line string) {
	o.enqueue(ctx, sessionID, attrs, line, false)
}
func (o *observations) WriteEvent(ctx context.Context, sessionID string, attrs playback.FFmpegLogAttrs, message string) {
	o.enqueue(ctx, sessionID, attrs, message, true)
}

// Playback invokes the sink with its lifecycle lock held. Scope resolution
// must never acquire Runtime.mu, which teardown owns while closing playback.
func (o *observations) enqueue(ctx context.Context, _ string, attrs playback.FFmpegLogAttrs, message string, event bool) {
	work, _ := ctx.Value(workContextKey{}).(string)
	generation, _ := ctx.Value(generationContextKey{}).(uint64)
	if work == "" || generation == 0 {
		if h, ok := workerinput.Lookup(ctx, attrs.InputPath); ok {
			source := h.Descriptor()
			work, generation = source.WorkID, source.Generation
		}
	}
	if work == "" || generation == 0 || !o.runtime.workAllowed(work, generation) {
		return
	}
	message = sanitizeObservationMessage(message, attrs)
	if message == "" {
		return
	}
	record := workerwire.FFmpegObservation{WorkID: work, Generation: generation, At: time.Now().UTC(), Event: event, Message: message, ExecutionMode: safeObservationLabel(attrs.ExecutionMode), TargetVideoCodec: safeObservationLabel(attrs.TargetVideoCodec), TargetAudioCodec: safeObservationLabel(attrs.TargetAudioCodec), HWAccel: safeObservationLabel(attrs.HWAccel), SeekSeconds: attrs.SeekSeconds, StartSegmentNumber: max(0, attrs.StartSegmentNumber), RestartCount: max(0, attrs.RestartCount), LineIndex: max(0, attrs.LineIndex), DroppedLines: max(0, attrs.DroppedLines)}
	if math.IsNaN(record.SeekSeconds) || math.IsInf(record.SeekSeconds, 0) || record.SeekSeconds < 0 {
		record.SeekSeconds = 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.logs) >= maxObservationLogs {
		o.dropped++
		return
	}
	o.sequence++
	o.logs = append(o.logs, queuedLog{sequence: o.sequence, record: record})
}

var observationLabel = regexp.MustCompile(`^[A-Za-z0-9_.+-]{1,32}$`)

func safeObservationLabel(value string) string {
	if !observationLabel.MatchString(value) {
		return ""
	}
	return value
}
func sanitizeObservationMessage(message string, attrs playback.FFmpegLogAttrs) string {
	for _, path := range []string{attrs.InputPath, attrs.OutputDir} {
		if path != "" {
			message = strings.ReplaceAll(message, path, "[location]")
		}
	}
	return workerwire.SanitizeLogMessage(message)
}

// batch retains its log prefix until the signed report is acknowledged. New
// logs can arrive concurrently without an acknowledgement consuming them.
func (o *observations) batch() *workerwire.ObservationReport {
	report := &workerwire.ObservationReport{}
	o.mu.Lock()
	o.pending = 0
	for _, entry := range o.logs {
		if len(report.Logs) >= maxObservationBatchLogs {
			break
		}
		o.pending = entry.sequence
		if !o.runtime.workAllowed(entry.record.WorkID, entry.record.Generation) {
			continue
		}
		report.Logs = append(report.Logs, entry.record)
	}
	report.DroppedLogs = o.dropped
	o.mu.Unlock()
	snapshot := o.registry.Sweep()
	available := make(map[string]workerwire.ObservationScope)
	for _, session := range o.runtime.tracker.scopedSnapshot() {
		if o.runtime.workAllowed(session.WorkID, session.Generation) {
			available[session.Info.SessionID] = workerwire.ObservationScope{WorkID: session.WorkID, Generation: session.Generation, SessionID: session.Info.SessionID}
		}
	}
	o.runtime.mu.Lock()
	tokens := make([]tokenEntry, 0, len(o.runtime.tokens))
	for _, entry := range o.runtime.tokens {
		tokens = append(tokens, entry)
	}
	o.runtime.mu.Unlock()
	for _, entry := range tokens {
		if entry.claims != nil && entry.claims.SessionID != "" && time.Now().UTC().Before(entry.expires) && o.runtime.workAllowed(entry.workID, entry.generation) {
			available[entry.claims.SessionID] = workerwire.ObservationScope{WorkID: entry.workID, Generation: entry.generation, SessionID: entry.claims.SessionID}
		}
	}
	report.Snapshot = snapshot
	report.Snapshot.Sessions = nil
	report.Snapshot.Transfers = nil
	for _, view := range snapshot.Sessions {
		scope, ok := available[view.SessionID]
		if !ok || len(report.Snapshot.Sessions) >= maxObservationSessions {
			report.Snapshot.Truncated = true
			report.Snapshot.UnattributedObservations += view.RequestCount
			report.Snapshot.UnattributedBytes += view.BytesAccepted
			continue
		}
		view.Subject = streamtelemetry.Subject{}
		view.ProfileID = ""
		view.MediaFileID = 0
		view.MediaFileIDs = nil
		view.PlayMethod = ""
		view.PlayMethods = nil
		view.ViewerIPs = nil
		view.DeviceIDs = nil
		view.ClientVariants = nil
		view.UserAgents = nil
		view.IdentityConflicts = nil
		report.Snapshot.Sessions = append(report.Snapshot.Sessions, view)
		report.Scopes = append(report.Scopes, scope)
	}
	// Transfers without an exact execution scope cannot be attributed to a work
	// based only on the shared route pattern. Preserve their completeness cost.
	for _, transfer := range snapshot.Transfers {
		report.Snapshot.Truncated = true
		report.Snapshot.UnattributedObservations += transfer.RequestCount
		report.Snapshot.UnattributedBytes += transfer.BytesAccepted
	}
	return report
}

func (o *observations) ack() {
	o.mu.Lock()
	defer o.mu.Unlock()
	count := 0
	for count < len(o.logs) && o.logs[count].sequence <= o.pending {
		count++
	}
	copy(o.logs, o.logs[count:])
	clear(o.logs[len(o.logs)-count:])
	o.logs = o.logs[:len(o.logs)-count]
	o.pending = 0
}
