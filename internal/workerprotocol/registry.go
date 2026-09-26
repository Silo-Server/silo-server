package workerprotocol

import (
	"net/http"
	"reflect"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
)

// Operation describes a retained worker HTTP operation. Listener is part of its identity:
// /status on a proxy is a different contract from /status on a transcode node.
// These descriptions never register a route on the native API listener.
type Operation struct {
	Parameters  []*huma.Param             `json:"parameters,omitempty"`
	RequestBody *huma.RequestBody         `json:"requestBody,omitempty"`
	Description string                    `json:"description,omitempty"`
	RetrySafety string                    `json:"retry_safety,omitempty"`
	Listener    string                    `json:"listener"`
	Method      string                    `json:"method"`
	Path        string                    `json:"path"`
	Handler     string                    `json:"handler"`
	AuthClass   string                    `json:"auth_class"`
	Responses   map[string]*huma.Response `json:"responses"`
}

// JSONRead uses the owning handler's actual response type. Worker errors retain
// their text/plain bodies; they are not native API Problem Details responses.
func JSONRead[T any](schemas huma.Registry, listener, path, handler string, failures ...int) Operation {
	responses := map[string]*huma.Response{
		"200": {Description: "OK", Content: map[string]*huma.MediaType{
			"application/json": {Schema: schemas.Schema(reflect.TypeFor[T](), true, "")},
		}},
	}
	for _, status := range failures {
		responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{
			"text/plain": {Schema: &huma.Schema{Type: "string"}},
		}}
	}
	return Operation{Listener: listener, Method: http.MethodGet, Path: path, Handler: handler, AuthClass: "node_bearer", Responses: responses}
}

// EmptyCommand describes a retained bodyless command with text/plain failures.
// No command has a durable replay receipt; uncertain results require observation.
func EmptyCommand(listener, path, handler, description string, failures ...int) Operation {
	op := Operation{Listener: listener, Path: path, Handler: handler, AuthClass: "node_bearer"}
	op.Method = http.MethodPost
	op.Description = description
	op.RetrySafety = "non_retryable"
	op.Responses = map[string]*huma.Response{
		"204": {Description: "No Content"},
	}
	for _, status := range append([]int{401, 500}, failures...) {
		op.Responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{
			"text/plain": {Schema: &huma.Schema{Type: "string"}},
		}}
	}
	return op
}
