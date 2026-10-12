package nodeenrollment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoreHandsOffBootstrapAndRecoversLostEnrollmentReply(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "lost-reply"}[lost], func(t *testing.T) {
			var bound atomic.Bool
			var transferred Binding
			s, err := NewServer(testToken, bindingStore(t), func(_ context.Context, b Binding) error { transferred = b; return nil })
			if err != nil {
				t.Fatal(err)
			}
			tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == ReceiptPath {
					if !bound.Load() {
						http.NotFound(w, r)
						return
					}
					if r.Header.Get("Authorization") != "Bearer customer-jwt-test" {
						w.WriteHeader(401)
						return
					}
					_ = json.NewEncoder(w).Encode(Receipt{BindingID: Identity(testBinding().SecretKey)})
					return
				}
				if lost {
					rec := httptest.NewRecorder()
					s.Handler().ServeHTTP(rec, r)
					if rec.Code != 200 {
						w.WriteHeader(rec.Code)
						return
					}
					bound.Store(true)
					h, ok := w.(http.Hijacker)
					if !ok {
						t.Error("no hijacker")
						return
					}
					conn, _, err := h.Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				s.Handler().ServeHTTP(w, r)
				bound.Store(true)
			}))
			defer tls.Close()
			c := Client{Bootstrap: testBinding(), JWTSecret: "customer-jwt-test", HTTPClient: tls.Client(), ReadyTimeout: time.Second}
			if _, err := c.Enroll(t.Context(), tls.URL, "Customer node", Input{Token: testToken}); err != nil {
				t.Fatal(err)
			}
			if transferred.DatabaseURL != testBinding().DatabaseURL || transferred.SecretKey != testBinding().SecretKey || transferred.NodeURL != tls.URL {
				t.Fatal("core did not transfer its bootstrap")
			}
			if _, err := c.Enroll(t.Context(), tls.URL, "Customer node", Input{Token: testToken}); err != nil {
				t.Fatalf("registration retry required re-binding: %v", err)
			}
		})
	}
}
func TestCoreRejectsUnsafeTargetsAndOtherCustomerBindings(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	c := Client{Bootstrap: testBinding(), JWTSecret: "test"}
	if _, err := c.Enroll(t.Context(), "file://"+strings.TrimPrefix(target.URL, "http://"), "node", Input{Token: testToken}); err == nil || calls.Load() != 0 {
		t.Fatal("bootstrap sent to unsupported URL scheme")
	}
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			t.Error("credentials sent to another customer")
		}
		_ = json.NewEncoder(w).Encode(Receipt{BindingID: Identity(strings.Repeat("other", 10))})
	}))
	defer tls.Close()
	c.HTTPClient = tls.Client()
	if _, err := c.Enroll(t.Context(), tls.URL, "node", Input{Token: testToken}); err == nil {
		t.Fatal("another customer's worker adopted")
	}
}
func TestEnrollmentDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Bool
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer receiver.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, receiver.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	c := Client{Bootstrap: testBinding(), JWTSecret: "test", HTTPClient: redirect.Client()}
	if _, err := c.Enroll(t.Context(), redirect.URL, "node", Input{Token: testToken}); err == nil || leaked.Load() {
		t.Fatal("enrollment followed a credential redirect")
	}
}

func TestInternalHTTPEnrollmentHandsOffAutomatically(t *testing.T) {
	var bound atomic.Bool
	s, err := NewServer(testToken, bindingStore(t), func(context.Context, Binding) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == ReceiptPath {
			if !bound.Load() {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(Receipt{BindingID: Identity(testBinding().SecretKey)})
			return
		}
		s.Handler().ServeHTTP(w, r)
		bound.Store(true)
	}))
	defer server.Close()
	c := Client{Bootstrap: testBinding(), JWTSecret: "customer-test", ReadyTimeout: time.Second}
	if _, err := c.Enroll(t.Context(), server.URL, "Internal node", Input{Token: testToken}); err != nil {
		t.Fatalf("internal HTTP enrollment failed: %v", err)
	}
}
