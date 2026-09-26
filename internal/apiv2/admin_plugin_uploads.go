package apiv2

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/uploads"
	"github.com/danielgtaylor/huma/v2"
)

// AdminPluginUploadService is the slice of *handlers.PluginHandler the plugin
// upload operations use. Chunked sessions are process-local: another replica
// or a restart does not know them.
type AdminPluginUploadService interface {
	InstallAdminPluginUpload(context.Context, *http.Request) (handlers.PluginInstallationView, error)
	CreateAdminPluginUpload(context.Context, handlers.PluginChunkedUploadCreateInput) (uploads.SessionInfo, error)
	PutAdminPluginUploadChunk(context.Context, string, int, io.Reader, int64) (uploads.SessionInfo, error)
	CompleteAdminPluginUpload(context.Context, string) (handlers.PluginInstallationView, error)
	CancelAdminPluginUpload(context.Context, string) error
}

const (
	// maxPluginUploadBytes mirrors the bridge cap on one plugin archive.
	maxPluginUploadBytes int64 = 256 << 20
	// maxPluginUploadChunkBytes mirrors the bridge chunk cap.
	maxPluginUploadChunkBytes int64 = 1 << 20
	pluginUploadFormOverhead  int64 = 1 << 20
)

// AdminPluginUploadForm is the direct upload: one plugin archive part.
type AdminPluginUploadForm struct {
	Archive string `json:"archive" format:"binary" doc:"Plugin zip archive, or a plugin binary whose manifest the server reads by executing it; at most 256 MiB."`
}

// AdminPluginUploadInput captures the stream after the gates. No Body/RawBody
// keeps Huma from spooling the multipart request before the seam reads it.
type AdminPluginUploadInput struct {
	requestCapture
}

type AdminPluginChunkedUploadCreate struct {
	Filename  string `json:"filename" minLength:"1" maxLength:"512"`
	SizeBytes int64  `json:"size_bytes" minimum:"1" maximum:"268435456"`
	ChunkSize int64  `json:"chunk_size,omitempty" minimum:"1" maximum:"1048576" doc:"Omitted picks the server default (512 KiB)"`
}
type AdminPluginChunkedUploadCreateInput struct {
	RawBody []byte
	Body    AdminPluginChunkedUploadCreate
}
type AdminPluginUploadSession struct {
	UploadID       string  `json:"upload_id"`
	Filename       string  `json:"filename"`
	SizeBytes      int64   `json:"size_bytes"`
	ChunkSize      int64   `json:"chunk_size"`
	TotalChunks    int     `json:"total_chunks"`
	ReceivedChunks int     `json:"received_chunks"`
	ReceivedBytes  int64   `json:"received_bytes"`
	Complete       bool    `json:"complete"`
	ExpiresAt      Instant `json:"expires_at" doc:"Inactivity deadline on this replica"`
}
type AdminPluginUploadSessionOutput struct{ Body AdminPluginUploadSession }
type AdminPluginUploadPath struct {
	UploadID string `path:"upload_id" minLength:"1" maxLength:"128"`
}
type AdminPluginUploadChunkInput struct {
	AdminPluginUploadPath
	ChunkIndex int `path:"chunk_index" minimum:"0"`
	requestCapture
}

func adminPluginUploadSessionOf(s uploads.SessionInfo) AdminPluginUploadSession {
	return AdminPluginUploadSession{UploadID: s.ID, Filename: s.Filename, SizeBytes: s.SizeBytes, ChunkSize: s.ChunkSize, TotalChunks: s.TotalChunks, ReceivedChunks: s.ReceivedChunks, ReceivedBytes: s.ReceivedBytes, Complete: s.Complete, ExpiresAt: NewInstant(s.ExpiresAt)}
}

// adminPluginUploadProblem maps the upload manager's typed errors. An expired
// or unknown session answers not_found, as another replica or a restart would.
func adminPluginUploadProblem(err error) error {
	var maxBytesErr *http.MaxBytesError
	switch {
	case errors.Is(err, handlers.ErrPluginUploadMissingArchive):
		return validationProblem(locationBody+".archive", codeRequired, "A plugin archive file is required.")
	case errors.As(err, &maxBytesErr), errors.Is(err, uploads.ErrTooLarge):
		return NewProblem(TypePayloadTooLarge, "The upload exceeds the maximum allowed size.")
	case errors.Is(err, uploads.ErrNotFound), errors.Is(err, uploads.ErrExpired):
		return NewProblem(TypeNotFound, "Plugin upload session is no longer available on this server.")
	case errors.Is(err, uploads.ErrIncomplete):
		return NewProblem(TypeConflict, "The upload session has not received every chunk.")
	case errors.Is(err, uploads.ErrAlreadyCompleted):
		return NewProblem(TypeConflict, "The upload session is already complete.")
	case errors.Is(err, uploads.ErrChunkBusy):
		return NewProblem(TypeConflict, "This chunk is already being uploaded.")
	case errors.Is(err, uploads.ErrInvalidChunk), errors.Is(err, uploads.ErrInvalidRequest):
		return validationProblem(locationBody, codeInvalid, err.Error())
	}
	return adminPluginMutationProblem(err)
}

