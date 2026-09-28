package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/danielgtaylor/huma/v2"
)

const diagnosticsChunkBinaryFormat = "binary"

// DiagnosticsChunksService owns local spool sessions shared with the bridge.
// Completion has no durable receipt; another replica cannot resume the session.
type DiagnosticsChunksService interface {
	InitDiagnosticChunks(context.Context, int, handlers.DiagnosticsChunkInitRequest) (handlers.DiagnosticsChunkInitResponse, error)
	PutDiagnosticChunk(http.ResponseWriter, *http.Request, int, string, int) (handlers.DiagnosticsChunkStateResponse, error)
	CompleteDiagnosticChunks(context.Context, int, string, *string) (diagnostics.IngestResult, error)
	AbortDiagnosticChunks(int, string)
	ExtendUploadDeadlines(http.ResponseWriter, *http.Request)
}

type DiagnosticsChunkInitInput struct {
	Body struct {
		Manifest    json.RawMessage `json:"manifest" doc:"Captured manifest object; full validation occurs at completion."`
		BundleBytes int64           `json:"bundle_bytes" minimum:"1" maximum:"268435456"`
	}
}
type DiagnosticsChunkInitBody struct {
	UploadID    string `json:"upload_id"`
	ChunkBytes  int64  `json:"chunk_bytes"`
	TotalChunks int    `json:"total_chunks"`
	ExpiresAt   string `json:"expires_at" format:"date-time"`
}
type DiagnosticsChunkInitOutput struct{ Body DiagnosticsChunkInitBody }
type DiagnosticsChunkPutOutput struct {
	Body DiagnosticsChunkStateResponse
}
type DiagnosticsChunkPath struct {
	UploadID string `path:"upload_id"`
}
type DiagnosticsChunkPutInput struct {
	DiagnosticsChunkPath
	ChunkIndex int `path:"chunk_index" minimum:"0"`
	requestCapture
}

type DiagnosticsChunkCompleteInput struct {
	DiagnosticsChunkPath
	DiagnosticsUploadInput
}

func diagnosticsChunkAccount(reg *Registry, ctx context.Context) (int, error) {
	user, err := diagnosticsAccount(ctx)
	if err != nil {
		return 0, err
	}
	if reg.deps.DiagnosticsChunks == nil {
		return 0, unavailable("diagnostics")
	}
	return user, nil
}

// Expired process-local sessions use the same absence response as another
// replica or restart. Do not use the catalog's device-login-specific 410 type.
func diagnosticsChunkProblem(err error) error {
	if failure, ok := errors.AsType[*handlers.DiagnosticsUploadFailure](err); ok && failure.Status == http.StatusGone {
		return NewProblem(TypeNotFound, "Diagnostics upload session is no longer available.")
	}
	return diagnosticsIngressProblem(err)
}

