package apiv2

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	apihandlers "github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	playbackAccountToken  = "token"
	playbackContentLength = "Content-Length"
	playbackLastModified  = "Last-Modified"
	playbackIntegerFormat = "int64"

	playbackMediaBinary        = "application/octet-stream"
	playbackSegmentOperation   = "getPlaybackSegment"
	playbackSubtitleOperation  = "getPlaybackSubtitle"
	playbackSubtitleHead       = "headPlaybackSubtitle"
	playbackParamTrack         = "track"
	playbackParamFileID        = "file_id"
	playbackParamPosition      = "position"
	playbackTag                = "playback"
	playbackSubtitleSubrip     = "application/x-subrip"
	playbackSubtitleVTT        = "text/vtt"
	playbackSubtitleSSA        = "text/x-ssa"
	playbackParamPath          = "path"
	playbackParamQuery         = "query"
	playbackSegmentName        = "name"
	playbackBinaryFormat       = "binary"
	playbackCacheControlHeader = "Cache-Control"
	playbackContentEncoding    = "Content-Encoding"
)

// PlaybackMediaHandlers shares the raw byte delivery protocols and the typed
// font service. Token-carried reconstruction and deny markers live in these
// shared operations; the v2 listener owns JSON envelopes and problem responses.
type PlaybackMediaHandlers struct {
	Original      http.Handler
	Manifest      http.Handler
	Segment       http.Handler
	Subtitle      http.Handler
	SubtitleFonts SubtitleFontService
}

type SubtitleFontService interface {
	SubtitleFonts(context.Context, apihandlers.SubtitleFontRequest) ([]playback.SubtitleFontBundleItem, error)
}

type PlaybackSubtitleFont struct {
	Name string `json:"name" doc:"Attachment file name as authored in the container"`
	Data string `json:"data" doc:"Base64-encoded font bytes"`
}
type PlaybackSubtitleFontsInput struct {
	SessionID           ID     `path:"session_id" minLength:"1"`
	Track               string `path:"track" minLength:"1" doc:"Combined subtitle ordinal from the plan inventory"`
	FileID              string `query:"file_id" doc:"Source media file the inventory URL names; must be the plan's effective or requested file"`
	EmbeddedStreamIndex string `query:"embedded_stream_index" doc:"Stable embedded subtitle stream index from the issued inventory URL; resolves the track independently of its combined ordinal"`
	Reference           string `query:"st" doc:"Signed stream reference the plan's font bundle URL carries; account authentication and viewer authorization are always required"`
	Token               string `query:"token" doc:"Media-element fallback for the account bearer token"`
	query               url.Values
}

func (in *PlaybackSubtitleFontsInput) Resolve(ctx huma.Context) []error {
	r, _ := humachi.Unwrap(ctx)
	in.query = r.URL.Query()
	return nil
}

type PlaybackSubtitleFontsOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         Collection[PlaybackSubtitleFont]
}

