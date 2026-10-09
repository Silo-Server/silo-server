package apiv2

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/danielgtaylor/huma/v2"
)

const (
	directContentEncoding = "Content-Encoding"
	directParamQuery      = "query"
	directAccountToken    = "token"
	directLinkToken       = apimw.DirectDownloadLinkParam
	directMediaBinary     = "application/octet-stream"
	directBinaryFormat    = "binary"
	directContentLength   = "Content-Length"
	directLastModified    = "Last-Modified"
	directCacheControl    = "Cache-Control"

	directContentType  = "Content-Type"
	directAcceptRanges = "Accept-Ranges"
	directIfRange      = "If-Range"
	directIfModified   = "If-Modified-Since"
	directIfUnmodified = "If-Unmodified-Since"

	directOriginalFormat    = "original"
	directRangeHeader       = "Range"
	directMultipart         = "multipart/byteranges"
	directDisposition       = "Content-Disposition"
	directDownloadPath      = "/direct-download"
	directDownloadProxyPath = "/direct-download-proxy"
	directDownloadFormat    = "format"
	directDownloadsTag      = "downloads"
	directContentRange      = "Content-Range"
)

// DirectDownloadHandlers retain the existing original-file permission,
// streaming and proxy-reservation lifecycle. They do not create playback sessions.
type DirectDownloadHandlers struct{ Original, Proxy http.Handler }

// DirectDownloadLinkService authorizes one file for the request's verified
// profile and mints the short-lived link token the direct-download routes
// accept as `dl`; handlers.DownloadHandler implements it.
type DirectDownloadLinkService interface {
	CreateDirectDownloadLink(ctx context.Context, fileID int) (token string, expiresAt time.Time, err error)
}

type DirectDownloadLinkBody struct {
	FileID ID `json:"file_id" doc:"Media file to download, as a canonical positive decimal string."`
}
type DirectDownloadLinkInput struct{ Body DirectDownloadLinkBody }
type DirectDownloadLink struct {
	URL       string  `json:"url" doc:"Server-relative GET/HEAD URL for /api/v2/direct-download that carries the link token as dl. Treat it as a secret until it expires."`
	ProxyURL  string  `json:"proxy_url" doc:"The same link for /api/v2/direct-download-proxy; use it only when the download capability reports proxy_delivery."`
	ExpiresAt Instant `json:"expires_at" doc:"Latest time a request with this link is accepted. A transfer that started in time may run past it."`
}
type DirectDownloadLinkOutput struct{ Body DirectDownloadLink }

// metaDirectDownloadLink marks the byte routes whose gate chain accepts a
// direct-download link token in place of the account credential.
const metaDirectDownloadLink = "silo.direct_download_link"

func registerDirectDownloadLinks(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+directDownloadPath+"/links", "createDirectDownloadLink", directDownloadsTag, "Authorize one original file for the selected profile and return a short-lived direct-download URL that a browser navigation can open without headers."), Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.MaxBodyBytes = 1024
	Register(reg, op, func(ctx context.Context, in *DirectDownloadLinkInput) (*DirectDownloadLinkOutput, error) {
		svc := reg.deps.DirectDownloadLinks
		if svc == nil {
			return nil, unavailable("direct download links")
		}
		fileID, p := in.Body.FileID.positive("body.file_id")
		if p != nil {
			return nil, p
		}
		token, expiresAt, err := svc.CreateDirectDownloadLink(ctx, fileID)
		if err != nil {
			return nil, directDownloadLinkProblem(err)
		}
		query := url.Values{playbackParamFileID: {strconv.Itoa(fileID)}, directLinkToken: {token}}.Encode()
		return &DirectDownloadLinkOutput{Body: DirectDownloadLink{
			URL:       Prefix + directDownloadPath + "?" + query,
			ProxyURL:  Prefix + directDownloadProxyPath + "?" + query,
			ExpiresAt: NewInstant(expiresAt),
		}}, nil
	})
}

