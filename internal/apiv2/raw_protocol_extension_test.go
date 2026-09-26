package apiv2

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/danielgtaylor/huma/v2"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
)

func rawProtocolFixture(method, protocol string, class Class) RawOperation {
	raw := rawFixture(method, class)
	raw.Path = Prefix + "/raw-protocol"
	raw.OperationID = "rawProtocolFixture"
	raw.Parameters = nil
	raw.Protocol = protocol
	raw.Reason = "Protocol exchange retains its HTML, redirect or WebSocket response."
	switch protocol {
	case "html-callback":
		raw.RetrySafety = RetrySafetyNonRetryable
		raw.Responses = map[string]*huma.Response{"200": {Description: "HTML result", Content: map[string]*huma.MediaType{"text/html": {Schema: &huma.Schema{Type: "string"}}}}}
	case "redirect":
		raw.Responses = map[string]*huma.Response{"302": {Description: "OAuth result redirect", Headers: map[string]*huma.Header{"Location": {Schema: &huma.Schema{Type: "string"}}}, Content: map[string]*huma.MediaType{"text/html": {Schema: &huma.Schema{Type: "string"}}}}}
	case "websocket":
		headers := map[string]*huma.Header{}
		for _, name := range []string{"Connection", "Upgrade", "Sec-WebSocket-Accept"} {
			headers[name] = &huma.Header{Schema: &huma.Schema{Type: "string"}}
		}
		raw.Responses = map[string]*huma.Response{"101": {Description: "WebSocket connection", Headers: headers}, "400": {Description: "Invalid upgrade request"}}
	}
	return raw
}

func TestRawProtocolPostHTMLAndRetryDeclaration(t *testing.T) {
	calls := 0
	handler := NewHandler(Dependencies{testRegister: func(reg *Registry) {
		RegisterRaw(reg, rawProtocolFixture(http.MethodPost, "html-callback", ClassPublic), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != "List-Unsubscribe=One-Click" || r.URL.Query().Get("token") != "synthetic" {
				t.Error("raw form request changed")
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<html>Unsubscribed</html>")
		}))
		op := reg.api.OpenAPI().Paths[Prefix+"/raw-protocol"].Post
		if op.Extensions[extRetrySafety] != string(RetrySafetyNonRetryable) || op.Metadata[metaRetrySafety] != string(RetrySafetyNonRetryable) || op.Responses["200"].Content["text/html"] == nil {
			t.Fatal("missing raw POST contract")
		}
		found := false
		for _, declared := range reg.Declared() {
			if declared.OperationID == "rawProtocolFixture" {
				found = true
				if declared.RetrySafety != RetrySafetyNonRetryable {
					t.Fatal("raw POST lost ledger retry classification", declared)
				}
			}
		}
		if !found {
			t.Fatal("raw POST missing from declared operation registry")
		}
	}})
	response := do(t, handler, http.MethodPost, Prefix+"/raw-protocol?token=synthetic", "List-Unsubscribe=One-Click", map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Accept": "text/html"})
	if response.Code != 200 || response.Body.String() != "<html>Unsubscribed</html>" || calls != 1 {
		t.Fatal(response.Code, response.Body.String(), calls)
	}
}

func TestRawProtocolRedirectOnly(t *testing.T) {
	handler := NewHandler(Dependencies{testRegister: func(reg *Registry) {
		RegisterRaw(reg, rawProtocolFixture(http.MethodGet, "redirect", ClassPublic), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/completed?state=synthetic", http.StatusFound)
		}))
		op := reg.api.OpenAPI().Paths[Prefix+"/raw-protocol"].Get
		if op.Responses["200"] != nil || op.Responses["302"].Headers["Location"] == nil {
			t.Fatal("redirect invented a 200 or lost Location")
		}
	}})
	response := do(t, handler, http.MethodGet, Prefix+"/raw-protocol?state=synthetic", "", map[string]string{"Accept": "text/html"})
	if response.Code != 302 || response.Header().Get("Location") != "/completed?state=synthetic" {
		t.Fatal(response.Code, response.Header())
	}
}

