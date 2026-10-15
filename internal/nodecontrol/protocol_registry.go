package nodecontrol

import (
	"maps"
	"net/http"
	"reflect"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
)

// Protocol describes the node control routes for the x-silo-worker-protocols
// registry. They are served by the API listener, but only transcode nodes call
// them, signing with their own key (RequireNode), so they have no /api/v2
// operation. Failures are plain text unless an entry says otherwise.
func Protocol(schemas huma.Registry) []workerprotocol.Operation {
	const (
		listener  = "api"
		authClass = "node_signed"
		jsonMedia = "application/json"
		textMedia = "text/plain"
		handler   = "(*internal/nodecontrol.Server)."
		inHeader  = "header"
	)
	jsonBody := func(t reflect.Type) *huma.RequestBody {
		return &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{jsonMedia: {Schema: schemas.Schema(t, true, "")}}}
	}
	jsonResponse := func(description string, t reflect.Type) *huma.Response {
		return &huma.Response{Description: description, Content: map[string]*huma.MediaType{jsonMedia: {Schema: schemas.Schema(t, true, "")}}}
	}
	text := func(status int) *huma.Response {
		return &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{textMedia: {Schema: &huma.Schema{Type: huma.TypeString}}}}
	}
	op := func(method, path, name, retry, description string, body *huma.RequestBody, ok map[string]*huma.Response, failures ...int) workerprotocol.Operation {
		responses := map[string]*huma.Response{}
		maps.Copy(responses, ok)
		// RequireNode answers 401 before any handler runs.
		for _, status := range append([]int{http.StatusUnauthorized}, failures...) {
			if key := strconv.Itoa(status); responses[key] == nil {
				responses[key] = text(status)
			}
		}
		return workerprotocol.Operation{Listener: listener, Method: method, Path: path, Handler: handler + name, AuthClass: authClass,
			RetrySafety: retry, Description: description, RequestBody: body, Responses: responses}
	}
	refusal := func(description string) *huma.Response {
		return jsonResponse(description, reflect.TypeFor[ResolveRefusal]())
	}
	grantHeader := &huma.Param{Name: nodesource.HeaderSourceGrant, In: inHeader, Required: true,
		Description: "The source grant, from a dispatch request, a resolution or an open response. A header so that access logs on the way never record it.",
		Schema:      &huma.Schema{Type: huma.TypeString}}
	// A source read answers with the file's own type (playback.ServeOpenFile),
	// application/octet-stream for an unknown extension, and with
	// multipart/byteranges for a request of several ranges.
	bytes := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}
	media := map[string]*huma.MediaType{playback.MimeFromExtension(".unknown"): bytes}
	for _, mime := range playback.MediaMIMETypes() {
		media[mime] = bytes
	}
	rangeMedia := maps.Clone(media)
	rangeMedia["multipart/byteranges"] = bytes
	sourceRead := func(method, body string) workerprotocol.Operation {
		o := op(method, nodesource.PathSources+"/", "HandleSource", "",
			"Bytes of the granted source for the node the grant names. No catalog read: open checks the catalog, and every renewal checks it again. The granted path is opened once, its size and whole-second modification time are compared with the grant, and the bytes come from that descriptor, served by net/http ServeContent with the file's media type by extension. "+body+" A Range header answers 206: a single or suffix range with the file's type, several ranges as multipart/byteranges (nodes send one range); 416 when no range is satisfiable. If-Match on the grant ETag is honored and other conditional headers are ignored, so there is no 304. 403: the grant does not verify, has expired, or is another node's. 410: the file is gone. 412: it changed, or If-Match failed. 503: the core has no source grant key.",
			nil, map[string]*huma.Response{
				"200": {Description: "The whole source, with ETag, Content-Length, Last-Modified and Accept-Ranges.", Content: media},
				"206": {Description: "The requested range or ranges, with Content-Range.", Content: rangeMedia},
			}, http.StatusForbidden, http.StatusGone, http.StatusPreconditionFailed, http.StatusRequestedRangeNotSatisfiable, http.StatusServiceUnavailable)
		o.Parameters = []*huma.Param{grantHeader,
			{Name: "Range", In: inHeader, Schema: &huma.Schema{Type: huma.TypeString}},
			{Name: "If-Match", In: inHeader, Schema: &huma.Schema{Type: huma.TypeString}}}
		return o
	}
	subtitle := op(http.MethodGet, nodesource.PathSubtitle, "HandleSubtitle", "natural_idempotent",
		"The subtitle bundle a text burn-in of the granted source needs, so a node that reads the source over HTTP never demuxes it whole through the relay: a tar of selected.ass, the track as an ASS script extracted with -copyts (text formats convert to ASS, ASS and SSA are copied), and fonts/, the source's font attachments under generated names. The core builds it from its own copy, after the same revision check as a source read, and caches it by revision, track and codec; builds share the subtitle cache's whole-source demux slots. No catalog read. A request waits up to 5 seconds for a build, then answers 202 and the node asks again. 400: the track or codec is missing or malformed, or the grant is not a media file's. 403: the grant does not verify, has expired, or is another node's. 410: the file is gone. 412: it changed. 422: the source cannot give the bundle (FFmpeg fails on the track, or it does not convert to ASS); the failure is cached, so a repeat within a minute answers 422 without building again. 503 is transient, including a failed build of the core's own (its disk, a process that cannot start), which is also cached for a minute.",
		nil, map[string]*huma.Response{
			"200": {Description: "The bundle, with Content-Length.", Content: map[string]*huma.MediaType{"application/x-tar": bytes}},
			"202": {Description: "The bundle is being built, or waits for a build slot; ask again."},
		}, http.StatusBadRequest, http.StatusForbidden, http.StatusGone, http.StatusPreconditionFailed, http.StatusUnprocessableEntity, http.StatusServiceUnavailable)
	subtitle.Parameters = []*huma.Param{grantHeader,
		{Name: nodesource.QuerySubtitleTrack, In: "query", Required: true, Description: "The subtitle stream's ordinal among the source's subtitle streams.", Schema: &huma.Schema{Type: huma.TypeInteger}},
		{Name: nodesource.QuerySubtitleCodec, In: "query", Required: true, Description: "The subtitle stream's codec as FFmpeg names it.", Schema: &huma.Schema{Type: huma.TypeString}}}
	config := op(http.MethodGet, PathConfig, "HandleConfig", "",
		"The calling node's configuration document. Revision, also the ETag, is the SHA-256 of the rest of the document; the node swaps its configuration only when it changes. Polled every 30 seconds with If-None-Match, and at once after /admin/reload-config or /admin/force-reload. 404: the node row is gone.",
		nil, map[string]*huma.Response{
			"200": jsonResponse("Configuration document.", reflect.TypeFor[NodeConfig]()),
			"304": {Description: "Revision unchanged."},
		}, http.StatusNotFound, http.StatusServiceUnavailable)
	config.Parameters = []*huma.Param{{Name: headerIfNoneMatch, In: inHeader, Schema: &huma.Schema{Type: huma.TypeString}}}
	return []workerprotocol.Operation{
		config,
		op(http.MethodPost, PathResolve, "HandleResolve", "non_retryable",
			"Recipe, fresh source grant and playback identity for one transport the node serves: an HLS session the node lost, or one progressive /remux request. The core verifies the stream token the relay forwarded (the node never does), checks the deny marker on the transport and on the playback session, binds the recipe to the calling node, and checks that the catalog still has the source before it mints the grant. Refusals carry a JSON reason. 503 is transient.",
			jsonBody(reflect.TypeFor[ResolveRequest]()), map[string]*huma.Response{
				"200": jsonResponse("Resolution.", reflect.TypeFor[Resolution]()),
				"403": refusal("invalid_token: a remux request without a valid token. wrong_route: the recipe or token belongs to another node, or the HLS recipe is bound to none."),
				"404": refusal("not_found: no recipe for the transport, or a token that does not describe it."),
				"410": refusal("denied: the session's deny marker is set. stopped: the remux authority is gone or no longer matches. source_changed: the source left the catalog or the theme file changed."),
			}, http.StatusBadRequest, http.StatusServiceUnavailable),
		op(http.MethodPost, nodesource.PathOpen, "HandleSourceOpen", "natural_idempotent",
			"Exchange a source grant for the approved source and the same claims valid for another hour. Every job input opens through it, in both source access modes, and the node calls it again every 20 minutes while the input is open. 403: the grant does not verify, has expired, or is another node's. 410: the catalog no longer has the source at the granted path. 412: its size or whole-second modification time changed. 503 is transient.",
			jsonBody(reflect.TypeFor[nodesource.OpenRequest]()), map[string]*huma.Response{
				"200": jsonResponse("Approved source and renewed grant.", reflect.TypeFor[nodesource.OpenResponse]()),
			}, http.StatusBadRequest, http.StatusForbidden, http.StatusGone, http.StatusPreconditionFailed, http.StatusServiceUnavailable),
		sourceRead(http.MethodGet, "GET returns the bytes."),
		sourceRead(http.MethodHead, "HEAD returns the same status and headers as GET, without the bytes."),
		subtitle,
		op(http.MethodPost, PathReport, "HandleReport", "non_retryable",
			"The node's sessions and stream telemetry, posted every second. Fenced on (boot_id, seq): an older or repeated pair gets 409 and writes nothing. The core files the report under the authenticated node, with its own clock, and replaces the node's session records and telemetry snapshot as a whole. 413: more than 2,000 sessions or transfers.",
			jsonBody(reflect.TypeFor[Report]()), map[string]*huma.Response{"204": {Description: "Accepted."}},
			http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusServiceUnavailable),
		op(http.MethodPost, PathLogs, "HandleLogs", "non_retryable",
			"A batch of the node's operational and FFmpeg log entries. The core stamps them with the authenticated node's identity (<name>#<id>), ignoring any node in the entries, clamps their timestamps to the last 24 hours and the next 30 seconds, and stores them for the admin log viewer. A replayed batch can duplicate entries. 413: more than 100 entries.",
			jsonBody(reflect.TypeFor[LogBatch]()), map[string]*huma.Response{"204": {Description: "Stored."}},
			http.StatusBadRequest, http.StatusNotFound, http.StatusRequestEntityTooLarge, http.StatusServiceUnavailable),
		op(http.MethodPost, PathHealthChanged, "HandleHealthChanged", "natural_idempotent",
			"The node's drain state changed (an admin drain request, SIGTERM, or drained). The core publishes node_drain_changed so every replica health-checks the node at once.",
			nil, map[string]*huma.Response{"204": {Description: "Published."}}, http.StatusServiceUnavailable),
	}
}
