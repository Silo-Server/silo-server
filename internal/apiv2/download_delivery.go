package apiv2

import (
	"net/http"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

type DownloadDeliveryService interface {
	ServeDownloadFile(http.ResponseWriter, *http.Request, string, bool) error
	ServeDownloadArtwork(http.ResponseWriter, *http.Request, string, string) error
	ServeDownloadSubtitle(http.ResponseWriter, *http.Request, string, string) error
}

// Kinds of managed-download asset served by the raw delivery routes.
const (
	downloadAssetFile     = "file"
	downloadAssetArtwork  = "artwork"
	downloadAssetSubtitle = "subtitle"
)

func registerDownloadDelivery(reg *Registry) {
	// observe marks the routes counted as viewer transfer egress, mirroring
	// v1: media bytes and subtitles, not catalog artwork. Each observed
	// pattern must be declared in the API's native media routes.
	type route struct {
		method, path, id, kind string
		proxy, observe         bool
	}
	routes := []route{
		{http.MethodGet, "/downloads/{id}/file", "downloadFile", downloadAssetFile, false, true},
		{http.MethodHead, "/downloads/{id}/file", "headDownloadFile", downloadAssetFile, false, true},
		{http.MethodGet, "/downloads/{id}/file-proxy", "downloadFileViaProxy", downloadAssetFile, true, true},
		{http.MethodHead, "/downloads/{id}/file-proxy", "headDownloadFileViaProxy", downloadAssetFile, true, true},
		{http.MethodGet, "/downloads/{id}/artwork/{kind}", "getDownloadArtwork", downloadAssetArtwork, false, false},
		{http.MethodGet, "/downloads/{id}/subtitles/{ref}", "getDownloadSubtitle", downloadAssetSubtitle, false, true},
	}
	for _, route := range routes {
		op := humaOp(route.method, Prefix+route.path, route.id, "downloads", "Stream an authorized download file or offline asset using the existing delivery service.")
		op.Parameters = []*huma.Param{{Name: "id", In: "path", Required: true, Schema: &huma.Schema{Type: huma.TypeString, MinLength: new(1)}}}
		if route.kind != downloadAssetFile {
			name := "kind"
			if route.kind == downloadAssetSubtitle {
				name = "ref"
			}
			op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: "path", Required: true, Schema: &huma.Schema{Type: huma.TypeString, MinLength: new(1)}})
		}
		op.Parameters = append(op.Parameters, &huma.Param{Name: "X-Silo-Device-Id", In: paramInHeader, Required: route.kind != downloadAssetFile, Schema: &huma.Schema{Type: huma.TypeString, MaxLength: new(128)}})
		headers := map[string]*huma.Param{}
		for _, name := range []string{"Content-Length", "Content-Disposition", "Cache-Control", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag", "Location"} {
			headers[name] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}}
		}
		media := map[string]*huma.MediaType{}
		if route.method == http.MethodGet {
			types := []string{"application/octet-stream"}
			switch route.kind {
			case downloadAssetFile:
				types = append(types, "video/*", "audio/*", "multipart/byteranges")
			case downloadAssetArtwork:
				types = []string{"image/*"}
			case downloadAssetSubtitle:
				types = []string{"text/plain", "text/vtt", "application/x-subrip", "text/x-ssa", "text/x-ass", "application/ttml+xml", "application/octet-stream"}
			}
			for _, name := range types {
				media[name] = &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}
			}
		}
		problem := func(description string) *huma.Response {
			return &huma.Response{Description: description, Content: map[string]*huma.MediaType{problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")}}}
		}
		op.Responses = map[string]*huma.Response{"200": {Description: "Authorized bytes; HEAD returns only file metadata.", Content: media, Headers: headers}, "400": problem("Invalid subtitle reference."), "422": problem("Invalid download identity or device scope."), "404": problem("Download or asset not found in this scope."), "409": problem("The download is not active.")}
		if route.kind == downloadAssetFile {
			for _, name := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
				op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: "header", Schema: &huma.Schema{Type: huma.TypeString}})
			}
			op.Responses["206"] = &huma.Response{Description: "Single or multipart byte range.", Content: media, Headers: headers}
			op.Responses["304"] = &huma.Response{Description: "Representation not modified.", Headers: headers}
			op.Responses["412"] = &huma.Response{Description: "File precondition failed.", Headers: headers}
			op.Responses["416"] = &huma.Response{Description: "Range not satisfiable.", Content: map[string]*huma.MediaType{"text/plain": {Schema: &huma.Schema{Type: huma.TypeString}}}, Headers: headers}
		}
		if route.kind == downloadAssetSubtitle {
			// Downloaded subtitles revalidate: a stored timing correction can
			// change their bytes (ETag carries the subtitle revision).
			op.Parameters = append(op.Parameters, &huma.Param{Name: "If-None-Match", In: "header", Schema: &huma.Schema{Type: huma.TypeString}})
			op.Responses["304"] = &huma.Response{Description: "Representation not modified.", Headers: headers}
		}
		if route.proxy {
			op.Responses["307"] = &huma.Response{Description: "Authorized temporary proxy location; preserve the original method and range headers.", Headers: headers}
			if route.method == http.MethodGet {
				op.Responses["307"].Content = map[string]*huma.MediaType{"text/html": {Schema: &huma.Schema{Type: huma.TypeString}}}
			}
		}
		if route.kind == downloadAssetArtwork {
			op.Responses["503"] = problem("Service Unavailable")
			op.Responses["503"].Headers = map[string]*huma.Header{}
			op.Responses["503"].Headers["Retry-After"] = &huma.Header{Schema: &huma.Schema{Type: huma.TypeString}, Description: "Seconds to wait before retrying when the artwork store failed or could not be reached."}
		}
		var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reg.serveDownloadDelivery(w, r, route.kind, route.proxy)
		})
		if route.observe && reg.deps.ObserveDownloadDelivery != nil {
			handler = reg.deps.ObserveDownloadDelivery(route.method, Prefix+route.path, handler)
		}
		RegisterRaw(reg, RawOperation{Operation: Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}, Protocol: "managed-download-" + route.kind, Reason: "Offline media and assets are binary streams; file routes preserve HTTP range, conditional and optional proxy semantics."}, handler)
	}
}
func (reg *Registry) serveDownloadDelivery(w http.ResponseWriter, r *http.Request, kind string, proxy bool) {
	if reg.deps.DownloadDelivery == nil {
		writeProblem(w, r, unavailable("download delivery"))
		return
	}
	id := chi.URLParam(r, "id")
	device := strings.TrimSpace(r.Header.Get("X-Silo-Device-Id"))
	if id == "" || len(device) > 128 || (kind != downloadAssetFile && device == "") {
		writeProblem(w, r, NewProblem(TypeValidationFailed, "A download identity and valid device scope are required."))
		return
	}
	writer := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
	// Artwork and subtitles are copied from stores that can fail on the first
	// read. Hiding ReadFrom leaves the wrapper to commit 200 on the first byte
	// written rather than before the first read, so that failure still gets
	// its problem response. Files keep ReadFrom for sendfile; ServeContent
	// writes their header before copying anyway.
	asset := assetResponseWriter{writer}
	var err error
	switch kind {
	case downloadAssetFile:
		err = reg.deps.DownloadDelivery.ServeDownloadFile(writer, r, id, proxy)
	case downloadAssetArtwork:
		err = reg.deps.DownloadDelivery.ServeDownloadArtwork(asset, r, id, chi.URLParam(r, "kind"))
	case downloadAssetSubtitle:
		err = reg.deps.DownloadDelivery.ServeDownloadSubtitle(asset, r, id, chi.URLParam(r, "ref"))
	}
	if err == nil || writer.Status() != 0 {
		return
	}
	// Upstream assets can fail after setting binary headers but before writing.
	// Never append JSON after committed bytes or retain their advertised length.
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Content-Disposition", "ETag", "Cache-Control"} {
		w.Header().Del(name)
	}
	writeProblem(w, r, downloadProblem(err))
}

// assetResponseWriter hides ReadFrom from artwork and subtitle copies (see
// serveDownloadDelivery) while keeping Unwrap, so response controllers can
// still reach the connection for rolling write deadlines.
type assetResponseWriter struct{ http.ResponseWriter }

func (w assetResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
