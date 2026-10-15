package transcodenode

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/mediasample"
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

// trickplayWork is the node's one trickplay run at a time, and the hardware
// its runs decode on.
type trickplayWork struct {
	busy     sync.Mutex
	once     sync.Once
	hardware *mediasample.HardwareResolver
}

func (t *trickplayWork) resolver() *mediasample.HardwareResolver {
	t.once.Do(func() { t.hardware = mediasample.NewHardwareResolver("", "") })
	return t.hardware
}

// handleTrickplayExtract runs one mediasample Sheets request for the API
// server and returns its sheets. The node takes one such run at a time and
// answers 503 node_busy past that, runs it at idle priority, and decodes on
// its own hardware: hardware attempts it cannot run are dropped.
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
	if !s.trickplay.busy.TryLock() {
		writeTrickplayError(w, http.StatusServiceUnavailable, trickplay.ExtractError{Reason: trickplay.NodeBusyReason, Message: "node is already making trickplay sheets"})
		return
	}
	defer s.trickplay.busy.Unlock()
	s.activeJobs.Add(1)
	defer s.activeJobs.Add(-1)
	// Backend resolution can smoke-encode after the shared probe cache is
	// invalidated. Reserve admission before it starts and keep it through
	// extraction, including a fallback to software.
	if !s.gpu.beginWork() {
		writeTrickplayError(w, http.StatusServiceUnavailable, trickplay.ExtractError{Reason: trickplay.NodeUnavailableReason, Message: "node is re-probing its hardware; retry shortly"})
		return
	}
	defer s.gpu.endWork()
	input, ok := s.openInput(w, r, req.SourceGrant)
	if !ok {
		return
	}
	defer func() { _ = input.Close() }()
	req.Input = input.Input()

	ffmpeg := playback.ResolveFFmpegPath(cfg.Playback.FFmpegPath)
	if reason, err := s.relayedSamplesUnsupported(r.Context(), ffmpeg, req); err != nil {
		writeTrickplayError(w, http.StatusUnprocessableEntity, trickplay.ExtractError{Reason: string(reason), Message: err.Error()})
		return
	}
	accel := strings.TrimSpace(cfg.Playback.HWAccel)
	if accel == "" {
		accel = trickplayAutoAccel
	}
	hardware := s.trickplay.resolver()
	hardware.Set(accel, cfg.Playback.HWDevice)
	resolved, device := hardware.Backend(r.Context(), ffmpeg)
	runner := mediasample.Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Trickplay}
	if mediasample.SupportsHardwareSheets(resolved) {
		runner.HWAccel, runner.HWDevice = resolved, device
	} else {
		req.Attempts = softwareAttempts(req.Attempts)
	}
	req.Background = true
	ctx, cancel := inputContext(r.Context(), input)
	defer cancel()
	result, err := runner.Run(ctx, req)
	if err != nil {
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	failure.Message = nodesource.RedactLoopback(failure.Message)
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
	return workerprotocol.Operation{
		Listener: listener, Method: http.MethodPost, Path: "/trickplay/extract",
		Handler: "(*internal/transcodenode.Server).handleTrickplayExtract", AuthClass: bearerClass,
		Description: "Make trickplay sprite sheets for a mediasample Sheets request of samples on the source its source_grant names, opened through the core, one run at a time, at idle priority, on the node's own hardware. A relayed source (core source access) whose concat form the node's ffmpeg cannot read is refused with 422 unsupported, or 422 capabilities when the node could not list its ffmpeg's capabilities, never permanent, so the API server runs it elsewhere. No durable replay receipt; the API server keeps the lease and retries elsewhere.",
		RetrySafety: nonRetryable,
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{jsonMedia: {Schema: request}}},
		Responses: map[string]*huma.Response{
			"200": {Description: "The run's sheets", Content: map[string]*huma.MediaType{jsonMedia: {Schema: result}}},
			"400": {Description: "Invalid request, missing source grant, or a source the core refused", Content: map[string]*huma.MediaType{jsonMedia: problem, textMedia: text}},
			"401": {Description: http.StatusText(http.StatusUnauthorized), Content: map[string]*huma.MediaType{textMedia: text}},
			"409": {Description: localSourceMismatchDescription, Content: map[string]*huma.MediaType{textMedia: text}},
			"422": {Description: "Sampling failed, or refused for this node's ffmpeg (unsupported, capabilities); permanent names a cause in the file itself", Content: map[string]*huma.MediaType{jsonMedia: problem}},
			"503": {Description: "Node busy with another trickplay run, re-probing, unconfigured, or without the core's source authority", Content: map[string]*huma.MediaType{jsonMedia: problem, textMedia: text}},
		},
	}
}
