// Package listener exercises classification across HTTP helper boundaries.
package listener

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type handler struct{}
type unrelated struct{}

func (h *handler) socket(w http.ResponseWriter, r *http.Request) { h.socketHelper(w, r) }
func (h *handler) socketHelper(output http.ResponseWriter, input *http.Request) {
	upgrader := websocket.Upgrader{}
	_, _ = upgrader.Upgrade(output, input, nil)
}
func (h *handler) upload(w http.ResponseWriter, r *http.Request)   { uploadHelper(r) }
func uploadHelper(input *http.Request)                             { (&handler{}).multipart(input) }
func (h *handler) multipart(input *http.Request)                   { _, _, _ = input.FormFile("file") }
func (h *handler) redirect(w http.ResponseWriter, r *http.Request) { h.redirectHelper(w, r) }
func (h *handler) redirectHelper(output http.ResponseWriter, input *http.Request) {
	http.Redirect(output, input, "/target", http.StatusFound)
}
func (h *handler) download(w http.ResponseWriter, r *http.Request) {
	upstream := &http.Response{Body: io.NopCloser(strings.NewReader("remote"))}
	stream(w, upstream.Body)
}
func stream(output io.Writer, input io.Reader) { _, _ = io.Copy(output, input) }
func (h *handler) binary(w http.ResponseWriter, r *http.Request) {
	body := r.Body
	(&handler{}).binaryHelper(body)
}
func (h *handler) binaryHelper(body io.Reader)                 { stream(io.Discard, body) }
func (h *handler) json(w http.ResponseWriter, r *http.Request) { readJSON(w, r.Body) }
func readJSON(output io.Writer, input io.Reader) {
	body, _ := io.ReadAll(input)
	var value any
	_ = json.Unmarshal(body, &value)
	_ = json.NewEncoder(output).Encode(value)
}
func (h *handler) upstreamJSON(w http.ResponseWriter, r *http.Request) {
	response := &http.Response{Body: io.NopCloser(strings.NewReader("{}"))}
	readJSON(io.Discard, response.Body)
}
func (h *handler) upstreamHeaders(w http.ResponseWriter, r *http.Request) {
	response := &http.Response{Header: make(http.Header)}
	response.Header.Set("Content-Type", "text/html")
	request, _ := http.NewRequest(http.MethodPost, "https://example.invalid", strings.NewReader("{}"))
	_, _ = io.ReadAll(request.Body)
}
func (h *handler) eventStream(w http.ResponseWriter, r *http.Request) { eventHelper(w) }
func eventHelper(output http.ResponseWriter) {
	header := output.Header()
	header.Set("Content-Type", "text/event-stream")
}
func (h *handler) recursive(w http.ResponseWriter, r *http.Request) { recurseA(w, r) }
func recurseA(w http.ResponseWriter, r *http.Request)               { recurseB(w, r) }
func recurseB(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/again" {
		recurseA(w, r)
	}
	http.ServeFile(w, r, "fixture.bin")
}

