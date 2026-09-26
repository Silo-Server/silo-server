package apiv2

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/httpstream"
)

// dispositionFilenameParam is the Content-Disposition parameter naming the file
// a browser saves.
const dispositionFilenameParam = "filename"

// AdminJobArtifactService opens a completed job's artifact. It exists so a
// store that cannot presign still has a way to deliver bytes.
type AdminJobArtifactService interface {
	OpenAdminJobArtifact(context.Context, string) (handlers.AdminJobArtifactDownload, error)
}

// AdminJobArtifactCapabilities tells an administrator client what this server's
// artifact storage can do before it asks for a job. Both answers depend on the
// configured backend, not on the release, so version sniffing cannot derive
// them.
type AdminJobArtifactCapabilities struct {
	Capability
	ArtifactDownload bool `json:"artifact_download" doc:"Whether completed job artifacts can be downloaded from this server"`
	PublicLinks      bool `json:"public_links" doc:"Whether a shareable seven-day link can be minted. False when artifacts are stored locally, because only storage-side presigning produces a URL usable off this server"`
}

type AdminJobArtifactCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminJobArtifactCapabilities
}

func registerAdminJobArtifactCapabilities(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/admin/jobs/capabilities", "getAdminJobCapabilities", "admin-tasks",
			"Artifact download and public-link support for administrator jobs."),
		Class: ClassActingAdmin, ServiceBacked: true,
	}, func(context.Context, *CapabilityInput) (*AdminJobArtifactCapabilitiesOutput, error) {
		if reg.deps.AdminTaskJobs == nil {
			return nil, unavailable("admin jobs")
		}
		// Downloading needs somewhere to stream from or a bucket to presign
		// against; a server with neither reports both false rather than
		// advertising an action that cannot complete.
		publicLinks := reg.deps.AdminTaskJobs.AdminTaskJobPublicLinkSupported()
		download := publicLinks || reg.deps.AdminJobArtifacts != nil
		return &AdminJobArtifactCapabilitiesOutput{Body: AdminJobArtifactCapabilities{
			Capability:       Capability{State: StateAvailable},
			ArtifactDownload: download,
			PublicLinks:      publicLinks,
		}}, nil
	})
}

// registerAdminJobArtifactDownload serves an artifact to a signed capability
// rather than a session. The presigned S3 URL this replaces authorized itself,
// and the web UI opens the URL in a new tab with no Authorization header, so an
// administrator-gated route would answer 401. The signature is scoped to one
// job ID under its own capability domain, so an artwork URL cannot be replayed
// here and a URL for one job does not read another's artifact.
func registerAdminJobArtifactDownload(reg *Registry) {
	operation := humaOp("GET", Prefix+"/admin/jobs/{id}/artifact", "downloadAdminJobArtifact", "admin-tasks",
		"Stream a completed job's artifact through the API host. Authorized by the signed capability in the download URL, not by a session.")
	operation.Parameters = []*huma.Param{
		{Name: "id", In: paramInPath, Required: true, Schema: &huma.Schema{Type: huma.TypeString, MinLength: new(1), MaxLength: new(128)}},
		{Name: "exp", In: artworkParamQuery, Required: true, Schema: &huma.Schema{Type: artworkIntegerType, Format: playbackIntegerFormat}},
		{Name: "sig", In: artworkParamQuery, Required: true, Schema: &huma.Schema{Type: huma.TypeString}},
	}
	operation.Responses = map[string]*huma.Response{
		"200": {
			Description: "Gzip-compressed job artifact",
			Content:     map[string]*huma.MediaType{mediaTypeCatalogGzip: {Schema: &huma.Schema{Type: huma.TypeString, Format: artworkBinaryFormat}}},
			Headers: map[string]*huma.Param{
				directDisposition:         {Schema: &huma.Schema{Type: huma.TypeString}},
				adminSubtitleLengthHeader: {Schema: &huma.Schema{Type: artworkIntegerType}},
			},
		},
		"404": {Description: "Artifact not found, or the capability is invalid or expired"},
		"503": {Description: "Artifact storage unavailable"},
	}
	RegisterRaw(reg, RawOperation{
		Operation: Operation{Operation: operation, Class: ClassPublic, ServiceBacked: true},
		Protocol:  "job-artifact",
		Reason:    "Artifact bytes must not pass through JSON encoding, and the signed capability replaces the presigned storage URL a session-gated route could not provide.",
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		// Every rejection answers 404 so the route never reveals whether a job
		// exists to a caller holding no valid capability.
		if strings.TrimSpace(id) == "" || len(id) > 128 || reg.deps.AdminJobArtifacts == nil || reg.deps.AdminJobArtifactSigner == nil {
			writeProblem(w, r, NewProblem(TypeNotFound, "Job artifact not found."))
			return
		}
		exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
		if err != nil {
			writeProblem(w, r, NewProblem(TypeNotFound, "Job artifact not found."))
			return
		}
		if err := reg.deps.AdminJobArtifactSigner.Verify(id, exp, r.URL.Query().Get("sig"), time.Now()); err != nil {
			writeProblem(w, r, NewProblem(TypeNotFound, "Job artifact not found."))
			return
		}
		// The capability has verified by this point, so the caller has proven it
		// was given a URL for this job. Hiding an outage behind 404 from here on
		// would tell an authorized administrator their artifact is permanently
		// gone when storage is only down; only a genuinely absent job or
		// artifact is 404.
		download, err := reg.deps.AdminJobArtifacts.OpenAdminJobArtifact(r.Context(), id)
		switch {
		case errors.Is(err, handlers.ErrJobArtifactNotFound):
			writeProblem(w, r, NewProblem(TypeNotFound, "Job artifact not found."))
			return
		case err != nil:
			writeProblem(w, r, unavailable("job artifacts"))
			return
		case download.Body == nil:
			writeProblem(w, r, NewProblem(TypeNotFound, "Job artifact not found."))
			return
		}
		defer func() { _ = download.Body.Close() }()
		// A large export can outlast the API server's absolute WriteTimeout, and
		// Accept-Ranges: none means a cut-off download cannot resume. Roll the
		// write deadline forward while bytes keep flowing, as the other download
		// routes do; a stalled client is still reaped.
		sw := httpstream.NewRollingDeadlineWriter(w)
		sw.Header().Set("Content-Type", mediaTypeCatalogGzip)
		sw.Header().Set(directDisposition, mime.FormatMediaType("attachment", map[string]string{dispositionFilenameParam: download.Filename}))
		sw.Header().Set("Cache-Control", "no-store")
		sw.Header().Set("Accept-Ranges", "none")
		if download.Size != nil && *download.Size >= 0 {
			sw.Header().Set(adminSubtitleLengthHeader, strconv.FormatInt(*download.Size, 10))
		}
		sw.WriteHeader(http.StatusOK)
		if _, err := io.Copy(sw, download.Body); err != nil {
			slog.WarnContext(r.Context(), "job artifact stream interrupted", "component", "adminjob", "job_id", id)
		}
	}))
}
