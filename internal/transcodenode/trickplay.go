package transcodenode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
	"github.com/Silo-Server/silo-server/internal/trickplay"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
)

// maxTrickplayRequestBytes bounds a trickplay request body: a sheets request
// of at most mediasample's sample limit.
const maxTrickplayRequestBytes = 1 << 20

// trickplayInvalidRequest is the reason for a body that is not a sheets
// request; trickplayAutoAccel is the accelerator an unset setting means.
const (
	trickplayInvalidRequest = "invalid_request"
	trickplayAutoAccel      = "auto"
)

// trickplayWork counts the node's trickplay runs against
// playback.trickplay_node_capacity, and holds the hardware they decode on.
type trickplayWork struct {
	mu      sync.Mutex
	running int
	// resolving pairs each run's Set with its Backend, so concurrent runs
	// across a settings reload never resolve another run's accelerator.
	resolving sync.Mutex
	once      sync.Once
	hardware  *mediasample.HardwareResolver
}

// start admits a run when fewer than capacity are running. It never waits: a
// full node refuses, and the API server moves the run to another node or its
// own. A lowered capacity lets current runs finish and admits none until the
// count is under it.
func (t *trickplayWork) start(capacity int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running >= max(1, capacity) {
		return false
	}
	t.running++
	return true
}

func (t *trickplayWork) done() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running--
}

// backend resolves the accelerator for accel and device, the node's current
// playback.hw_accel and playback.hw_device.
func (t *trickplayWork) backend(ctx context.Context, ffmpeg, accel, device string) (string, string) {
	hardware := t.resolver()
	t.resolving.Lock()
	defer t.resolving.Unlock()
	hardware.Set(accel, device)
	return hardware.Backend(ctx, ffmpeg)
}

func (t *trickplayWork) resolver() *mediasample.HardwareResolver {
	t.once.Do(func() { t.hardware = mediasample.NewHardwareResolver("", "") })
	return t.hardware
}

