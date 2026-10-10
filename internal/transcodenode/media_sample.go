package transcodenode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// handleMediaSample runs one media sampling request with this node's ffmpeg,
// so analysis of large files reads them on a transcode node instead of the
// API server. Only software attempts are accepted: hardware decode would
// need this node's resolved accelerator and the GPU admission gate, and no
// remote caller needs it yet.
func (s *Server) handleMediaSample(w http.ResponseWriter, r *http.Request) {
	var req mediasample.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, mediasample.MaxRemoteRequestBytes)).Decode(&req); err != nil {
		writeMediaSampleError(w, http.StatusBadRequest, mediasample.ReasonInvalidRequest, "invalid request body")
		return
	}
	if err := req.Validate(); err != nil {
		writeMediaSampleError(w, http.StatusBadRequest, mediasample.ReasonInvalidRequest, err.Error())
		return
	}
	for _, attempt := range req.Attempts {
		if attempt.Hardware {
			writeMediaSampleError(w, http.StatusBadRequest, mediasample.ReasonInvalidRequest, "remote media samples run in software only")
			return
		}
	}
	cfg := s.watcher.Config()
	if cfg == nil {
		writeMediaSampleError(w, http.StatusServiceUnavailable, mediasample.ReasonNodeUnavailable, "node not configured")
		return
	}
	if !s.requireApprovedInputPath(w, r, req.Input) {
		return
	}
	// API servers each reserve nodes on their own, so the node enforces the
	// capacity itself. A request over it waits for a slot, separately from
	// its run's timeouts, and is refused as unavailable when none frees up.
	limiter := s.mediaSampleLimiter(cfg.Playback.SubtitleSyncNodeCapacity)
	wait := mediasample.MaxRemoteAdmissionWait
	if s.mediaSampleWait > 0 {
		wait = s.mediaSampleWait
	}
	admitCtx, cancelAdmit := context.WithTimeout(r.Context(), wait)
	release, err := limiter.Acquire(admitCtx)
	cancelAdmit()
	if err != nil {
		writeMediaSampleError(w, http.StatusServiceUnavailable, mediasample.ReasonNodeUnavailable, "node is at sampling capacity")
		return
	}
	defer release()
	runner := mediasample.Runner{FFmpegPath: cfg.Playback.FFmpegPath, Workload: processmetrics.Analysis}
	result, err := runner.Run(r.Context(), req)
	if err != nil {
		var runErr *mediasample.Error
		if !errors.As(err, &runErr) {
			writeMediaSampleError(w, http.StatusBadRequest, mediasample.ReasonInvalidRequest, err.Error())
			return
		}
		writeMediaSampleError(w, http.StatusUnprocessableEntity, mediasample.Classify(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// mediaSampleLimiter returns the node's sampling limiter, resized to the
// current configured capacity.
func (s *Server) mediaSampleLimiter(capacity int) *mediasample.Limiter {
	s.mediaSamplesOnce.Do(func() { s.mediaSamples = mediasample.NewLimiter(capacity) })
	s.mediaSamples.Resize(capacity)
	return s.mediaSamples
}

func writeMediaSampleError(w http.ResponseWriter, status int, reason mediasample.Reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(mediasample.RemoteFailure{Reason: reason, Error: message})
}

// ProtocolMediaSample describes the media sampling route.
func ProtocolMediaSample(schemas huma.Registry) workerprotocol.Operation {
	const (
		listener    = "transcode_node"
		bearerClass = "node_bearer"
		jsonMedia   = "application/json"
		textMedia   = "text/plain"
	)
	request := schemas.Schema(reflect.TypeFor[mediasample.Request](), true, "")
	result := schemas.Schema(reflect.TypeFor[mediasample.Result](), true, "")
	failure := &huma.MediaType{Schema: schemas.Schema(reflect.TypeFor[mediasample.RemoteFailure](), true, "")}
	text := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString}}
	return workerprotocol.Operation{
		Listener: listener, Method: http.MethodPost, Path: mediasample.RemotePath,
		Handler: "(*internal/transcodenode.Server).handleMediaSample", AuthClass: bearerClass,
		Description: "Run one software media sampling request on an approved input path with the node's ffmpeg and return its result. Read-only analysis; a repeated request decodes the file again.",
		RetrySafety: "natural_idempotent",
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{jsonMedia: {Schema: request}}},
		Responses: map[string]*huma.Response{
			"200": {Description: "Sampling result", Content: map[string]*huma.MediaType{jsonMedia: {Schema: result}}},
			"400": {Description: "Invalid request, hardware attempt, or unapproved input path", Content: map[string]*huma.MediaType{jsonMedia: failure, textMedia: text}},
			"401": {Description: "Unauthorized", Content: map[string]*huma.MediaType{textMedia: text}},
			"422": {Description: "Sampling failed; reason is the run's classified cause", Content: map[string]*huma.MediaType{jsonMedia: failure}},
			"503": {Description: "Node or input authority unavailable", Content: map[string]*huma.MediaType{jsonMedia: failure, textMedia: text}},
		},
	}
}
