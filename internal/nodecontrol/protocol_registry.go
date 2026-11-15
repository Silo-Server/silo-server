package nodecontrol

import (
	"maps"
	"net/http"
	"reflect"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/workerprotocol"
)

// Protocol describes the node control routes for the x-silo-worker-protocols
// registry. They are served by the API listener, but only transcode nodes call
// them, signing with their own key (RequireNode), so they have no /api/v2
// operation. Failures are plain text.
func Protocol(schemas huma.Registry) []workerprotocol.Operation {
	const (
		listener  = "api"
		authClass = "node_signed"
		jsonMedia = "application/json"
		textMedia = "text/plain"
		handler   = "(*internal/nodecontrol.Server)."
	)
	jsonBody := func(t reflect.Type) *huma.RequestBody {
		return &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{jsonMedia: {Schema: schemas.Schema(t, true, "")}}}
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
	config := op(http.MethodGet, PathConfig, "HandleConfig", "",
		"The calling node's configuration document: the server settings it runs with, its own acceleration and download overrides applied. Revision, also the strong ETag, is the SHA-256 of the rest of the document. If-None-Match naming it answers 304. 404: the node row is gone. 503 is transient.",
		nil, map[string]*huma.Response{
			"200": {Description: "Configuration document.", Content: map[string]*huma.MediaType{jsonMedia: {Schema: schemas.Schema(reflect.TypeFor[NodeConfig](), true, "")}}},
			"304": {Description: "Revision unchanged."},
		}, http.StatusNotFound, http.StatusServiceUnavailable)
	config.Parameters = []*huma.Param{{Name: headerIfNoneMatch, In: "header", Schema: &huma.Schema{Type: huma.TypeString}}}
	return []workerprotocol.Operation{
		config,
		op(http.MethodPost, PathReport, "HandleReport", "non_retryable",
			"The node's sessions and stream telemetry, posted every second. Fenced on (boot_id, seq): an older or repeated pair gets 409 and writes nothing. The core files the report under the authenticated node, with its own clock, and replaces the node's session records and telemetry snapshot as a whole. 413: more than 2,000 sessions or transfers. 429: the node exceeded its request budget on this server.",
			jsonBody(reflect.TypeFor[Report]()), map[string]*huma.Response{"204": {Description: "Accepted, or dropped by a core without Redis."}},
			http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusTooManyRequests, http.StatusServiceUnavailable),
		op(http.MethodPost, PathLogs, "HandleLogs", "non_retryable",
			"A batch of the node's operational and FFmpeg log entries. The core stamps them with the authenticated node (<name>#<id>), ignoring any node in the entries, clamps their timestamps to the last 24 hours and the next 30 seconds, and stores them for the admin log viewer and its live tail. A replayed batch can duplicate entries. 413: more than 100 entries. 429: the node exceeded its request budget on this server.",
			jsonBody(reflect.TypeFor[LogBatch]()), map[string]*huma.Response{"204": {Description: "Stored."}},
			http.StatusBadRequest, http.StatusNotFound, http.StatusRequestEntityTooLarge, http.StatusTooManyRequests, http.StatusServiceUnavailable),
		op(http.MethodPost, PathHealthChanged, "HandleHealthChanged", "natural_idempotent",
			"The node's drain state changed (an admin drain request, SIGTERM, or drained). The core publishes node_drain_changed so every API server health-checks the node at once. 429: the node exceeded its request budget on this server.",
			nil, map[string]*huma.Response{"204": {Description: "Published."}}, http.StatusTooManyRequests, http.StatusServiceUnavailable),
	}
}
