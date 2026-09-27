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

func registerDownloadDelivery(reg *Registry) {
	type route struct {
		method, path, id, kind string
		proxy                  bool
	}
	routes := []route{
		{"GET", "/downloads/{id}/file", "downloadFile", "file", false},
		{"HEAD", "/downloads/{id}/file", "headDownloadFile", "file", false},
		{"GET", "/downloads/{id}/file-proxy", "downloadFileViaProxy", "file", true},
		{"HEAD", "/downloads/{id}/file-proxy", "headDownloadFileViaProxy", "file", true},
		{"GET", "/downloads/{id}/artwork/{kind}", "getDownloadArtwork", "artwork", false},
		{"GET", "/downloads/{id}/subtitles/{ref}", "getDownloadSubtitle", "subtitle", false},
	}
	for _, route := range routes {
		op := humaOp(route.method, Prefix+route.path, route.id, "downloads", "Stream an authorized download file or offline asset using the existing delivery service.")
		op.Parameters = []*huma.Param{{Name: "id", In: "path", Required: true, Schema: &huma.Schema{Type: huma.TypeString, MinLength: new(1)}}}
		if route.kind != "file" {
			name := "kind"
			if route.kind == "subtitle" {
				name = "ref"
			}
			op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: "path", Required: true, Schema: &huma.Schema{Type: huma.TypeString, MinLength: new(1)}})
		}
		op.Parameters = append(op.Parameters, &huma.Param{Name: "X-Silo-Device-Id", In: "header", Required: route.kind != "file", Schema: &huma.Schema{Type: huma.TypeString, MaxLength: new(128)}})
		headers := map[string]*huma.Param{}
		for _, name := range []string{"Content-Length", "Content-Disposition", "Cache-Control", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag", "Location"} {
			headers[name] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}}
		}
		media := map[string]*huma.MediaType{}
		if route.method == "GET" {
			types := []string{"application/octet-stream"}
			switch route.kind {
			case "file":
				types = append(types, "video/*", "audio/*", "multipart/byteranges")
			case "artwork":
				types = []string{"image/*"}
			case "subtitle":
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
		if route.kind == "file" {
			for _, name := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
				op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: "header", Schema: &huma.Schema{Type: huma.TypeString}})
			}
			op.Responses["206"] = &huma.Response{Description: "Single or multipart byte range.", Content: media, Headers: headers}
			op.Responses["304"] = &huma.Response{Description: "Representation not modified.", Headers: headers}
			op.Responses["412"] = &huma.Response{Description: "File precondition failed.", Headers: headers}
			op.Responses["416"] = &huma.Response{Description: "Range not satisfiable.", Content: map[string]*huma.MediaType{"text/plain": {Schema: &huma.Schema{Type: huma.TypeString}}}, Headers: headers}
		}
		if route.proxy {
			op.Responses["307"] = &huma.Response{Description: "Authorized temporary proxy location; preserve the original method and range headers.", Headers: headers}
			if route.method == http.MethodGet {
				op.Responses["307"].Content = map[string]*huma.MediaType{"text/html": {Schema: &huma.Schema{Type: huma.TypeString}}}
			}
		}
		RegisterRaw(reg, RawOperation{Operation: Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}, Protocol: "managed-download-" + route.kind, Reason: "Offline media and assets are binary streams; file routes preserve HTTP range, conditional and optional proxy semantics."}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reg.serveDownloadDelivery(w, r, route.kind, route.proxy)
		}))
	}
}
func (reg *Registry) serveDownloadDelivery(w http.ResponseWriter, r *http.Request, kind string, proxy bool) {
	if reg.deps.DownloadDelivery == nil {
		writeProblem(w, r, unavailable("download delivery"))
		return
	}
	id := chi.URLParam(r, "id")
	device := strings.TrimSpace(r.Header.Get("X-Silo-Device-Id"))
	if id == "" || len(device) > 128 || (kind != "file" && device == "") {
		writeProblem(w, r, NewProblem(TypeValidationFailed, "A download identity and valid device scope are required."))
		return
	}
	writer := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
	var err error
	switch kind {
	case "file":
		err = reg.deps.DownloadDelivery.ServeDownloadFile(writer, r, id, proxy)
	case "artwork":
		err = reg.deps.DownloadDelivery.ServeDownloadArtwork(writer, r, id, chi.URLParam(r, "kind"))
	case "subtitle":
		err = reg.deps.DownloadDelivery.ServeDownloadSubtitle(writer, r, id, chi.URLParam(r, "ref"))
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