// handleTrickplayExtract runs one mediasample Sheets request for the API
// server and returns its sheets. The node takes up to
// playback.trickplay_node_capacity such runs at once and answers 503
// node_busy past that, runs them at idle priority, and decodes on its own
// hardware: hardware attempts it cannot run are dropped.
func (s *Server) handleTrickplayExtract(w http.ResponseWriter, r *http.Request) {
	defer s.trackInFlight()()
	var req mediasample.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrickplayRequestBytes)).Decode(&req); err != nil {
		writeTrickplayError(w, http.StatusBadRequest, trickplay.ExtractError{Reason: trickplayInvalidRequest, Message: "invalid request body"})
		return
	}
	if req.Sheets == nil || req.Samples == nil || req.Validate() != nil {
		writeTrickplayError(w, http.StatusBadRequest, trickplay.ExtractError{Reason: trickplayInvalidRequest, Message: "a sheets request of samples is required"})
		return
	}
	if s.draining() {
		writeTrickplayError(w, http.StatusServiceUnavailable, trickplay.ExtractError{Reason: trickplay.NodeUnavailableReason, Message: errNodeDraining})
		return
	}
	cfg := s.watcher.Config()
	if cfg == nil {
		writeTrickplayError(w, http.StatusServiceUnavailable, trickplay.ExtractError{Reason: trickplay.NodeUnavailableReason, Message: "node not configured"})
		return
	}
	if !s.trickplay.start(cfg.Playback.TrickplayNodeCapacity) {
		writeTrickplayError(w, http.StatusServiceUnavailable, trickplay.ExtractError{Reason: trickplay.NodeBusyReason, Message: "node is at its trickplay run capacity"})
		return
	}
	defer s.trickplay.done()
	// A run is a job, so the node's own job cap refuses it as busy, which a
	// core of any version tries elsewhere or later.
	slot, admitted := s.admitJob(nodepool.JobKindTrickplay)
	if !admitted {
		setRefusal(w, nodepool.RefusalAtCapacity)
		writeTrickplayError(w, http.StatusServiceUnavailable, trickplay.ExtractError{Reason: trickplay.NodeBusyReason, Message: errNodeAtCapacity})
		return
	}
	defer slot.release()
	// The run reads the source the core grants, never the path the request
	// names, and ends if the core withdraws it.
	source, err := s.openSource(r.Context(), req.SourceGrant)
	if err != nil {
		// Only a job without a grant is the request's fault; a refused grant or
		// a mount that does not match sends the run elsewhere.
		status, message := sourceFailure(r.Context(), err)
		reason := trickplay.NodeUnavailableReason
		if errors.Is(err, nodesource.ErrNoGrant) {
			reason = trickplayInvalidRequest
		}
		writeTrickplayError(w, status, trickplay.ExtractError{Reason: reason, Message: message})
		return
	}
	defer func() { _ = source.Close() }()
	ctx, cancel := sourceContext(r.Context(), source)
	defer cancel()
	req.Input = source.Input()
	s.countJob(slot, nodepool.JobKindTrickplay)
	defer s.endJob(nodepool.JobKindTrickplay)
	// Backend resolution can smoke-encode after the shared probe cache is
	// invalidated. Reserve admission before it starts and keep it through
	// extraction, including a fallback to software.
	if !s.gpu.beginWork() {
		writeTrickplayError(w, http.StatusServiceUnavailable, trickplay.ExtractError{Reason: trickplay.NodeUnavailableReason, Message: "node is re-probing its hardware; retry shortly"})
		return
	}
	defer s.gpu.endWork()

	ffmpeg := playback.ResolveFFmpegPath(cfg.Playback.FFmpegPath)
	accel := strings.TrimSpace(cfg.Playback.HWAccel)
	if accel == "" {
		accel = trickplayAutoAccel
	}
	resolved, device := s.trickplay.backend(ctx, ffmpeg, accel, cfg.Playback.HWDevice)
	runner := mediasample.Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Trickplay}
	if mediasample.SupportsHardwareSheets(resolved) {
		runner.HWAccel, runner.HWDevice = resolved, device
	} else {
		req.Attempts = softwareAttempts(req.Attempts)
	}
	req.Background = true
	result, err := runner.Run(ctx, req)
	if err != nil {
		if ctx.Err() == nil && trickplay.InputUnreadable(err) {
			// This node's ffmpeg cannot open a file that passed the path
			// check, as with a permission problem here: no fault of the
			// file's content, so the API server does not count a failure.
			writeTrickplayError(w, http.StatusUnprocessableEntity, trickplay.ExtractError{Reason: trickplay.InputUnreadableReason, Message: err.Error()})
			return
		}
		cause := mediasample.Classify(err)
		writeTrickplayError(w, http.StatusUnprocessableEntity, trickplay.ExtractError{Reason: string(cause), Permanent: cause.Permanent(), Message: err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// softwareAttempts drops the hardware attempts of a plan, keeping at least
// one software attempt.
func softwareAttempts(attempts []mediasample.Attempt) []mediasample.Attempt {
	var software []mediasample.Attempt
	for _, attempt := range attempts {
		if !attempt.Hardware {
			software = append(software, attempt)
		}
	}
	if len(software) == 0 && len(attempts) > 0 {
		software = append(software, mediasample.Attempt{TimeoutSeconds: attempts[len(attempts)-1].TimeoutSeconds})
	}
	return software
}

func writeTrickplayError(w http.ResponseWriter, status int, failure trickplay.ExtractError) {
	// FFmpeg's log can name a relayed input, a capability on this host.
	failure.Message = fspath.RedactRelayed(failure.Message)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(failure)
}

// ProtocolTrickplayExtraction describes the JSON request/JSON sheets boundary.
func ProtocolTrickplayExtraction(schemas huma.Registry) workerprotocol.Operation {
	const (
		listener     = "transcode_node"
		bearerClass  = "node_bearer"
		nonRetryable = "non_retryable"
		jsonMedia    = "application/json"
		textMedia    = "text/plain"
	)
	request := schemas.Schema(reflect.TypeFor[mediasample.Request](), true, "")
	result := schemas.Schema(reflect.TypeFor[mediasample.Result](), true, "")
	failure := schemas.Schema(reflect.TypeFor[trickplay.ExtractError](), true, "")
	text := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString}}
	problem := &huma.MediaType{Schema: failure}
	return withJobCapRefusal(workerprotocol.Operation{
		Listener: listener, Method: http.MethodPost, Path: "/trickplay/extract",
		Handler: "(*internal/transcodenode.Server).handleTrickplayExtract", AuthClass: bearerClass,
		Description: "Make trickplay sprite sheets for a mediasample Sheets request of samples on the granted source, up to the node's configured number of runs at once, at idle priority, on the node's own hardware. No durable replay receipt; the API server keeps the lease and retries elsewhere. source_grant is required: the node exchanges it at the core (POST /api/internal/nodes/self/sources/open) and reads only the path the core approves, never the input path the request names, for the job's life; a source the core withdraws ends the job. A missing or refused grant returns 400, a node mount that does not show the approved revision 409, a core the node cannot reach 503.",
		RetrySafety: nonRetryable,
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{jsonMedia: {Schema: request}}},
		Responses: map[string]*huma.Response{
			"200": {Description: "The run's sheets", Content: map[string]*huma.MediaType{jsonMedia: {Schema: result}}},
			"400": {Description: "Invalid request, or a missing or refused source grant", Content: map[string]*huma.MediaType{jsonMedia: problem, textMedia: text}},
			"409": {Description: mountMismatchDescription, Content: map[string]*huma.MediaType{jsonMedia: problem}},
			"401": {Description: http.StatusText(http.StatusUnauthorized), Content: map[string]*huma.MediaType{textMedia: text}},
			"422": {Description: "Sampling failed; permanent names a cause in the file itself", Content: map[string]*huma.MediaType{jsonMedia: problem}},
			"503": {Description: "Node at its trickplay run capacity or its job cap (reason node_busy), re-probing, unconfigured, or unable to reach the core's source authority", Content: map[string]*huma.MediaType{jsonMedia: problem, textMedia: text}},
		},
	}, "")
}