func directDownloadLinkProblem(err error) *Problem {
	switch {
	case errors.Is(err, handlers.ErrDirectDownloadLinksUnavailable):
		return unavailable("direct download links")
	case errors.Is(err, handlers.ErrDirectDownloadLinkSession):
		return NewProblem(TypePermissionDenied, "Download links need a signed-in session; an API key can call direct download with its own credential.")
	case errors.Is(err, catalogpkg.ErrItemNotFound), errors.Is(err, downloads.ErrNotFound):
		return NewProblem(TypeNotFound, "File not found.")
	case errors.Is(err, downloads.ErrFeatureDisabled), errors.Is(err, downloads.ErrDownloadNotAllowed):
		return NewProblem(TypePermissionDenied, "Downloads are not allowed.")
	default:
		return serviceProblem(err)
	}
}

func registerDirectDownloads(reg *Registry) {
	registerDirectDownloadLinks(reg)
	var handlers DirectDownloadHandlers
	if reg.deps.DirectDownloads != nil {
		handlers = *reg.deps.DirectDownloads
	}
	for _, route := range []struct {
		method, path, id string
		handler          http.Handler
		proxy            bool
	}{
		{http.MethodGet, directDownloadPath, "getDirectDownload", handlers.Original, false},
		{http.MethodHead, directDownloadPath, "headDirectDownload", handlers.Original, false},
		{http.MethodGet, directDownloadProxyPath, "getDirectDownloadProxy", handlers.Proxy, true},
		{http.MethodHead, directDownloadProxyPath, "headDirectDownloadProxy", handlers.Proxy, true},
	} {
		params := []*huma.Param{
			{Name: "file_id", In: directParamQuery, Required: true, Schema: &huma.Schema{Type: huma.TypeString, Pattern: "^[1-9][0-9]*$"}},
			{Name: directDownloadFormat, In: directParamQuery, Schema: &huma.Schema{Type: huma.TypeString, Enum: []any{"", directOriginalFormat}}},
			{Name: directAccountToken, In: directParamQuery, Description: "Existing account bearer fallback for browser navigation without authorization headers. Does not grant profile or file authority, so on an account with a PIN-protected or access-restricted profile it needs X-Profile-Id like any other account credential; browsers use dl instead.", Schema: &huma.Schema{Type: huma.TypeString}},
			{Name: directLinkToken, In: directParamQuery, Description: "Direct-download link token from POST /api/v2/direct-download/links, used in place of any other credential. It authorizes only the file_id it was minted for, as the profile that minted it, while its login session stays valid; it is refused with 401 once expired and 403 for another file. Any X-Profile-Id or X-Profile-Token header is ignored. Do not combine with token.", Schema: &huma.Schema{Type: huma.TypeString}},
		}
		for _, name := range []string{directRangeHeader, directIfRange, ifMatchField, ifNoneMatchField, directIfModified, directIfUnmodified} {
			params = append(params, &huma.Param{Name: name, In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}})
		}
		var content map[string]*huma.MediaType
		if route.method == http.MethodGet {
			content = map[string]*huma.MediaType{directMediaBinary: {Schema: &huma.Schema{Type: huma.TypeString, Format: directBinaryFormat}}, directMultipart: {Schema: &huma.Schema{Type: huma.TypeString, Format: directBinaryFormat}}}
		}
		if route.method == http.MethodGet {
			for _, mime := range playback.MediaMIMETypes() {
				content[mime] = &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: directBinaryFormat}}
			}
		}
		headers := map[string]*huma.Param{}
		for _, name := range []string{directContentLength, directContentType, directDisposition, directAcceptRanges, directLastModified, directCacheControl} {
			headers[name] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}}
		}
		responses := map[string]*huma.Response{
			"200": {Description: "Authorized original bytes or HEAD metadata; content type follows the original file.", Content: content, Headers: headers},
			"206": {Description: "Requested original byte range", Content: content, Headers: map[string]*huma.Param{directContentRange: {Schema: &huma.Schema{Type: huma.TypeString}}}},
			"304": {Description: "Authorized file has not changed"},
		}
		if route.proxy {
			responses["307"] = &huma.Response{Description: "Authorized target on the selected proxy; short-lived signed URL. Not a receipt for successful transfer.", Headers: map[string]*huma.Param{jobLocationHeader: {Schema: &huma.Schema{Type: huma.TypeString}}}}
		}
		for _, status := range []int{400, 404, 409, 412, 416, 422, 500, 503} {
			responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")}}}
		}
		responses["416"].Headers = map[string]*huma.Param{directContentRange: {Schema: &huma.Schema{Type: huma.TypeString}}}
		raw := RawOperation{Operation: Operation{Operation: huma.Operation{Method: route.method, Path: Prefix + route.path, OperationID: route.id, Tags: []string{directDownloadsTag}, Parameters: params, Responses: responses, Metadata: map[string]any{metaDirectDownloadLink: true}}, Class: ClassProfileScoped, ProfileOptional: true, HouseholdProfileGate: true, DemoRestricted: isMutatingMethod(route.method), ServiceBacked: true}, Protocol: "download-bytes", Reason: "Original downloads retain streaming, HEAD, range and optional proxy redirect semantics without JSON buffering or a persistent download record."}
		RegisterRaw(reg, raw, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			query, err := parseDirectDownloadQuery(r)
			if err != nil {
				writeProblem(w, r, err)
				return
			}
			if route.handler == nil {
				writeProblem(w, r, NewProblem(TypeDependencyUnavailable, "Direct downloads are unavailable."))
				return
			}
			request := r.Clone(r.Context())
			u := *r.URL
			request.URL = &u
			request.URL.RawQuery = query
			route.handler.ServeHTTP(&directDownloadWriter{ResponseWriter: w, request: r}, request)
		}))
	}
}