func registerAdminPluginUploads(reg *Registry) {
	op := func(method, path, id, summary string, safety RetrySafety) Operation {
		return Operation{Operation: humaOp(method, Prefix+"/admin/plugins/uploads"+path, id, "admin-plugins", summary), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, RetrySafety: safety}
	}
	direct := op(http.MethodPost, "", "uploadAdminPluginInstallation", "Install a plugin from one uploaded archive. A zip archive is installed from its manifest; any other file is treated as a plugin binary whose manifest the server obtains by executing it. An installation with the same plugin_id is replaced; there is no replay identity, so a delayed retry can replace a newer installation. Never automatically retry.", RetrySafetyNonRetryable)
	direct.DefaultStatus = http.StatusCreated
	direct.MaxBodyBytes = maxPluginUploadBytes + pluginUploadFormOverhead
	direct.Errors = append(direct.Errors, http.StatusRequestTimeout, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType)
	Register(reg, direct, func(ctx context.Context, in *AdminPluginUploadInput) (*AdminPluginInstallationOutput, error) {
		if reg.deps.AdminPluginUploads == nil {
			return nil, unavailable("plugin uploads")
		}
		view, err := reg.deps.AdminPluginUploads.InstallAdminPluginUpload(ctx, in.request)
		if err != nil {
			return nil, adminPluginUploadProblem(err)
		}
		out, err := adminPluginInstallationOf(view)
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &AdminPluginInstallationOutput{Body: out}, nil
	})
	// Attached after registration so the media gate enforces multipart and the
	// cap while Huma's decoder never buffers the archive.
	registeredOperation(reg.api.OpenAPI(), direct).RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
		mediaTypeMultipart: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[AdminPluginUploadForm](), true, ""), Encoding: map[string]*huma.Encoding{"archive": {ContentType: "application/zip, application/octet-stream"}}},
	}}

	create := op(http.MethodPost, "/chunked", "createAdminPluginUpload", "Open a process-local chunked upload session for one plugin archive; every call creates a new session on this replica. Sessions expire after inactivity and are unknown to other replicas. Never automatically retry an uncertain create.", RetrySafetyNonRetryable)
	create.DefaultStatus = http.StatusCreated
	create.MaxBodyBytes = 64 << 10
	create.Errors = append(create.Errors, http.StatusRequestEntityTooLarge)
	Register(reg, create, func(ctx context.Context, in *AdminPluginChunkedUploadCreateInput) (*AdminPluginUploadSessionOutput, error) {
		if reg.deps.AdminPluginUploads == nil {
			return nil, unavailable("plugin uploads")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		session, err := reg.deps.AdminPluginUploads.CreateAdminPluginUpload(ctx, handlers.PluginChunkedUploadCreateInput{Filename: in.Body.Filename, SizeBytes: in.Body.SizeBytes, ChunkSize: in.Body.ChunkSize})
		if err != nil {
			return nil, adminPluginUploadProblem(err)
		}
		return &AdminPluginUploadSessionOutput{Body: adminPluginUploadSessionOf(session)}, nil
	})

	put := op(http.MethodPut, "/chunked/{upload_id}/chunks/{chunk_index}", "putAdminPluginUploadChunk", "Store one chunk of a process-local session; the body length must equal the session chunk size for that index. A chunk already received is accepted without rewriting, so repeating the same bytes converges.", RetrySafetyNaturalIdempotent)
	put.MaxBodyBytes = maxPluginUploadChunkBytes
	put.Errors = append(put.Errors, http.StatusNotFound, http.StatusRequestTimeout, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType)
	Register(reg, put, func(ctx context.Context, in *AdminPluginUploadChunkInput) (*AdminPluginUploadSessionOutput, error) {
		if reg.deps.AdminPluginUploads == nil {
			return nil, unavailable("plugin uploads")
		}
		session, err := reg.deps.AdminPluginUploads.PutAdminPluginUploadChunk(ctx, in.UploadID, in.ChunkIndex, in.request.Body, in.request.ContentLength)
		if err != nil {
			return nil, adminPluginUploadProblem(err)
		}
		return &AdminPluginUploadSessionOutput{Body: adminPluginUploadSessionOf(session)}, nil
	})
	registeredOperation(reg.api.OpenAPI(), put).RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
		mediaTypeBinary: {Schema: &huma.Schema{Type: huma.TypeString, Format: diagnosticsChunkBinaryFormat}},
	}}

	complete := op(http.MethodPost, "/chunked/{upload_id}/complete", "completeAdminPluginUpload", "Consume a fully received session on this replica and install the assembled archive as the direct upload does. The session is removed before the install runs: a repeat finds no session and a lost response is not a receipt. Never automatically retry.", RetrySafetyNonRetryable)
	complete.DefaultStatus = http.StatusCreated
	complete.Errors = append(complete.Errors, http.StatusNotFound, http.StatusConflict)
	Register(reg, complete, func(ctx context.Context, in *AdminPluginUploadPath) (*AdminPluginInstallationOutput, error) {
		if reg.deps.AdminPluginUploads == nil {
			return nil, unavailable("plugin uploads")
		}
		view, err := reg.deps.AdminPluginUploads.CompleteAdminPluginUpload(ctx, in.UploadID)
		if err != nil {
			return nil, adminPluginUploadProblem(err)
		}
		out, err := adminPluginInstallationOf(view)
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &AdminPluginInstallationOutput{Body: out}, nil
	})

	cancel := op(http.MethodDelete, "/chunked/{upload_id}", "cancelAdminPluginUpload", "Discard a process-local upload session and its spooled bytes; an absent or expired session is already gone and answers 204.", RetrySafetyNaturalIdempotent)
	cancel.DefaultStatus = http.StatusNoContent
	Register(reg, cancel, func(ctx context.Context, in *AdminPluginUploadPath) (*struct{}, error) {
		if reg.deps.AdminPluginUploads == nil {
			return nil, unavailable("plugin uploads")
		}
		if err := reg.deps.AdminPluginUploads.CancelAdminPluginUpload(ctx, in.UploadID); err != nil {
			return nil, adminPluginUploadProblem(err)
		}
		return nil, nil
	})
}
