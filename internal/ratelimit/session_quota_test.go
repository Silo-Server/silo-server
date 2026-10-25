package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"

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

	// A bucket is dropped only once it would be full again.
	ml.evict(start.Add(time.Hour + 2*time.Second))
	if len(ml.buckets) != 1 {
		t.Fatalf("bucket evicted before it refilled")
	}
	ml.evict(start.Add(time.Hour + 3*time.Second))
	if len(ml.buckets) != 0 {
		t.Fatalf("refilled bucket kept after eviction")
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
		{"admins are not counted", SessionQuotaEnforce, with(func(c *auth.Claims) { c.Role = "admin" }), "/api/v2/catalog/items/x", []int{204, 204, 204}},
		{"impersonating admins are not counted", SessionQuotaEnforce, with(func(c *auth.Claims) { c.ImpersonatorUserID = &admin }), "/api/v2/catalog/items/x", []int{204, 204, 204}},
		{"API keys keep their own tiers", SessionQuotaEnforce, with(func(c *auth.Claims) { c.TokenType = auth.TokenTypeAPIKey; c.APIKeyID = 9 }), "/api/v2/catalog/items/x", []int{204, 204, 204}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := NewMiddleware(&recordingLimiter{}, &recordingLimiter{}, nil, true)
			t.Cleanup(mw.quota.Close)
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
	t.Cleanup(mw.quota.Close)
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

// While Redis is down the quota still holds, counted on this node, and takes
// stop calling Redis until the pause ends.
func TestRedisQuotaLimiterCountsLocallyWhileRedisIsDown(t *testing.T) {
	client, err := cache.NewDeadlineRedisClientForRole(config.RedisConfig{URL: "redis://" + refusingRedisAddr(t)}, "tasks")
	if err != nil {
		t.Fatalf("redis client: %v", err)
	}
	t.Cleanup(func() { _ = cache.CloseRedisClient(client) })
	commands := &commandHook{}
	client.AddHook(commands)
	rl := NewRedisQuotaLimiter(client)
	t.Cleanup(rl.Close)
	rl.callTimeout = 100 * time.Millisecond
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
	if got := commands.started.Load(); got != sent {
		t.Fatalf("takes during the pause sent %d commands to Redis, want none", got-sent)
	}
}
