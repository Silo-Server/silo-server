package ratelimit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

func TestQuotaBucket(t *testing.T) {
	q := SessionQuotaConfig{Mode: SessionQuotaEnforce, RequestsPerHour: 3600, Burst: 3} // one token a second
	start := time.Unix(1_800_000_000, 0)
	ml := &MemoryQuotaLimiter{buckets: map[string]*quotaBucket{}}
	take := func(at time.Duration) AllowResult { return ml.takeAt("k", q, start.Add(at)) }

	for i := range 3 {
		if got := take(0); !got.Allowed {
			t.Fatalf("take %d of a full bucket = %+v, want allowed", i, got)
		}
	}
	refused := take(0)
	if refused.Allowed || refused.RetryAfter != time.Second {
		t.Fatalf("take from an empty bucket = %+v, want refused with a one-second retry", refused)
	}
	// A clock that steps back must not refill or drain the bucket.
	if got := take(-time.Hour); got.Allowed {
		t.Fatalf("take after the clock stepped back = %+v, want refused", got)
	}
	if got := take(time.Second); !got.Allowed {
		t.Fatalf("take after one second of refill = %+v, want allowed", got)
	}
	// A long idle refills to the burst, not beyond it.
	for i := range 3 {
		if got := take(time.Hour); !got.Allowed {
			t.Fatalf("take %d after an idle hour = %+v, want allowed", i, got)
		}
	}
	if got := take(time.Hour); got.Allowed {
		t.Fatalf("fourth take after an idle hour = %+v, want the burst of 3 to cap the refill", got)
	}

	// A drained bucket outlives a slower rate's refill: lowering the rate must
	// not hand it a fresh burst. It holds half a token of 3, so it is kept until
	// the slowest allowed rate (a burst a week) would have refilled the rest.
	slow := SessionQuotaConfig{Mode: SessionQuotaEnforce, RequestsPerHour: 1, Burst: 3}
	last := start.Add(time.Hour)
	if got := ml.takeAt("k", slow, last.Add(30*time.Minute)); got.Allowed {
		t.Fatalf("take after lowering the rate = %+v, want the drained bucket kept", got)
	}
	keep := SessionQuotaIdleTTL * 5 / 6
	ml.evictLocked(last.Add(30*time.Minute+keep-time.Second), slow.Burst)
	if len(ml.buckets) != 1 {
		t.Fatal("drained bucket evicted before the slowest allowed rate would have refilled it")
	}
	ml.evictLocked(last.Add(30*time.Minute+keep), slow.Burst)
	if len(ml.buckets) != 0 {
		t.Fatal("idle bucket kept after the slowest allowed rate would have refilled it")
	}

	// Sessions that sign in, make one request and leave don't pile up: at the
	// default size their buckets go at the next sweep a minute later.
	def := DefaultConfig().Session
	busy := NewMemoryQuotaLimiter()
	for i := range 1000 {
		busy.takeAt(fmt.Sprint("session:", i), def, start)
	}
	busy.takeAt("session:later", def, start.Add(quotaEvictEvery))
	if len(busy.buckets) != 1 {
		t.Fatalf("%d buckets kept a minute after one request each, want only the new one", len(busy.buckets))
	}
}

