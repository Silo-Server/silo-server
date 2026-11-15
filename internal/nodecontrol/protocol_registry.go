package nodecontrol

import (
	"maps"
	"net/http"
	"reflect"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/nodesource"
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
		inHeader  = "header"
	)
	jsonBody := func(t reflect.Type) *huma.RequestBody {
		return &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{jsonMedia: {Schema: schemas.Schema(t, true, "")}}}
	}
	text := func(status int) *huma.Response {
		return &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{textMedia: {Schema: &huma.Schema{Type: huma.TypeString}}}}
	}
	refusal := func(status int) *huma.Response {
		return &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{jsonMedia: {Schema: schemas.Schema(reflect.TypeFor[ResolveRefusal](), true, "")}}}
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
	config.Parameters = []*huma.Param{{Name: headerIfNoneMatch, In: inHeader, Schema: &huma.Schema{Type: huma.TypeString}}}
	sourceParams := []*huma.Param{
		{Name: nodesource.HeaderSourceGrant, In: inHeader, Required: true, Description: "The source grant, as sources/open last returned it. A header, so access logs never record it; the node's signature covers it.", Schema: &huma.Schema{Type: huma.TypeString}},
		{Name: headerRange, In: inHeader, Description: "One byte range or suffix range. Several ranges are refused with 416.", Schema: &huma.Schema{Type: huma.TypeString}},
		{Name: headerIfMatch, In: inHeader, Description: "The grant's entity tag; another answers 412. If-None-Match, If-Modified-Since, If-Unmodified-Since and If-Range are ignored.", Schema: &huma.Schema{Type: huma.TypeString}},
	}
	sourceBytesDescription := "The bytes of the source a grant approves, for a node that reads its sources through the core (source_access core). The grant must verify, be unexpired, be younger than its 24-hour whole life and name the calling node (403). No catalog read: the catalog is checked at open and every renewal. The core opens the granted path through fspath, fstats the descriptor and serves from it: gone or not a regular file is 410, another size or modification time (compared at microseconds) is 412, so a file replaced after the check is never served. Identity encoding, Accept-Ranges bytes, the grant's ETag; 206 with Content-Range for a range, 416 with Content-Range bytes */size and the ETag past the end. Every answer is Cache-Control no-store. Exempt from the per-node request budgets and from the request and activity logs; the write deadline rolls with progress. 429 with Retry-After: the node has 512 source responses in flight on this server. Responses in flight are cut when the server begins a graceful shutdown; the node resumes from its offset. 503: no grant key, or the server is shutting down."
	sourceBytes := op(http.MethodGet, PathSources, "HandleSource", "natural_idempotent", sourceBytesDescription, nil,
		map[string]*huma.Response{
			"200": {Description: "The whole source.", Content: map[string]*huma.MediaType{"application/octet-stream": {Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}}},
			"206": {Description: "One range of the source.", Content: map[string]*huma.MediaType{"application/octet-stream": {Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}}},
		}, http.StatusForbidden, http.StatusGone, http.StatusPreconditionFailed, http.StatusRequestedRangeNotSatisfiable, http.StatusTooManyRequests, http.StatusServiceUnavailable)
	sourceBytes.Parameters = sourceParams
	sourceHead := op(http.MethodHead, PathSources, "HandleSource", "natural_idempotent", sourceBytesDescription, nil,
		map[string]*huma.Response{"200": {Description: "The source's length and ETag, without a body."}},
		http.StatusForbidden, http.StatusGone, http.StatusPreconditionFailed, http.StatusRequestedRangeNotSatisfiable, http.StatusTooManyRequests, http.StatusServiceUnavailable)
	sourceHead.Parameters = sourceParams
	return []workerprotocol.Operation{
		config,
		sourceBytes,
		sourceHead,
		op(http.MethodPost, PathReport, "HandleReport", "non_retryable",
			"The node's sessions and stream telemetry, posted every second. Fenced on (boot_id, seq): an older or repeated pair gets 409 and writes nothing. The core files the report under the authenticated node, with its own clock, and replaces the node's session records and telemetry snapshot as a whole. 413: more than 2,000 sessions or transfers. 429: the node exceeded its request budget on this server.",
			jsonBody(reflect.TypeFor[Report]()), map[string]*huma.Response{"204": {Description: "Accepted, or dropped by a core without Redis."}},
			http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusTooManyRequests, http.StatusServiceUnavailable),
		op(http.MethodPost, PathLogs, "HandleLogs", "non_retryable",
			"A batch of the node's operational and FFmpeg log entries. The core stamps them with the authenticated node (<name>#<id>), ignoring any node in the entries, clamps their timestamps to the last 24 hours and the next 30 seconds, and stores them for the admin log viewer and its live tail. A replayed batch can duplicate entries. 413: more than 100 entries. 429: the node exceeded its request budget, or its log byte budget (128 KiB a second, bursts of 4 MiB), on this server.",
			jsonBody(reflect.TypeFor[LogBatch]()), map[string]*huma.Response{"204": {Description: "Stored."}},
			http.StatusBadRequest, http.StatusNotFound, http.StatusRequestEntityTooLarge, http.StatusTooManyRequests, http.StatusServiceUnavailable),
		op(http.MethodPost, PathHealthChanged, "HandleHealthChanged", "natural_idempotent",
			"The node's drain state changed (an admin drain request, SIGTERM, or drained). The core publishes node_drain_changed so every API server health-checks the node at once. 429: the node exceeded its request budget on this server.",
			nil, map[string]*huma.Response{"204": {Description: "Published."}}, http.StatusTooManyRequests, http.StatusServiceUnavailable),
		op(http.MethodPost, PathRevokeRecipes, "HandleRevokeRecipes", "natural_idempotent",
			"The node, tearing its sessions down for a force reload, withdraws every recipe bound to it. The core revokes by the authenticated node id and then by the node's registered URL, never by anything in the body. 204 also from a core without Redis, which holds no recipes. 404: the node row is gone. 429: the node exceeded its request budget on this server. 503 is transient; the node retries the teardown.",
			nil, map[string]*huma.Response{"204": {Description: "Revoked."}}, http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable),
		op(http.MethodPost, PathResolve, "HandleResolve", "natural_idempotent",
			"The recipe and playback identity of one transport the calling node serves, for rebuilding a lost HLS transcode (purpose hls) or serving one progressive remux request (purpose remux). stream_token is the token a relay forwarded, passed through unread. The core checks the deny markers of the transport and its playback session, verifies the token, picks the recipe (a complete stored card over a complete token card, for hls) and requires it to be bound to the calling node by execution node id or, without one, by canonical node URL. It then requires the catalog to still have the recipe's exact media file (by id, not missing, at exactly the recipe's path, a regular file) or its theme in an enabled library, and returns a fresh source_grant for it naming the calling node. A refusal carries its reason as JSON: 404 not_found, 410 denied, stopped or source_changed, 403 invalid_token or wrong_route. 503 is transient: the core cannot verify tokens, mint grants, or read the recipe store, the catalog or the node's registration right now.",
			jsonBody(reflect.TypeFor[ResolveRequest]()), map[string]*huma.Response{
				"200": {Description: "Resolution.", Content: map[string]*huma.MediaType{jsonMedia: {Schema: schemas.Schema(reflect.TypeFor[Resolution](), true, "")}}},
				"403": refusal(http.StatusForbidden), "404": refusal(http.StatusNotFound), "410": refusal(http.StatusGone),
			}, http.StatusBadRequest, http.StatusServiceUnavailable),
		op(http.MethodPost, PathSourcesOpen, "HandleSourceOpen", "natural_idempotent",
			"Exchange a source grant for the source it approves, when a job's input opens and at every 20-minute renewal. The grant must verify, be unexpired and name the calling node (403). The catalog must still have the source: a media file by id, not missing, at exactly the granted path, or a theme by id at exactly that path in an enabled library (410). The core stats the path through fspath, following symlinks: gone or not a regular file is 410, another size or modification time (compared at microseconds) is 412. 200 returns the same claims as a grant valid for another hour, with the approved path, size, mtime_ns and etag, Cache-Control no-store. 400: no grant in the body. 503 is transient: no grant key, or the catalog cannot be read.",
			jsonBody(reflect.TypeFor[nodesource.OpenRequest]()), map[string]*huma.Response{
				"200": {Description: "Approved source and renewed grant.", Content: map[string]*huma.MediaType{jsonMedia: {Schema: schemas.Schema(reflect.TypeFor[nodesource.OpenResponse](), true, "")}}},
			}, http.StatusBadRequest, http.StatusForbidden, http.StatusGone, http.StatusPreconditionFailed, http.StatusServiceUnavailable),
	}
}
