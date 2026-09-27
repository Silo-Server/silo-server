package apiv2

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/danielgtaylor/huma/v2"
)

type DiagnosticsIngressService interface {
	UploadStatus(context.Context, int) (diagnostics.Status, error)
	ExtendUploadDeadlines(http.ResponseWriter, *http.Request)
	IngestMultipart(http.ResponseWriter, *http.Request, int, *string) (diagnostics.IngestResult, error)
}

type DiagnosticsCapabilities struct {
	Capability
	diagnostics.Status
}
type DiagnosticsCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         DiagnosticsCapabilities
}
type DiagnosticsUploadOutput struct{ Body diagnostics.IngestResult }

// Both parts are files. The shared ingest service validates the manifest JSON
// and streams the gzip bundle without framework form buffering.
type DiagnosticsUploadForm struct {
	Manifest string `json:"manifest" format:"binary" doc:"JSON manifest file, first part; at most 65536 bytes."`
	Bundle   string `json:"bundle" format:"binary" doc:"Gzip bundle file, second and final part; bounded by the advertised max_bundle_bytes."`
}

type DiagnosticsUploadInput struct {
	ProfileID string `header:"X-Profile-Id" doc:"Optional captured report attribution. Must belong to this account and cannot be a child profile; independent of the current viewer."`
	requestCapture
}

// Resolve captures the stream after authentication and media gates. Omitting
// Body/RawBody keeps Huma from pre-reading or spooling the multipart request.

func diagnosticsAccount(ctx context.Context) (int, error) {
	claims := claimsFrom(ctx)
	if claims == nil || claims.UserID <= 0 {
		return 0, NewProblem(TypeAuthenticationRequired, "Diagnostics require a signed-in account.")
	}
	if claims.TokenType != auth.TokenTypeAccess {
		return 0, NewProblem(TypePermissionDenied, "Diagnostics require a user access token; API keys are not permitted.")
	}
	return claims.UserID, nil
}

func diagnosticsIngressProblem(err error) error {
	failure, ok := errors.AsType[*handlers.DiagnosticsUploadFailure](err)
	if !ok {
		return NewProblem(TypeInternalError, "Diagnostics upload failed.")
	}
	kind := TypeForStatus(failure.Status)
	// The shared ingress distinguishes operator configuration from request
	// permissions and transient capacity using these domain failure codes.
	switch failure.Code {
	case handlers.DiagnosticsDisabledCode:
		kind = TypeCapabilityDisabled
	case handlers.DiagnosticsStorageUnavailableCode:
		kind = TypeCapabilityNotConfigured
	}
	problem := NewProblem(kind, failure.Message)
	if failure.RetryAfter != "" {
		problem = problem.WithHeader("Retry-After", failure.RetryAfter)
	}
	return problem
}

func registerDiagnosticsIngress(reg *Registry) {
	read := Operation{Operation: humaOp(http.MethodGet, Prefix+"/diagnostics/capabilities", "getDiagnosticsCapabilities", "diagnostics", "Read diagnostics upload availability and limits for this account."), Class: ClassAuthenticated, ServiceBacked: true}
	read.Errors = []int{http.StatusForbidden}
	Register(reg, read, func(ctx context.Context, _ *CapabilityInput) (*DiagnosticsCapabilitiesOutput, error) {
		userID, err := diagnosticsAccount(ctx)
		if err != nil {
			return nil, err
		}
		if reg.deps.DiagnosticsIngress == nil {
			return &DiagnosticsCapabilitiesOutput{Body: DiagnosticsCapabilities{Capability: Capability{State: StateNotConfigured}, Status: diagnostics.Status{Status: diagnostics.StatusStorageUnavailable, AcceptedSchemaVersions: []int{}}}}, nil
		}
		status, err := reg.deps.DiagnosticsIngress.UploadStatus(ctx, userID)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Failed to load diagnostics status.")
		}
		state := StateNotConfigured
		switch status.Status {
		case diagnostics.StatusAvailable:
			state = StateAvailable
		case diagnostics.StatusDisabled:
			state = StateDisabled
		}
		// Nonzero advertises working chunk routes in this API namespace.
		// A missing chunk service must not advertise the bridge transport.
		if reg.deps.DiagnosticsChunks == nil {
			status.UploadChunkBytes = 0
		}
		return &DiagnosticsCapabilitiesOutput{Body: DiagnosticsCapabilities{Capability: Capability{State: state}, Status: status}}, nil
	})
	upload := Operation{Operation: humaOp(http.MethodPost, Prefix+"/diagnostics/reports", "uploadDiagnosticsReport", "diagnostics", "Stream an ordered manifest and gzip bundle through diagnostics validation."), Class: ClassAuthenticated, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	upload.DefaultStatus = http.StatusCreated
	// Hard administrative maximum plus existing framing allowance. The shared
	// service enforces the lower live limit before parsing any part.
	upload.MaxBodyBytes = (256 << 20) + (128 << 10)
	upload.Errors = []int{400, 403, 408, 409, 413, 415, 429, 500, 503}
	requestBody := &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
		mediaTypeMultipart: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[DiagnosticsUploadForm](), true, ""), Encoding: map[string]*huma.Encoding{
			"manifest": {ContentType: "application/json"}, "bundle": {ContentType: diagnostics.BundleContentType},
		}},
	}}
	responseHeaders := map[string]map[string]*huma.Header{}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		responseHeaders[strconv.Itoa(status)] = map[string]*huma.Header{
			"Retry-After": {Schema: &huma.Schema{Type: "string"}, Description: "Minimum delay in seconds after an explicit quota or busy rejection; not permission to replay an uncertain upload."},
		}
	}
	Register(reg, upload, func(ctx context.Context, in *DiagnosticsUploadInput) (*DiagnosticsUploadOutput, error) {
		userID, err := diagnosticsAccount(ctx)
		if err != nil {
			return nil, err
		}
		if reg.deps.DiagnosticsIngress == nil {
			return nil, CapabilityProblem(StateNotConfigured, "diagnostics")
		}
		var profileID *string
		if value := strings.TrimSpace(in.ProfileID); value != "" {
			profileID = new(value)
		}
		reg.deps.DiagnosticsIngress.ExtendUploadDeadlines(in.writer, in.request)
		result, err := reg.deps.DiagnosticsIngress.IngestMultipart(in.writer, in.request, userID, profileID)
		if err != nil {
			return nil, diagnosticsIngressProblem(err)
		}
		return &DiagnosticsUploadOutput{Body: result}, nil
	})
	// Huma treats any request schema as a request to read the whole body, even
	// without an input Body field. Attach this typed multipart description only
	// after registration, so the runtime media gate sees it but Huma's compiled
	// input decoder never buffers the stream.
	registered := registeredOperation(reg.api.OpenAPI(), upload)
	registered.RequestBody = requestBody
	for status, headers := range responseHeaders {
		registered.Responses[status].Headers = headers
	}

}