func TestRawProtocolWebSocketRealUpgradeAndAuthorization(t *testing.T) {
	deps := parityDeps(false)
	var calls atomic.Int32
	completed := make(chan struct{})
	deps.testRegister = func(reg *Registry) {
		RegisterRaw(reg, rawProtocolFixture(http.MethodGet, "websocket", ClassProfileScoped), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if apimw.GetUserID(r.Context()) != 1 || apimw.GetProfileID(r.Context()) != "p-owner" {
				t.Error("upgrade lost authorized profile")
			}
			upgrader := websocket.Upgrader{}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			defer close(completed)
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			kind, payload, err := conn.ReadMessage()
			if err != nil {
				t.Error(err)
				return
			}
			if err := conn.WriteMessage(kind, payload); err != nil {
				t.Error(err)
			}
		}))
		op := reg.api.OpenAPI().Paths[Prefix+"/raw-protocol"].Get
		if op.Responses["200"] != nil || len(op.Responses["101"].Content) != 0 {
			t.Fatal("upgrade has a fake response body")
		}
	}
	labels := prometheus.Labels{"api_major": "2", "operation_id": "rawProtocolFixture", "method": "GET", "status_class": "hijacked", "error_code": "none", "auth_class": "session", "client": "none"}
	before := counterValue(t, requestsTotal, labels)
	observed := make(chan struct{}, 4)
	handler := NewHandler(deps)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		observed <- struct{}{}
	}))
	defer server.Close()
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	url := "ws" + strings.TrimPrefix(server.URL, "http") + Prefix + "/raw-protocol"
	for _, tc := range []struct {
		token, profile string
		status         int
	}{{"invalid", "p-owner", 401}, {memberToken, "p-locked", 403}, {memberToken, "p-other", 404}} {
		headers := http.Header{"Authorization": {"Bearer " + tc.token}, "X-Profile-Id": {tc.profile}}
		conn, response, err := dialer.Dial(url, headers)
		if conn != nil {
			conn.Close()
			t.Fatal("unauthorized upgrade succeeded")
		}
		if err == nil || response == nil {
			t.Fatal("expected handshake rejection", err)
		}
		response.Body.Close()
		if response.StatusCode != tc.status || calls.Load() != 0 {
			t.Fatal(response.StatusCode, calls.Load())
		}
	}
	conn, response, err := dialer.Dial(url, http.Header{"Authorization": {"Bearer " + memberToken}, "X-Profile-Id": {"p-owner"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if response.StatusCode != 101 || response.Header.Get("Upgrade") != "websocket" || response.Header.Get("Sec-WebSocket-Accept") == "" {
		t.Fatal(response.StatusCode, response.Header)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	payload := []byte{0, 1, 2, 255}
	if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		t.Fatal(err)
	}
	kind, echo, err := conn.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || string(echo) != string(payload) {
		t.Fatal(kind, echo, err)
	}
	select {
	case <-completed:
	case <-time.After(3 * time.Second):
		t.Fatal("upgrade handler did not finish")
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	for range 4 {
		select {
		case <-observed:
		case <-time.After(3 * time.Second):
			t.Fatal("request observation did not complete")
		}
	}
	if got := counterValue(t, requestsTotal, labels); got != before+1 {
		t.Fatalf("hijacked request counted as abandoned: %v -> %v", before, got)
	}
}

func TestRawProtocolDeclarationsRejectInvalidHandshakes(t *testing.T) {
	for name, change := range map[string]func(*RawOperation){
		"missing_retry": func(r *RawOperation) { r.RetrySafety = "" },
		"json_post": func(r *RawOperation) {
			r.Responses["200"].Content = map[string]*huma.MediaType{"application/json": {Schema: &huma.Schema{Type: "string"}}}
		},
		"json_suffix": func(r *RawOperation) {
			r.Responses["200"].Content = map[string]*huma.MediaType{"application/example+json": {Schema: &huma.Schema{Type: "string"}}}
		},
		"undocumented_body": func(r *RawOperation) { r.Responses["200"].Content = nil },
		"redirect_location": func(r *RawOperation) {
			*r = rawProtocolFixture(http.MethodGet, "redirect", ClassPublic)
			r.Responses["302"].Headers = nil
		},
		"upgrade_body": func(r *RawOperation) {
			*r = rawProtocolFixture(http.MethodGet, "websocket", ClassPublic)
			r.Responses["101"].Content = map[string]*huma.MediaType{"text/plain": {Schema: &huma.Schema{Type: "string"}}}
		},
		"upgrade_headers": func(r *RawOperation) {
			*r = rawProtocolFixture(http.MethodGet, "websocket", ClassPublic)
			delete(r.Responses["101"].Headers, "Sec-WebSocket-Accept")
		},
		"upgrade_protocol": func(r *RawOperation) {
			*r = rawProtocolFixture(http.MethodGet, "websocket", ClassPublic)
			r.Protocol = "byte-range"
		},
		"upgrade_method": func(r *RawOperation) {
			*r = rawProtocolFixture(http.MethodPost, "websocket", ClassPublic)
			r.RetrySafety = RetrySafetyNonRetryable
		},
		"upgrade_success": func(r *RawOperation) {
			*r = rawProtocolFixture(http.MethodGet, "websocket", ClassPublic)
			delete(r.Responses, "101")
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid raw protocol accepted")
				}
			}()
			NewHandler(Dependencies{testRegister: func(reg *Registry) {
				raw := rawProtocolFixture(http.MethodPost, "html-callback", ClassPublic)
				change(&raw)
				RegisterRaw(reg, raw, http.NotFoundHandler())
			}})
		})
	}
}