func registerDiagnosticsChunks(reg *Registry) {
	init := Operation{Operation: humaOp(http.MethodPost, Prefix+"/diagnostics/reports/uploads", "createDiagnosticsUpload", "diagnostics", "Create a process-local diagnostics upload session, replacing this account's previous session."), Class: ClassAuthenticated, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	init.DefaultStatus = http.StatusCreated
	init.MaxBodyBytes = diagnostics.MaxManifestBytes + (128 << 10)
	init.Errors = []int{400, 403, 409, 413, 500, 503}
	Register(reg, init, func(ctx context.Context, in *DiagnosticsChunkInitInput) (*DiagnosticsChunkInitOutput, error) {
		user, err := diagnosticsChunkAccount(reg, ctx)
		if err != nil {
			return nil, err
		}
		result, err := reg.deps.DiagnosticsChunks.InitDiagnosticChunks(ctx, user, handlers.DiagnosticsChunkInitRequest{Manifest: in.Body.Manifest, BundleBytes: in.Body.BundleBytes})
		if err != nil {
			return nil, diagnosticsChunkProblem(err)
		}
		expires, err := time.Parse(time.RFC3339, result.ExpiresAt)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Diagnostics upload returned an invalid expiry.")
		}
		return &DiagnosticsChunkInitOutput{Body: DiagnosticsChunkInitBody{UploadID: result.UploadID, ChunkBytes: result.ChunkBytes, TotalChunks: result.TotalChunks, ExpiresAt: expires.UTC().Format("2006-01-02T15:04:05.000Z")}}, nil
	})

	put := Operation{Operation: humaOp(http.MethodPut, Prefix+"/diagnostics/reports/uploads/{upload_id}/chunks/{chunk_index}", "putDiagnosticsUploadChunk", "diagnostics", "Store one immutable chunk in a process-local upload session."), Class: ClassAuthenticated, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	put.MaxBodyBytes = diagnostics.UploadChunkBytes
	put.Errors = []int{400, 403, 404, 408, 409, 413, 415, 500}
	Register(reg, put, func(ctx context.Context, in *DiagnosticsChunkPutInput) (*DiagnosticsChunkPutOutput, error) {
		user, err := diagnosticsChunkAccount(reg, ctx)
		if err != nil {
			return nil, err
		}
		reg.deps.DiagnosticsChunks.ExtendUploadDeadlines(in.writer, in.request)
		result, err := reg.deps.DiagnosticsChunks.PutDiagnosticChunk(in.writer, in.request, user, in.UploadID, in.ChunkIndex)
		if err != nil {
			return nil, diagnosticsChunkProblem(err)
		}
		return &DiagnosticsChunkPutOutput{Body: DiagnosticsChunkStateResponse(result)}, nil
	})
	// The media gate enforces this explicit binary contract and cap. As with
	// multipart ingress, attaching it after registration avoids Huma pre-reading
	// before session ownership is checked by the shared service.
	registeredOperation(reg.api.OpenAPI(), put).RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
		mediaTypeBinary: {Schema: &huma.Schema{Type: huma.TypeString, Format: diagnosticsChunkBinaryFormat}},
	}}

	complete := Operation{Operation: humaOp(http.MethodPost, Prefix+"/diagnostics/reports/uploads/{upload_id}/complete", "completeDiagnosticsUpload", "diagnostics", "Consume a local upload session and ingest its diagnostics report without durable receipt replay."), Class: ClassAuthenticated, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	complete.DefaultStatus = http.StatusCreated
	complete.Errors = []int{400, 403, 404, 409, 413, 429, 500, 503}
	Register(reg, complete, func(ctx context.Context, in *DiagnosticsChunkCompleteInput) (*DiagnosticsUploadOutput, error) {
		user, err := diagnosticsChunkAccount(reg, ctx)
		if err != nil {
			return nil, err
		}
		reg.deps.DiagnosticsChunks.ExtendUploadDeadlines(in.writer, in.request)
		var profile *string
		if value := strings.TrimSpace(in.ProfileID); value != "" {
			profile = new(value)
		}
		result, err := reg.deps.DiagnosticsChunks.CompleteDiagnosticChunks(ctx, user, in.UploadID, profile)
		if err != nil {
			return nil, diagnosticsChunkProblem(err)
		}
		return &DiagnosticsUploadOutput{Body: result}, nil
	})
	abort := Operation{Operation: humaOp(http.MethodDelete, Prefix+"/diagnostics/reports/uploads/{upload_id}", "abortDiagnosticsUpload", "diagnostics", "Best-effort abort of this account's local diagnostics upload."), Class: ClassAuthenticated, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	abort.DefaultStatus = http.StatusNoContent
	abort.Errors = []int{403}
	Register(reg, abort, func(ctx context.Context, in *DiagnosticsChunkPath) (*struct{}, error) {
		user, err := diagnosticsChunkAccount(reg, ctx)
		if err != nil {
			return nil, err
		}
		reg.deps.DiagnosticsChunks.AbortDiagnosticChunks(user, in.UploadID)
		return nil, nil
	})
	for _, op := range []Operation{init, complete} {
		for _, status := range []string{"429", "503"} {
			if response := registeredOperation(reg.api.OpenAPI(), op).Responses[status]; response != nil {
				if response.Headers == nil {
					response.Headers = map[string]*huma.Header{}
				}
				response.Headers["Retry-After"] = &huma.Header{Schema: &huma.Schema{Type: huma.TypeString}, Description: "Delay after explicit quota/busy refusal; uncertain completion is not safe to replay."}
			}
		}
	}
}

// DiagnosticsChunkStateResponse is the native transport projection, independent of handler views.
type DiagnosticsChunkStateResponse struct {
	ReceivedChunks int `json:"received_chunks"`
	TotalChunks    int `json:"total_chunks"`
}