func registerPlaybackDelivery(reg *Registry) {
	var handlers PlaybackMediaHandlers
	if reg.deps.PlaybackMedia != nil {
		handlers = *reg.deps.PlaybackMedia
	}
	for _, route := range []struct {
		method, path, id, protocol string
		handler                    http.Handler
		media                      []string
		ranges                     bool
	}{
		{http.MethodGet, "/stream/{session_id}", "getPlaybackMedia", "media-bytes", handlers.Original, []string{"video/mp4", "video/x-matroska", "video/webm", "video/x-msvideo", "video/quicktime", "video/mp2t", "video/x-flv", "video/x-ms-wmv", "audio/mp4", "audio/mpeg", "audio/flac", "audio/ogg", "audio/wav", "audio/aac", playbackMediaBinary, "multipart/byteranges"}, true},
		{http.MethodHead, "/stream/{session_id}", "headPlaybackMedia", "media-bytes", handlers.Original, nil, true},
		{http.MethodGet, "/playback/transcode/{session_id}/master.m3u8", "getPlaybackManifest", "hls", handlers.Manifest, []string{"application/vnd.apple.mpegurl"}, false},
		{http.MethodGet, "/playback/transcode/{session_id}/segment/{name}", playbackSegmentOperation, "hls", handlers.Segment, []string{"video/mp4", "video/mp2t", playbackMediaBinary, "multipart/byteranges"}, true},
		{http.MethodGet, "/stream/{session_id}/subtitles/{track}", playbackSubtitleOperation, "subtitle-sidecar", handlers.Subtitle, []string{playbackSubtitleVTT, playbackSubtitleSSA, playbackSubtitleSubrip, playbackMediaBinary}, false},
		{http.MethodHead, "/stream/{session_id}/subtitles/{track}", playbackSubtitleHead, "subtitle-sidecar", handlers.Subtitle, nil, false},
	} {
		params := []*huma.Param{
			{Name: "session_id", In: playbackParamPath, Required: true, Schema: &huma.Schema{Type: huma.TypeString, MinLength: new(1)}},
			{Name: playbackAccountToken, In: playbackParamQuery, Description: "Media-element fallback for the account bearer token when an Authorization header cannot be set. Header-authenticated media requires the Authorization header and the profile selector.", Schema: &huma.Schema{Type: huma.TypeString}},
			{Name: "st", In: playbackParamQuery, Description: "Signed stream reference the plan URL carries; it reconstructs the session after a restart. Omitted for header-authenticated media. Account and viewer authorization are always required.", Schema: &huma.Schema{Type: huma.TypeString}},
		}
		if route.id == playbackSegmentOperation {
			params = append(params, &huma.Param{Name: playbackSegmentName, In: playbackParamPath, Required: true, Schema: &huma.Schema{Type: huma.TypeString, MinLength: new(1)}})
		}
		subtitle := route.id == playbackSubtitleOperation || route.id == playbackSubtitleHead
		if subtitle {
			params = append(params,
				&huma.Param{Name: playbackParamTrack, In: playbackParamPath, Required: true, Description: "Combined subtitle ordinal from the plan inventory, optionally suffixed with the sidecar extension (.vtt, .ass, .sup) the inventory URL carries.", Schema: &huma.Schema{Type: huma.TypeString, MinLength: new(1)}},
				&huma.Param{Name: playbackParamFileID, In: playbackParamQuery, Description: "Source media file the inventory URL names; must be the plan's effective or requested file.", Schema: &huma.Schema{Type: huma.TypeString}},
				&huma.Param{Name: playback.DownloadedSubtitleIDParamV3, In: playbackParamQuery, Description: "Stable downloaded-subtitle identity the inventory URL carries; must belong to the source file.", Schema: &huma.Schema{Type: huma.TypeString}},
				&huma.Param{Name: playbackParamPosition, In: playbackParamQuery, Description: "Seek position in seconds for windowed text extraction.", Schema: &huma.Schema{Type: huma.TypeNumber}},
				&huma.Param{Name: "duration", In: playbackParamQuery, Description: "Window length in seconds for text extraction.", Schema: &huma.Schema{Type: huma.TypeNumber}},
				&huma.Param{Name: "windowed", In: playbackParamQuery, Description: "PGS: opt into a positioned window instead of the whole track.", Schema: &huma.Schema{Type: huma.TypeString}})
		}
		content := map[string]*huma.MediaType{}
		for _, media := range route.media {
			content[media] = &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: playbackBinaryFormat}}
		}
		headers := map[string]*huma.Param{playbackContentLength: {Schema: &huma.Schema{Type: huma.TypeInteger, Format: playbackIntegerFormat}}, playbackCacheControlHeader: {Schema: &huma.Schema{Type: huma.TypeString}}}
		responses := map[string]*huma.Response{"200": {Description: "Playback bytes or HEAD metadata", Content: content, Headers: headers}}
		if route.ranges {
			headers["Accept-Ranges"] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}}
			headers[etagField] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}}
			headers[playbackLastModified] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}}
			responses["206"] = &huma.Response{Description: "Requested byte range", Content: content, Headers: map[string]*huma.Param{"Content-Range": {Schema: &huma.Schema{Type: huma.TypeString}}}}
			responses["304"] = &huma.Response{Description: "The authorized representation has not changed"}
			params = append(params, &huma.Param{Name: ifMatchField, In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}}, &huma.Param{Name: "If-Unmodified-Since", In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}}, &huma.Param{Name: "Range", In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}}, &huma.Param{Name: "If-Range", In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}}, &huma.Param{Name: ifNoneMatchField, In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}}, &huma.Param{Name: "If-Modified-Since", In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}})
		}
		statuses := []int{400, 404, 409, 410, 422, 500, 503}
		if route.ranges {
			statuses = append(statuses, 412, 416)
		}
		if subtitle {
			statuses = append(statuses, 415)
		}
		for _, status := range statuses {
			responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")}}}
		}
		if route.ranges {
			responses["416"].Headers = map[string]*huma.Param{"Content-Range": {Schema: &huma.Schema{Type: huma.TypeString}}}
		}
		reason := "Token-authorized media retains native byte, range, HEAD and HLS semantics without JSON buffering."
		if subtitle {
			reason = "Token-authorized sidecar text and bitmap bytes are streamed as extracted."
		}
		raw := RawOperation{Operation: Operation{Operation: huma.Operation{Method: route.method, Path: Prefix + route.path, OperationID: route.id, Tags: []string{playbackTag}, Parameters: params, Responses: responses}, Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true}, Protocol: route.protocol, Reason: reason}
		RegisterRaw(reg, raw, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !playbackUUID(chi.URLParam(r, "session_id")) {
				writeProblem(w, r, validationProblem("path.session_id", "invalid", "Expected a canonical UUID."))
				return
			}
			if route.handler == nil {
				writeProblem(w, r, NewProblem(TypeDependencyUnavailable, "Playback delivery is not configured."))
				return
			}
			route.handler.ServeHTTP(&playbackDeliveryWriter{ResponseWriter: w, request: r}, r)
		}))
	}
	fonts := humaOp(http.MethodGet, Prefix+"/stream/{session_id}/subtitles/{track}/fonts", "getPlaybackSubtitleFonts", playbackTag,
		"Read the attached-font bundle of a session's embedded ASS/SSA subtitle track. Admission is the sidecar's: account authentication, viewer authorization and the session the plan named.")
	fonts.Errors = []int{http.StatusNotFound, http.StatusGone}
	Register(reg, Operation{Operation: fonts, Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true}, func(ctx context.Context, in *PlaybackSubtitleFontsInput) (*PlaybackSubtitleFontsOutput, error) {
		if !playbackUUID(string(in.SessionID)) {
			return nil, validationProblem("path.session_id", "invalid", "Expected a canonical UUID.")
		}
		if reg.deps.PlaybackMedia == nil || reg.deps.PlaybackMedia.SubtitleFonts == nil {
			return nil, NewProblem(TypeDependencyUnavailable, "Playback delivery is not configured.")
		}
		items, err := reg.deps.PlaybackMedia.SubtitleFonts.SubtitleFonts(ctx, apihandlers.SubtitleFontRequest{
			SessionID: string(in.SessionID), Track: in.Track, Query: in.query,
		})
		if err != nil {
			return nil, playbackSubtitleFontProblem(err)
		}
		fonts := make([]PlaybackSubtitleFont, 0, len(items))
		for _, item := range items {
			fonts = append(fonts, PlaybackSubtitleFont{Name: item.Name, Data: item.Data})
		}
		return &PlaybackSubtitleFontsOutput{CacheControl: playbackCacheControl, Body: NewCollection(fonts)}, nil
	})
}

