package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func testClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Client{
		httpClient: server.Client(),
		apiBase:    server.URL,
		limiter:    rate.NewLimiter(rate.Inf, 1),
	}
}

func TestExchangeCodeAndGetUser(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "client-id" || pass != "client-secret" {
				t.Errorf("missing or wrong basic auth: %s/%s", user, pass)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code") != "the-code" {
				t.Errorf("unexpected form: %v", r.PostForm)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "user-token"})
		case "/users/@me":
			if got := r.Header.Get("Authorization"); got != "Bearer user-token" {
				t.Errorf("unexpected authorization %q", got)
			}
			_ = json.NewEncoder(w).Encode(User{ID: "42", Username: "quick"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	token, err := client.ExchangeCode(context.Background(), "client-id", "client-secret", "the-code", "https://silo.example/cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	user, err := client.GetUser(context.Background(), token)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.ID != "42" || user.Username != "quick" {
		t.Fatalf("unexpected user %+v", user)
	}
}

func TestOpenDMChannelAndSendDM(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/@me/channels":
			if got := r.Header.Get("Authorization"); got != "Bot bot-token" {
				t.Errorf("unexpected authorization %q", got)
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["recipient_id"] != "42" {
				t.Errorf("unexpected recipient %q", body["recipient_id"])
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "dm-123"})
		case "/channels/dm-123/messages":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	channelID, err := client.OpenDMChannel(context.Background(), "bot-token", "42")
	if err != nil {
		t.Fatalf("open dm: %v", err)
	}
	if channelID != "dm-123" {
		t.Fatalf("unexpected channel id %q", channelID)
	}
	if err := client.SendDM(context.Background(), "bot-token", channelID, []byte(`{"content":"hi"}`)); err != nil {
		t.Fatalf("send dm: %v", err)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"dm blocked", http.StatusForbidden, `{"code":50007,"message":"Cannot send messages to this user"}`, ErrDMBlocked},
		{"bad token", http.StatusUnauthorized, `{"message":"401: Unauthorized"}`, ErrUnauthorized},
		{"rate limited", http.StatusTooManyRequests, `{"message":"You are being rate limited."}`, ErrRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			err := client.SendDM(context.Background(), "bot-token", "dm-123", []byte(`{}`))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLimiterHonorsContextCancel(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Exhausted limiter with a long refill forces Wait to block on the context.
	client.limiter = rate.NewLimiter(rate.Every(time.Hour), 1)
	client.limiter.Allow()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := client.SendDM(ctx, "bot-token", "dm-123", []byte(`{}`)); err == nil {
		t.Fatal("expected context error from limiter wait")
	}
}