func parseDirectDownloadQuery(r *http.Request) (string, *Problem) {
	values, err := urlParseDirectQuery(r)
	if err != nil {
		return "", err
	}
	id, p := ID(values.Get("file_id")).positive("query.file_id")
	if p != nil {
		return "", p
	}
	if strconv.Itoa(id) != values.Get("file_id") {
		return "", validationProblem("query.file_id", "invalid", "Expected a canonical positive integer identifier.")
	}
	if format := values.Get(directDownloadFormat); format != "" && format != directOriginalFormat {
		return "", validationProblem("query.format", "invalid", "Only original files are available.")
	}
	return values.Encode(), nil
}

func urlParseDirectQuery(r *http.Request) (url.Values, *Problem) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, validationProblem("query.parameters", "invalid", "Invalid download query.")
	}
	for key, v := range values {
		if key != "file_id" && key != directDownloadFormat && key != directAccountToken && key != directLinkToken {
			return nil, validationProblem("query.parameters", "unknown", "Unknown download query parameter.")
		}
		if len(v) != 1 {
			return nil, validationProblem("query.parameters", "invalid", "Download query parameters must occur once.")
		}
	}
	if values.Has(directAccountToken) && values.Has(directLinkToken) {
		return nil, validationProblem("query.parameters", "invalid", "Send either token or dl, not both.")
	}
	return values, nil
}

// The legacy direct transport reports failures through HTTP. Adapt only this
// byte boundary: redact before commitment and abort if an error follows bytes.
// This adapter is independent of the held playback executor/runtime transport.
type directDownloadWriter struct {
	http.ResponseWriter
	request *http.Request
	inner   *streamResponseWriter
}

func (w *directDownloadWriter) transport() *streamResponseWriter {
	if w.inner == nil {
		w.inner = &streamResponseWriter{ResponseWriter: w.ResponseWriter, request: w.request, problemType: TypeForStatus, redactHeaders: []string{directContentLength, directContentType, directDisposition, directContentEncoding, jobLocationHeader, etagField, directLastModified}}
	}
	return w.inner
}
func (w *directDownloadWriter) Unwrap() http.ResponseWriter    { return w.ResponseWriter }
func (w *directDownloadWriter) WriteHeader(status int)         { w.transport().WriteHeader(status) }
func (w *directDownloadWriter) Write(data []byte) (int, error) { return w.transport().Write(data) }
func (w *directDownloadWriter) FlushError() error              { return w.transport().FlushError() }