func TestSessionQuotaMiddleware(t *testing.T) {
	user := &auth.Claims{UserID: 5, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess}
	with := func(edit func(*auth.Claims)) *auth.Claims {
		c := *user
		edit(&c)
		return &c
	}
	admin := 1
	tests := []struct {
		name   string
		mode   string
		claims *auth.Claims
		path   string
		want   []int
	}{
		{"enforce refuses a session over its quota", SessionQuotaEnforce, user, "/api/v2/catalog/items/x", []int{204, 204, 429}},
		{"log serves a session over its quota", SessionQuotaLog, user, "/api/v2/catalog/items/x", []int{204, 204, 204}},
		{"off counts nothing", SessionQuotaOff, user, "/api/v2/catalog/items/x", []int{204, 204, 204}},
		{"media transfer is not counted", SessionQuotaEnforce, user, "/api/v2/stream/x", []int{204, 204, 204}},
		{"signing out is not counted", SessionQuotaEnforce, user, "/api/v2/auth/logout", []int{204, 204, 204}},
		{"the provider sign-out lookup is not counted", SessionQuotaEnforce, user, "/api/v2/auth/provider-logout", []int{204, 204, 204}},
		{"admins are not counted", SessionQuotaEnforce, with(func(c *auth.Claims) { c.Role = "admin" }), "/api/v2/catalog/items/x", []int{204, 204, 204}},
		{"impersonating admins are not counted", SessionQuotaEnforce, with(func(c *auth.Claims) { c.ImpersonatorUserID = &admin }), "/api/v2/catalog/items/x", []int{204, 204, 204}},
		{"API keys keep their own tiers", SessionQuotaEnforce, with(func(c *auth.Claims) { c.TokenType = auth.TokenTypeAPIKey; c.APIKeyID = 9 }), "/api/v2/catalog/items/x", []int{204, 204, 204}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := NewMiddleware(&recordingLimiter{}, &recordingLimiter{}, nil, true)
			mw.cfg = DefaultConfig()
			mw.cfg.Session = SessionQuotaConfig{Mode: tt.mode, RequestsPerHour: 1, Burst: 2}
			handler := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			for i, want := range tt.want {
				req := httptest.NewRequest(http.MethodGet, tt.path, nil)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req.WithContext(apimw.SetClaims(req.Context(), tt.claims)))
				if rec.Code != want {
					t.Fatalf("request %d = %d, want %d", i, rec.Code, want)
				}
				if want == http.StatusTooManyRequests && rec.Header().Get("Retry-After") == "" {
					t.Fatal("refused request has no Retry-After")
				}
			}
		})
	}

	// Sessions of one account have separate buckets.
	mw := NewMiddleware(&recordingLimiter{}, &recordingLimiter{}, nil, true)
	mw.cfg = DefaultConfig()
	mw.cfg.Session = SessionQuotaConfig{Mode: SessionQuotaEnforce, RequestsPerHour: 1, Burst: 1}
	handler := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, session := range []string{"a", "b"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/catalog", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req.WithContext(apimw.SetClaims(req.Context(), with(func(c *auth.Claims) { c.SessionID = session }))))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("first request of session %s = %d, want 204", session, rec.Code)
		}
	}
}

// While Redis is down the quota still holds, counted on this node, and a
// failed take pauses the window limiter too, so the request's other checks
// stop calling Redis as well.
func TestRedisQuotaLimiterCountsLocallyWhileRedisIsDown(t *testing.T) {
	commands := &commandHook{}
	windows := hookedRedisLimiter(t, refusingRedisAddr(t), commands)
	rl := NewRedisQuotaLimiter(windows)
	q := SessionQuotaConfig{Mode: SessionQuotaEnforce, RequestsPerHour: 1, Burst: 2}

	if got := rl.Take(t.Context(), "session:s", q); !got.Allowed {
		t.Fatalf("first take = %+v, want allowed", got)
	}
	sent := commands.started.Load()
	if got := rl.Take(t.Context(), "session:s", q); !got.Allowed {
		t.Fatalf("second take = %+v, want allowed", got)
	}
	if got := rl.Take(t.Context(), "session:s", q); got.Allowed {
		t.Fatalf("third take = %+v, want the burst of 2 enforced on this node", got)
	}
	if got := windows.Allow(t.Context(), "ip:192.0.2.1", Rate{RequestsPerSecond: 10, RequestsPerMinute: 600, Burst: 10}); !got.Allowed {
		t.Fatalf("window check during the pause = %+v, want allowed", got)
	}
	if got := commands.started.Load(); got != sent {
		t.Fatalf("checks during the pause sent %d commands to Redis, want none", got-sent)
	}
}
