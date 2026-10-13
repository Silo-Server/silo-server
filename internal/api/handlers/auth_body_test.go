package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// oversizedAuthBody is a syntactically valid JSON object whose single string
// field exceeds maxAuthRequestBodyBytes, the shape an anonymous caller would
// use to make the decoder buffer a huge value.
func oversizedAuthBody() *bytes.Reader {
	var b bytes.Buffer
	b.WriteString(`{"username":"`)
	b.WriteString(strings.Repeat("a", maxAuthRequestBodyBytes+1))
	b.WriteString(`"}`)
	return bytes.NewReader(b.Bytes())
}

func TestPublicAuthHandlersRejectOversizedBody(t *testing.T) {
	authHandler := &AuthHandler{device: &auth.DeviceLoginService{}}
	invitationHandler := &InvitationHandler{}

	cases := []struct {
		name    string
		path    string
		handler http.HandlerFunc
	}{
		{"login", "/api/v1/auth/login", authHandler.HandleLogin},
		{"setup", "/api/v1/auth/setup", authHandler.HandleSetup},
		{"refresh", "/api/v1/auth/refresh", authHandler.HandleRefresh},
		{"signup", "/api/v1/auth/signup", authHandler.HandleSignup},
		{"device start", "/api/v1/auth/device/start", authHandler.HandleDeviceStart},
		{"device poll", "/api/v1/auth/device/poll", authHandler.HandleDevicePoll},
		{"invitation accept", "/api/v1/invitations/tok/accept", invitationHandler.HandleAcceptInvitation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, oversizedAuthBody())
			w := httptest.NewRecorder()
			// The handlers' services are unset: reaching them would panic,
			// so a 413 also proves the request stopped at the decoder.
			tc.handler(w, r)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413; body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"too_large"`) {
				t.Fatalf("body = %s, want too_large error code", w.Body.String())
			}
		})
	}
}

func TestDecodeAuthRequestBody(t *testing.T) {
	type payload struct {
		Username string `json:"username"`
	}
	t.Run("normal body decodes", func(t *testing.T) {
		var got payload
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"admin"}`))
		w := httptest.NewRecorder()
		if !decodeAuthRequestBody(w, r, &got, false) || got.Username != "admin" {
			t.Fatalf("decode = %+v, status %d", got, w.Code)
		}
	})
	t.Run("body at the limit decodes", func(t *testing.T) {
		name := strings.Repeat("a", maxAuthRequestBodyBytes-len(`{"username":""}`))
		var got payload
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"`+name+`"}`))
		w := httptest.NewRecorder()
		if !decodeAuthRequestBody(w, r, &got, false) || got.Username != name {
			t.Fatalf("decode at limit failed: status %d", w.Code)
		}
	})
	t.Run("malformed body is 400", func(t *testing.T) {
		var got payload
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{`))
		w := httptest.NewRecorder()
		if decodeAuthRequestBody(w, r, &got, false) || w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
	t.Run("empty body", func(t *testing.T) {
		var got payload
		w := httptest.NewRecorder()
		if !decodeAuthRequestBody(w, httptest.NewRequest(http.MethodPost, "/", http.NoBody), &got, true) {
			t.Fatalf("allowEmpty rejected an empty body: %d", w.Code)
		}
		w = httptest.NewRecorder()
		if decodeAuthRequestBody(w, httptest.NewRequest(http.MethodPost, "/", http.NoBody), &got, false) || w.Code != http.StatusBadRequest {
			t.Fatalf("empty body without allowEmpty: status %d, want 400", w.Code)
		}
	})
}