func playbackSubtitleFontProblem(err error) *Problem {
	failure, ok := errors.AsType[*apihandlers.APIError](err)
	if !ok || failure.Status >= 500 && failure.Status != http.StatusServiceUnavailable {
		return NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	kind := playbackProblemType(failure.Status, failure.Code)
	if failure.Status == http.StatusGone {
		kind = TypePlaybackSessionEnded
	}
	detail := failure.Message
	if detail == "" {
		detail = kind.Title
	}
	return NewProblem(kind, detail)
}

// Playback success bytes pass through immediately. A pre-body transport error
// becomes a safe v2 problem; its legacy JSON/text body is never exposed. Once
// bytes have begun, a transport failure must end the stream, not append JSON.
type playbackDeliveryWriter struct {
	http.ResponseWriter
	request *http.Request
	inner   *streamResponseWriter
}

func (w *playbackDeliveryWriter) transport() *streamResponseWriter {
	if w.inner == nil {
		w.inner = &streamResponseWriter{ResponseWriter: w.ResponseWriter, request: w.request, problemType: playbackDeliveryProblemType, redactHeaders: []string{playbackContentLength, playbackContentEncoding, directDisposition, jobLocationHeader, etagField, playbackLastModified}}
	}
	return w.inner
}

// playbackDeliveryProblemType maps a pre-body failure status of a v1 media
// handler onto the catalog. A 410 is the stream deny marker (the session was
// stopped or expired), which has its own corrective action: start again.
func playbackDeliveryProblemType(status int) ProblemType {
	if status == http.StatusGone {
		return TypePlaybackSessionEnded
	}
	return TypeForStatus(status)
}
func (w *playbackDeliveryWriter) Unwrap() http.ResponseWriter    { return w.ResponseWriter }
func (w *playbackDeliveryWriter) WriteHeader(status int)         { w.transport().WriteHeader(status) }
func (w *playbackDeliveryWriter) Write(data []byte) (int, error) { return w.transport().Write(data) }
func (w *playbackDeliveryWriter) FlushError() error              { return w.transport().FlushError() }