// Same spelling does not make this unrelated method a redirect helper.
func (u *unrelated) redirectHelper(output http.ResponseWriter, input *http.Request) {
	_ = json.NewEncoder(output).Encode(nil)
}
func (h *handler) otherReceiver(w http.ResponseWriter, r *http.Request) {
	(&unrelated{}).redirectHelper(w, r)
}
func (h *handler) guarded(w http.ResponseWriter, r *http.Request) {
	output, input := guard(w, r)
	w, r = output, input
	data, _ := readBounded(w, r)
	var value any
	_ = json.Unmarshal(data, &value)
	_ = json.NewEncoder(w).Encode(value)
}
func guard(output http.ResponseWriter, input *http.Request) (http.ResponseWriter, *http.Request) {
	return output, input.Clone(input.Context())
}
func readBounded(output http.ResponseWriter, input *http.Request) ([]byte, error) {
	return buffered(http.MaxBytesReader(output, input.Body, 1024))
}
func buffered(input io.Reader) ([]byte, error) {
	var buffer bytes.Buffer
	_, err := buffer.ReadFrom(input)
	return buffer.Bytes(), err
}
func (h *handler) freshRequest(w http.ResponseWriter, r *http.Request) {
	input := replaceRequest(r)
	_, _ = io.ReadAll(input.Body)
}
func replaceRequest(input *http.Request) *http.Request {
	fresh, _ := http.NewRequest(http.MethodPost, "https://example.invalid", strings.NewReader("data"))
	return fresh
}
func (h *handler) methodExpression(w http.ResponseWriter, r *http.Request) {
	(*handler).redirectHelper(h, w, r)
}
func (h *handler) upstreamDecoded(w http.ResponseWriter, r *http.Request) {
	response := &http.Response{Body: io.NopCloser(strings.NewReader("{}"))}
	var value any
	_ = json.NewDecoder(response.Body).Decode(&value)
	_ = json.NewEncoder(w).Encode(value)
}

type wrappedWriter struct{ http.ResponseWriter }

func wrap(output http.ResponseWriter) http.ResponseWriter {
	return &wrappedWriter{ResponseWriter: output}
}
func (h *handler) wrapped(w http.ResponseWriter, r *http.Request) {
	writer := wrap(w)
	http.ServeFile(writer, r, "fixture.bin")
}
func (h *handler) closure(w http.ResponseWriter, r *http.Request) {
	redirect := func(target string) { http.Redirect(w, r, target, http.StatusFound) }
	redirect("/target")
}
func (h *handler) uncalled(w http.ResponseWriter, r *http.Request) {
	redirect := func() { http.Redirect(w, r, "/target", http.StatusFound) }
	_ = redirect
}
func (h *handler) local(w http.ResponseWriter, r *http.Request) { h.delivery(w, r, false) }
func (h *handler) proxy(w http.ResponseWriter, r *http.Request) { h.delivery(w, r, true) }
func (h *handler) delivery(w http.ResponseWriter, r *http.Request, delegate bool) {
	allowProxy := delegate
	deliver(w, r, allowProxy)
}
func deliver(w http.ResponseWriter, r *http.Request, delegate bool) {
	if delegate && r.URL.Query().Get("device_id") != "" {
		http.Redirect(w, r, "/proxy", http.StatusTemporaryRedirect)
	} else {
		http.ServeFile(w, r, "fixture.bin")
	}
}
func NewRouter() http.Handler { return sealedHandler{h: newRouter()} }

type sealedHandler struct{ h http.Handler }

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }
func newRouter() chi.Router {
	r := chi.NewRouter()
	h := &handler{}
	r.Get("/local", h.local)
	r.Head("/local", h.local)
	r.Get("/proxy", h.proxy)
	r.Head("/proxy", h.proxy)
	r.Get("/wrapped", h.wrapped)
	r.Get("/closure", h.closure)
	r.Get("/uncalled", h.uncalled)
	r.Post("/guarded", h.guarded)
	r.Get("/fresh-request", h.freshRequest)
	r.Get("/method-expression", h.methodExpression)
	r.Get("/upstream-decoded", h.upstreamDecoded)
	r.Get("/socket", h.socket)
	r.Post("/upload", h.upload)
	r.Get("/redirect", h.redirect)
	r.Get("/download", h.download)
	r.Post("/binary", h.binary)
	r.Post("/json", h.json)
	r.Get("/upstream-json", h.upstreamJSON)
	r.Get("/upstream-headers", h.upstreamHeaders)
	r.Get("/events", h.eventStream)
	r.Get("/recursive", h.recursive)
	r.Get("/other-receiver", h.otherReceiver)
	r.Post("/literal", func(w http.ResponseWriter, r *http.Request) { readJSON(w, r.Body) })
	return r
}
