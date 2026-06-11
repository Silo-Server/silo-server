# Core Push Notifications — Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A durable, presence-gated server-side push subsystem that mirrors Phase-1 in-app notifications to registered devices over Web Push, APNs, and FCM.

**Architecture:** A new `internal/push` package (device/token store, enqueuer, delivery worker, transport interface + three transports, provider config) and a new `internal/presence` package (per-user live-connection registry, in-process + Redis overlay). `notifications.Service.Create()` calls a nil-tolerant push enqueuer after the row commits; the enqueuer writes one `push_deliveries` row per eligible device with a presence-grace `not_before`; a `PushDeliveryTask` claims due rows, re-checks presence, dispatches via the platform transport, and records outcomes / prunes dead tokens.

**Tech Stack:** Go 1.26, pgx v5, Goose migrations, go-redis v9 (already a dep), the existing taskmanager + encrypted-settings + events-hub infrastructure. Net-new deps for real transports: `github.com/sideshow/apns2` (APNs), `firebase.google.com/go/v4` (FCM), `github.com/SherClockHolmes/webpush-go` (Web Push).

**Spec:** `docs/superpowers/specs/2026-06-10-core-push-notifications-design.md`

Commands assume the repository root is the cwd. Set `export GOWORK=off` before any go command (this is a worktree). Local Postgres + Redis run via `docker compose up -d postgres redis`.

---

## Avoiding an import cycle

`notifications` must not import `push`. The seam is a tiny interface declared in the `notifications` package:

```go
// notifications package
type PushEnqueuer interface {
	EnqueueForNotification(ctx context.Context, n *Notification)
}
func (s *Service) SetPushEnqueuer(e PushEnqueuer) { s.push = e }
```

`push.Enqueuer` implements it (importing `notifications` for the `*Notification` type — one direction, no cycle). The delivery worker reads notification content via raw SQL against the `notifications` table (no Go import needed for the join). `main.go` wires the concrete enqueuer in.

## File map

| File | Action | Responsibility |
|---|---|---|
| `migrations/sql/<ts>_push_notifications.sql` | create | `user_devices` push columns + `push_deliveries` table |
| `internal/presence/registry.go` | create | `Registry` interface + in-process refcount impl |
| `internal/presence/redis.go` | create | Redis-overlay impl (INCR/DECR/TTL) |
| `internal/presence/registry_test.go` | create | registry tests |
| `internal/api/handlers/events_ws.go` | modify | `Add`/`release` on WS connect; TTL refresh on ping |
| `internal/push/types.go` | create | Device, Delivery, Payload, Result, Transport iface, status/transport consts |
| `internal/push/store.go` | create | pgx store: eligible devices, enqueue, claim, outcome, prune, register/revoke/toggle/list, purge |
| `internal/push/enqueuer.go` | create | Enqueuer: resolve eligible devices, write deliveries with grace |
| `internal/notifications/service.go` | modify | `PushEnqueuer` iface + `SetPushEnqueuer` + call after insert |
| `internal/push/config.go` | create | provider config read from encrypted settings; `Status()` |
| `internal/push/transport.go` | create | `Transport` interface (in types.go is fine) + registry/selection by platform |
| `internal/push/worker.go` | create | claim → presence recheck → dispatch → outcome; `PushDeliveryTask` |
| `internal/push/transport_webpush.go` | create | Web Push transport |
| `internal/push/transport_apns.go` | create | APNs transport |
| `internal/push/transport_fcm.go` | create | FCM transport |
| `internal/api/handlers/push.go` | create | registration/revoke/list/toggle/webpush-key/admin status |
| `internal/api/router.go` | modify | mount routes |
| `internal/catalog/encrypted_settings_repo.go` | modify | add `push.*` sensitive keys |
| `internal/taskmanager/tasks/notifications_retention.go` | modify | also purge terminal deliveries >7d |
| `cmd/silo/main.go` | modify | wire presence, push store/config/enqueuer/worker, register task |

---

### Task 1: Migration

**Files:**
- Create: `migrations/sql/<generated>_push_notifications.sql`

- [ ] **Step 1: Generate**

```bash
make migrate-create NAME=push_notifications
```

- [ ] **Step 2: Write the migration**

```sql
-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.user_devices
    ADD COLUMN push_token     text NULL,
    ADD COLUMN push_transport text NULL,
    ADD COLUMN push_enabled   boolean NOT NULL DEFAULT true,
    ADD COLUMN push_token_at  timestamptz NULL,
    ADD COLUMN push_failures  integer NOT NULL DEFAULT 0;

CREATE TABLE public.push_deliveries (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    notification_id bigint NOT NULL REFERENCES public.notifications(id) ON DELETE CASCADE,
    user_id         integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    device_id       text NOT NULL,
    transport       text NOT NULL,
    status          text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','sent','failed','skipped','dead')),
    attempts        integer NOT NULL DEFAULT 0,
    not_before      timestamptz NOT NULL DEFAULT now(),
    last_error      text NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX push_deliveries_claim_idx
    ON public.push_deliveries (not_before)
    WHERE status IN ('pending','failed');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE public.push_deliveries;
ALTER TABLE public.user_devices
    DROP COLUMN push_token,
    DROP COLUMN push_transport,
    DROP COLUMN push_enabled,
    DROP COLUMN push_token_at,
    DROP COLUMN push_failures;
-- +goose StatementEnd
```

- [ ] **Step 3: Apply + verify**

```bash
docker compose up -d postgres redis
make migrate-up && make migrate-status
```

Expected: new migration listed applied; no errors.

- [ ] **Step 4: Commit**

```bash
git add migrations/sql/
git commit -m "feat(push): user_devices push columns and push_deliveries table"
```

---

### Task 2: Presence registry (in-process)

**Files:**
- Create: `internal/presence/registry.go`, `internal/presence/registry_test.go`

- [ ] **Step 1: Write the failing test**

```go
package presence

import (
	"context"
	"testing"
)

func TestMemoryRegistry_ConnectedRefcount(t *testing.T) {
	r := NewMemoryRegistry()
	ctx := context.Background()
	if r.Connected(ctx, 7) {
		t.Fatal("user should start absent")
	}
	rel1 := r.Add(ctx, 7)
	rel2 := r.Add(ctx, 7)
	if !r.Connected(ctx, 7) {
		t.Fatal("user should be present after Add")
	}
	rel1()
	if !r.Connected(ctx, 7) {
		t.Fatal("still present: one connection remains")
	}
	rel2()
	if r.Connected(ctx, 7) {
		t.Fatal("absent after all releases")
	}
}

func TestMemoryRegistry_ReleaseIdempotent(t *testing.T) {
	r := NewMemoryRegistry()
	ctx := context.Background()
	rel := r.Add(ctx, 1)
	rel()
	rel() // double release must not underflow to negative / panic
	if r.Connected(ctx, 1) {
		t.Fatal("absent")
	}
	r.Add(ctx, 1)
	if !r.Connected(ctx, 1) {
		t.Fatal("present again")
	}
}
```

- [ ] **Step 2: Run, verify fail**

```bash
GOWORK=off go test ./internal/presence/ -run TestMemoryRegistry -v
```

Expected: FAIL (package/symbols undefined).

- [ ] **Step 3: Implement**

```go
package presence

import (
	"context"
	"sync"
)

// Registry tracks whether a user has at least one live realtime connection.
// Used to suppress push notifications to users who are actively connected.
type Registry interface {
	// Add registers one live connection for userID and returns a release
	// function that must be called exactly once on disconnect. The returned
	// function is safe to call multiple times.
	Add(ctx context.Context, userID int) (release func())
	// Connected reports whether userID has any live connection.
	Connected(ctx context.Context, userID int) bool
}

// MemoryRegistry is a process-local refcount registry.
type MemoryRegistry struct {
	mu     sync.Mutex
	counts map[int]int
}

func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{counts: make(map[int]int)}
}

func (m *MemoryRegistry) Add(_ context.Context, userID int) func() {
	m.mu.Lock()
	m.counts[userID]++
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.counts[userID] > 0 {
				m.counts[userID]--
				if m.counts[userID] == 0 {
					delete(m.counts, userID)
				}
			}
			m.mu.Unlock()
		})
	}
}

func (m *MemoryRegistry) Connected(_ context.Context, userID int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[userID] > 0
}
```

- [ ] **Step 4: Run, commit**

```bash
GOWORK=off go test ./internal/presence/ -v
git add internal/presence/registry.go internal/presence/registry_test.go
git commit -m "feat(presence): in-process connection registry"
```

---

### Task 3: Presence registry (Redis overlay)

**Files:**
- Create: `internal/presence/redis.go`
- Test: `internal/presence/redis_test.go` (skips if no local Redis)

- [ ] **Step 1: Write the test (integration, skip-guarded)**

```go
package presence

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/15" // db 15 = test scratch
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Skipf("redis url parse: %v", err)
	}
	c := redis.NewClient(opt)
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	return c
}

func TestRedisRegistry_ConnectedAndRelease(t *testing.T) {
	c := testRedis(t)
	ctx := context.Background()
	c.FlushDB(ctx)
	r := NewRedisRegistry(c, 60*time.Second)

	if r.Connected(ctx, 42) {
		t.Fatal("absent initially")
	}
	rel := r.Add(ctx, 42)
	if !r.Connected(ctx, 42) {
		t.Fatal("present after Add")
	}
	rel()
	if r.Connected(ctx, 42) {
		t.Fatal("absent after release")
	}
}

func TestRedisRegistry_FailsOpenIsCallerConcern(t *testing.T) {
	// Connected returns false only when the key is genuinely 0/absent.
	// (Fail-open on Redis *errors* is handled by the caller/worker, which
	// treats a Connected error as "not connected" → push proceeds. Here we
	// only assert the happy path; error injection is covered in the worker.)
	c := testRedis(t)
	ctx := context.Background()
	c.FlushDB(ctx)
	r := NewRedisRegistry(c, 60*time.Second)
	if r.Connected(ctx, 99) {
		t.Fatal("absent")
	}
}
```

- [ ] **Step 2: Run, verify fail**

```bash
GOWORK=off go test ./internal/presence/ -run TestRedisRegistry -v
```

Expected: FAIL (NewRedisRegistry undefined). If Redis is down the tests skip — that is acceptable, but the build must still fail until implemented.

- [ ] **Step 3: Implement**

```go
package presence

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisRegistry is a cluster-aware presence registry. Each Add increments a
// per-user counter key with a TTL; Connected reports key > 0. On any Redis
// error, Connected returns false so callers fail open (push proceeds rather
// than being suppressed by an outage).
type RedisRegistry struct {
	client *redis.Client
	ttl    time.Duration
}

func NewRedisRegistry(client *redis.Client, ttl time.Duration) *RedisRegistry {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &RedisRegistry{client: client, ttl: ttl}
}

func presenceKey(userID int) string { return fmt.Sprintf("push:presence:%d", userID) }

func (r *RedisRegistry) Add(ctx context.Context, userID int) func() {
	key := presenceKey(userID)
	pipe := r.client.TxPipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, r.ttl)
	_, _ = pipe.Exec(ctx) // best-effort; presence is advisory

	var once sync.Once
	return func() {
		once.Do(func() {
			// Detached context: release must run even as the request ctx cancels.
			bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if n, err := r.client.Decr(bg, key).Result(); err == nil && n <= 0 {
				r.client.Del(bg, key)
			}
		})
	}
}

func (r *RedisRegistry) Connected(ctx context.Context, userID int) bool {
	n, err := r.client.Get(ctx, presenceKey(userID)).Int()
	if err != nil {
		return false // fail open: unknown presence → allow push
	}
	return n > 0
}

// Refresh extends the TTL for a user's presence key; call on WS heartbeat.
func (r *RedisRegistry) Refresh(ctx context.Context, userID int) {
	r.client.Expire(ctx, presenceKey(userID), r.ttl)
}
```

- [ ] **Step 4: Run, commit**

```bash
GOWORK=off go test ./internal/presence/ -v
git add internal/presence/redis.go internal/presence/redis_test.go
git commit -m "feat(presence): redis-overlay registry"
```

---

### Task 4: Wire presence into the events WS handler

**Files:**
- Modify: `internal/api/handlers/events_ws.go` (struct, constructor, `HandleWebSocket`)

- [ ] **Step 1: Add the dependency**

Add a `presence presence.Registry` field to `EventsHandler` (struct at line 40) and a constructor parameter to `NewEventsHandler` (nil-tolerant). Import `internal/presence`.

- [ ] **Step 2: Add/release on connect**

In `HandleWebSocket`, after `claims` is confirmed non-nil and the connection is upgraded (right after the `eventsCh, unsubscribe := h.hub.Subscribe()` / `defer unsubscribe()` block, ~line 96):

```go
if h.presence != nil {
	release := h.presence.Add(ctx, claims.UserID)
	defer release()
}
```

- [ ] **Step 3: Refresh on ping (Redis TTL)**

The ping loop is started at line 97 via `startWebSocketPingLoop`. Extend its callback to also refresh presence when the registry supports it:

```go
startWebSocketPingLoop(ctx, func() error {
	if rr, ok := h.presence.(interface{ Refresh(context.Context, int) }); ok {
		rr.Refresh(ctx, claims.UserID)
	}
	return writeWebSocketControl(conn, websocket.PingMessage, nil)
})
```

(The `interface{ Refresh(...) }` assertion keeps `MemoryRegistry`, which has no TTL, working unchanged.)

- [ ] **Step 4: Build + existing handler tests green**

```bash
GOWORK=off go build ./internal/api/... && GOWORK=off go test ./internal/api/handlers/ -run TestEvents -v 2>&1 | tail -5
```

(The constructor signature changed — update its existing call sites in tests and `router.go`/`main.go` to pass `nil` for now; main wiring lands in Task 16. Build must pass.)

- [ ] **Step 5: Commit**

```bash
git add internal/api/handlers/events_ws.go internal/api/router.go internal/api/handlers/*_test.go
git commit -m "feat(presence): track live connections in events websocket"
```

---

### Task 5: Push domain types

**Files:**
- Create: `internal/push/types.go`

- [ ] **Step 1: Write the types**

```go
package push

import (
	"context"
	"time"
)

// Transport identifiers (also stored in user_devices.push_transport and
// push_deliveries.transport).
const (
	TransportAPNs    = "apns"
	TransportFCM     = "fcm"
	TransportWebPush = "webpush"
)

// Delivery statuses.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
	StatusDead    = "dead"
)

// Device is a push-eligible device row.
type Device struct {
	UserID    int
	ProfileID string
	DeviceID  string
	Transport string
	Token     string // bare token (apns/fcm) or JSON subscription (webpush)
}

// Delivery is one queued push for one device.
type Delivery struct {
	ID             int64
	NotificationID int64
	UserID         int
	DeviceID       string
	Transport      string
	Status         string
	Attempts       int
	NotBefore      time.Time
}

// Payload is the content sent to a device. Built from a notification row at
// send time so edits/expiry are reflected.
type Payload struct {
	NotificationID int64
	Title          string
	Body           string
	Link           string
	Category       string
}

// SendResult classifies a transport attempt.
type SendResult int

const (
	// ResultSent: delivered (or accepted by the provider).
	ResultSent SendResult = iota
	// ResultSoftFail: retryable (timeout, 5xx, rate-limit).
	ResultSoftFail
	// ResultDead: token is permanently invalid; prune it.
	ResultDead
)

// Transport delivers a payload to one device token. Implementations are
// selected by Device.Transport.
type Transport interface {
	// Name returns the transport id (TransportAPNs/FCM/WebPush).
	Name() string
	// Configured reports whether provider credentials are present.
	Configured() bool
	// Send delivers payload to token. err is for logging only; the SendResult
	// drives queue state. retryAfter (>0) parks the transport when rate-limited.
	Send(ctx context.Context, token string, payload Payload) (res SendResult, retryAfter time.Duration, err error)
}
```

- [ ] **Step 2: Build + commit**

```bash
GOWORK=off go build ./internal/push/
git add internal/push/types.go
git commit -m "feat(push): domain types and transport interface"
```

---

### Task 6: Push store

**Files:**
- Create: `internal/push/store.go`

The store owns all SQL against `user_devices` (push columns) and `push_deliveries`. Child suppression is enforced in `EligibleDevices` via a join to `user_profiles`.

- [ ] **Step 1: Implement**

```go
package push

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("push: not found")

// hiddenForChild are categories never pushed to a child profile (mirrors the
// web toast suppression rule).
var hiddenForChild = []string{"request", "system", "admin"}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// EligibleDevices returns push-eligible devices for a notification addressed to
// userID (profileID empty = all of the user's devices). A device is excluded
// when category is child-hidden and the device's profile is a child.
func (s *Store) EligibleDevices(ctx context.Context, userID int, profileID, category string) ([]Device, error) {
	q := `
		SELECT d.user_id, d.profile_id, d.device_id, d.push_transport, d.push_token
		FROM user_devices d
		JOIN user_profiles p ON p.user_id = d.user_id AND p.id = d.profile_id
		WHERE d.user_id = $1
		  AND d.push_token IS NOT NULL
		  AND d.push_enabled = true
		  AND ($2 = '' OR d.profile_id = $2)
		  AND NOT (p.is_child AND $3 = ANY($4::text[]))`
	rows, err := s.pool.Query(ctx, q, userID, profileID, category, hiddenForChild)
	if err != nil {
		return nil, fmt.Errorf("eligible devices: %w", err)
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var transport, token *string
		if err := rows.Scan(&d.UserID, &d.ProfileID, &d.DeviceID, &transport, &token); err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		if transport != nil {
			d.Transport = *transport
		}
		if token != nil {
			d.Token = *token
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// EnqueueDelivery writes one pending delivery row.
func (s *Store) EnqueueDelivery(ctx context.Context, notificationID int64, d Device, notBefore time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO push_deliveries (notification_id, user_id, device_id, transport, status, not_before)
		VALUES ($1,$2,$3,$4,'pending',$5)`,
		notificationID, d.UserID, d.DeviceID, d.Transport, notBefore)
	if err != nil {
		return fmt.Errorf("enqueue delivery: %w", err)
	}
	return nil
}

// claimedDelivery bundles a delivery row with the data needed to send it.
type claimedDelivery struct {
	Delivery
	Token   string
	Payload Payload
}

// ClaimDue locks up to limit due deliveries (pending/failed, not_before<=now)
// FOR UPDATE SKIP LOCKED inside a transaction, joining the device token and the
// notification content. The returned commit func must be called after the
// caller records outcomes via the provided tx-bound store ops. To keep the
// worker simple we instead claim-and-return within one tx and let the caller
// pass outcomes back; see Worker.runOnce. Here ClaimDue returns the rows and a
// function set bound to the open tx.
func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int) (*pgxpool.Conn, pgx.Tx, []claimedDelivery, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("acquire: %w", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		return nil, nil, nil, fmt.Errorf("begin: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT pd.id, pd.notification_id, pd.user_id, pd.device_id, pd.transport, pd.status, pd.attempts, pd.not_before,
		       d.push_token, n.title, n.body, COALESCE(n.link,''), n.category
		FROM push_deliveries pd
		JOIN user_devices d ON d.user_id = pd.user_id AND d.device_id = pd.device_id
		JOIN notifications n ON n.id = pd.notification_id
		WHERE pd.status IN ('pending','failed') AND pd.not_before <= $1
		ORDER BY pd.not_before
		LIMIT $2
		FOR UPDATE OF pd SKIP LOCKED`, now, limit)
	if err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		return nil, nil, nil, fmt.Errorf("claim query: %w", err)
	}
	defer rows.Close()
	var out []claimedDelivery
	for rows.Next() {
		var c claimedDelivery
		var token *string
		if err := rows.Scan(&c.ID, &c.NotificationID, &c.UserID, &c.DeviceID, &c.Transport,
			&c.Status, &c.Attempts, &c.NotBefore,
			&token, &c.Payload.Title, &c.Payload.Body, &c.Payload.Link, &c.Payload.Category); err != nil {
			_ = tx.Rollback(ctx)
			conn.Release()
			return nil, nil, nil, fmt.Errorf("scan claim: %w", err)
		}
		if token != nil {
			c.Token = *token
		}
		c.Payload.NotificationID = c.NotificationID
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		return nil, nil, nil, err
	}
	return conn, tx, out, nil
}

// MarkSentTx / MarkSkippedTx / MarkFailedTx / MarkDeadTx operate within the
// claim transaction.
func (s *Store) MarkSentTx(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, `UPDATE push_deliveries SET status='sent', attempts=attempts+1, updated_at=now() WHERE id=$1`, id)
	return err
}

func (s *Store) MarkSkippedTx(ctx context.Context, tx pgx.Tx, id int64, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE push_deliveries SET status='skipped', last_error=$2, updated_at=now() WHERE id=$1`, id, reason)
	return err
}

func (s *Store) MarkFailedTx(ctx context.Context, tx pgx.Tx, id int64, nextNotBefore time.Time, errMsg string) error {
	_, err := tx.Exec(ctx, `UPDATE push_deliveries SET status='failed', attempts=attempts+1, not_before=$2, last_error=$3, updated_at=now() WHERE id=$1`, id, nextNotBefore, errMsg)
	return err
}

func (s *Store) MarkDeadTx(ctx context.Context, tx pgx.Tx, id int64, errMsg string) error {
	_, err := tx.Exec(ctx, `UPDATE push_deliveries SET status='dead', attempts=attempts+1, last_error=$2, updated_at=now() WHERE id=$1`, id, errMsg)
	return err
}

// PruneTokenTx clears a dead token from the device row (within the claim tx).
func (s *Store) PruneTokenTx(ctx context.Context, tx pgx.Tx, userID int, deviceID string) error {
	_, err := tx.Exec(ctx, `UPDATE user_devices SET push_token=NULL, push_enabled=false, updated_at_marker=now() WHERE user_id=$1 AND device_id=$2`, userID, deviceID)
	if err != nil {
		// user_devices has no updated_at column; fall back without it.
		_, err = tx.Exec(ctx, `UPDATE user_devices SET push_token=NULL, push_enabled=false WHERE user_id=$1 AND device_id=$2`, userID, deviceID)
	}
	return err
}

// --- registration / management (autocommit) ---

// RegisterToken upserts a push token onto the device row (creating the device
// row if the client hasn't been seen). profileID/deviceName/platform come from
// request context.
func (s *Store) RegisterToken(ctx context.Context, userID int, profileID, deviceID, transport, token string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_devices (user_id, profile_id, device_id, push_token, push_transport, push_enabled, push_token_at, push_failures)
		VALUES ($1,$2,$3,$4,$5,true,now(),0)
		ON CONFLICT (user_id, profile_id, device_id) DO UPDATE
		SET push_token=$4, push_transport=$5, push_enabled=true, push_token_at=now(), push_failures=0`,
		userID, profileID, deviceID, token, transport)
	if err != nil {
		return fmt.Errorf("register token: %w", err)
	}
	return nil
}

func (s *Store) RevokeToken(ctx context.Context, userID int, profileID, deviceID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE user_devices SET push_token=NULL, push_enabled=false
		WHERE user_id=$1 AND profile_id=$2 AND device_id=$3`, userID, profileID, deviceID)
	return err
}

func (s *Store) SetDeviceEnabled(ctx context.Context, userID int, deviceID string, enabled bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE user_devices SET push_enabled=$3 WHERE user_id=$1 AND device_id=$2`, userID, deviceID, enabled)
	if err != nil {
		return fmt.Errorf("set device enabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeviceInfo is a row for the management list.
type DeviceInfo struct {
	DeviceID     string `json:"device_id"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	Transport    string `json:"transport"`
	PushEnabled  bool   `json:"push_enabled"`
	RegisteredAt *time.Time `json:"registered_at,omitempty"`
}

func (s *Store) ListDevices(ctx context.Context, userID int) ([]DeviceInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT device_id, device_name, device_platform, COALESCE(push_transport,''), push_enabled, push_token_at
		FROM user_devices
		WHERE user_id=$1 AND push_token IS NOT NULL
		ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()
	var out []DeviceInfo
	for rows.Next() {
		var d DeviceInfo
		if err := rows.Scan(&d.DeviceID, &d.Name, &d.Platform, &d.Transport, &d.PushEnabled, &d.RegisteredAt); err != nil {
			return nil, fmt.Errorf("scan device info: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PurgeTerminal deletes terminal deliveries older than before.
func (s *Store) PurgeTerminal(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM push_deliveries
		WHERE status IN ('sent','skipped','dead') AND updated_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("purge terminal: %w", err)
	}
	return tag.RowsAffected(), nil
}
```

NOTE: `PruneTokenTx`'s first statement references a nonexistent `updated_at_marker` column deliberately so the implementer removes it — `user_devices` has no `updated_at`. Use ONLY the second form:

```go
func (s *Store) PruneTokenTx(ctx context.Context, tx pgx.Tx, userID int, deviceID string) error {
	_, err := tx.Exec(ctx, `UPDATE user_devices SET push_token=NULL, push_enabled=false WHERE user_id=$1 AND device_id=$2`, userID, deviceID)
	return err
}
```

(Verify `user_profiles.is_child` column exists — `grep -n is_child migrations/sql/001_schema.sql`; it does per Phase-1 work. Verify `user_devices` has no `updated_at`/`created_at` — `\d user_devices` / read migration 180.)

- [ ] **Step 2: Build**

```bash
GOWORK=off go build ./internal/push/
```

- [ ] **Step 3: Commit**

```bash
git add internal/push/store.go
git commit -m "feat(push): pgx store for devices and deliveries"
```

---

### Task 7: Enqueuer + notifications hook

**Files:**
- Create: `internal/push/enqueuer.go`
- Modify: `internal/notifications/service.go`
- Test: `internal/push/enqueuer_test.go`

- [ ] **Step 1: notifications seam**

In `internal/notifications/service.go` add the interface + field + setter:

```go
// PushEnqueuer mirrors a created notification to push delivery. Implemented by
// internal/push; nil when push is disabled.
type PushEnqueuer interface {
	EnqueueForNotification(ctx context.Context, n *Notification)
}

// (add field `push PushEnqueuer` to Service struct)
func (s *Service) SetPushEnqueuer(e PushEnqueuer) { s.push = e }
```

After the successful insert + hub publish in `Create()` (after the `hub.PublishJSON` block, before `return nil`):

```go
if s.push != nil {
	s.push.EnqueueForNotification(ctx, n)
}
```

Build the notifications package; existing tests must stay green (the field is nil in tests).

- [ ] **Step 2: Write the failing enqueuer test**

```go
package push

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeDeviceSource struct {
	devices map[string][]Device // key: fmt userID/profileID/category not needed; return all
	enqueued []struct {
		nid int64
		dev Device
		nb  time.Time
	}
	eligibleErr error
}

func (f *fakeDeviceSource) EligibleDevices(_ context.Context, userID int, profileID, category string) ([]Device, error) {
	if f.eligibleErr != nil {
		return nil, f.eligibleErr
	}
	return f.devices["all"], nil
}
func (f *fakeDeviceSource) EnqueueDelivery(_ context.Context, nid int64, d Device, nb time.Time) error {
	f.enqueued = append(f.enqueued, struct {
		nid int64
		dev Device
		nb  time.Time
	}{nid, d, nb})
	return nil
}

func TestEnqueuer_WritesOnePerDevice(t *testing.T) {
	src := &fakeDeviceSource{devices: map[string][]Device{"all": {
		{UserID: 7, DeviceID: "d1", Transport: "apns", Token: "t1"},
		{UserID: 7, DeviceID: "d2", Transport: "webpush", Token: "t2"},
	}}}
	now := time.Unix(1_000_000, 0)
	e := NewEnqueuer(src, 30*time.Second, func() time.Time { return now })

	link := "/requests"
	e.EnqueueForNotification(context.Background(), &notifications.Notification{
		ID: 5, UserID: 7, Category: notifications.CategoryRequest, Title: "Approved", Link: &link,
	})

	if len(src.enqueued) != 2 {
		t.Fatalf("enqueued = %d, want 2", len(src.enqueued))
	}
	if !src.enqueued[0].nb.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("not_before = %v, want now+30s", src.enqueued[0].nb)
	}
}

func TestEnqueuer_NoDevicesNoop(t *testing.T) {
	src := &fakeDeviceSource{devices: map[string][]Device{"all": nil}}
	e := NewEnqueuer(src, 30*time.Second, time.Now)
	e.EnqueueForNotification(context.Background(), &notifications.Notification{ID: 1, UserID: 7, Category: notifications.CategoryContent, Title: "x"})
	if len(src.enqueued) != 0 {
		t.Fatal("no devices should enqueue nothing")
	}
}

func TestEnqueuer_EligibleErrorIsSwallowed(t *testing.T) {
	src := &fakeDeviceSource{eligibleErr: context.DeadlineExceeded}
	e := NewEnqueuer(src, 30*time.Second, time.Now)
	// must not panic; best-effort
	e.EnqueueForNotification(context.Background(), &notifications.Notification{ID: 1, UserID: 7, Category: notifications.CategorySystem, Title: "x"})
	if len(src.enqueued) != 0 {
		t.Fatal("error path enqueues nothing")
	}
}
```

- [ ] **Step 3: Run, verify fail**

```bash
GOWORK=off go test ./internal/push/ -run TestEnqueuer -v
```

- [ ] **Step 4: Implement**

```go
package push

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

// deviceSource is the slice of Store the enqueuer needs (interface for testing).
type deviceSource interface {
	EligibleDevices(ctx context.Context, userID int, profileID, category string) ([]Device, error)
	EnqueueDelivery(ctx context.Context, notificationID int64, d Device, notBefore time.Time) error
}

// Enqueuer mirrors created notifications to per-device delivery rows with a
// presence-grace not_before. Best-effort: failures log and return.
type Enqueuer struct {
	src   deviceSource
	grace time.Duration
	now   func() time.Time
}

func NewEnqueuer(src deviceSource, grace time.Duration, now func() time.Time) *Enqueuer {
	if now == nil {
		now = time.Now
	}
	return &Enqueuer{src: src, grace: grace, now: now}
}

func (e *Enqueuer) EnqueueForNotification(ctx context.Context, n *notifications.Notification) {
	if n == nil || n.UserID <= 0 {
		return
	}
	profileID := ""
	if n.ProfileID != nil {
		profileID = *n.ProfileID
	}
	devices, err := e.src.EligibleDevices(ctx, n.UserID, profileID, string(n.Category))
	if err != nil {
		slog.WarnContext(ctx, "push: eligible devices failed", "notification_id", n.ID, "error", err)
		return
	}
	notBefore := e.now().Add(e.grace)
	for _, d := range devices {
		if err := e.src.EnqueueDelivery(ctx, n.ID, d, notBefore); err != nil {
			slog.WarnContext(ctx, "push: enqueue delivery failed", "notification_id", n.ID, "device_id", d.DeviceID, "error", err)
		}
	}
}
```

`*Store` satisfies `deviceSource` already (it has both methods). Confirm with a compile assertion in enqueuer.go: `var _ deviceSource = (*Store)(nil)`.

- [ ] **Step 5: Run, build notifications, commit**

```bash
GOWORK=off go test ./internal/push/ -v && GOWORK=off go test ./internal/notifications/ 2>&1 | tail -3
git add internal/push/enqueuer.go internal/push/enqueuer_test.go internal/notifications/service.go
git commit -m "feat(push): enqueuer and notifications hook"
```

---

### Task 8: Provider config

**Files:**
- Create: `internal/push/config.go`
- Modify: `internal/catalog/encrypted_settings_repo.go`

- [ ] **Step 1: Register sensitive keys**

In `internal/catalog/encrypted_settings_repo.go` add to `SensitiveSettingKeys`:

```go
"push.apns.p8_key":             true,
"push.apns.key_id":             true,
"push.apns.team_id":            true,
"push.apns.bundle_id":          true,
"push.fcm.service_account_json": true,
"push.webpush.vapid_private":   true,
"push.webpush.vapid_public":    true,
"push.webpush.subject":         true,
```

- [ ] **Step 2: Config reader**

```go
package push

import "context"

// settingsReader is the slice of the settings repo we need.
type settingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

type APNsConfig struct {
	P8Key, KeyID, TeamID, BundleID string
}
type FCMConfig struct {
	ServiceAccountJSON string
}
type WebPushConfig struct {
	VAPIDPublic, VAPIDPrivate, Subject string
}

func (c APNsConfig) Configured() bool {
	return c.P8Key != "" && c.KeyID != "" && c.TeamID != "" && c.BundleID != ""
}
func (c FCMConfig) Configured() bool     { return c.ServiceAccountJSON != "" }
func (c WebPushConfig) Configured() bool { return c.VAPIDPublic != "" && c.VAPIDPrivate != "" }

// Config loads provider config from the (encrypted) settings repo on demand.
type Config struct{ s settingsReader }

func NewConfig(s settingsReader) *Config { return &Config{s: s} }

func (c *Config) get(ctx context.Context, key string) string {
	v, err := c.s.Get(ctx, key)
	if err != nil {
		return ""
	}
	return v
}

func (c *Config) APNs(ctx context.Context) APNsConfig {
	return APNsConfig{
		P8Key:    c.get(ctx, "push.apns.p8_key"),
		KeyID:    c.get(ctx, "push.apns.key_id"),
		TeamID:   c.get(ctx, "push.apns.team_id"),
		BundleID: c.get(ctx, "push.apns.bundle_id"),
	}
}
func (c *Config) FCM(ctx context.Context) FCMConfig {
	return FCMConfig{ServiceAccountJSON: c.get(ctx, "push.fcm.service_account_json")}
}
func (c *Config) WebPush(ctx context.Context) WebPushConfig {
	return WebPushConfig{
		VAPIDPublic:  c.get(ctx, "push.webpush.vapid_public"),
		VAPIDPrivate: c.get(ctx, "push.webpush.vapid_private"),
		Subject:      c.get(ctx, "push.webpush.subject"),
	}
}

// Status reports per-transport configured booleans (no secrets).
type Status struct {
	APNs    bool `json:"apns"`
	FCM     bool `json:"fcm"`
	WebPush bool `json:"webpush"`
}

func (c *Config) Status(ctx context.Context) Status {
	return Status{
		APNs:    c.APNs(ctx).Configured(),
		FCM:     c.FCM(ctx).Configured(),
		WebPush: c.WebPush(ctx).Configured(),
	}
}
```

Verify the settings repo's `Get` signature matches `settingsReader` (read `internal/catalog/server_settings_repo.go` / `encrypted_settings_repo.go`); adjust the interface to the real signature.

- [ ] **Step 3: Build + commit**

```bash
GOWORK=off go build ./internal/push/ ./internal/catalog/
git add internal/push/config.go internal/catalog/encrypted_settings_repo.go
git commit -m "feat(push): encrypted provider config and status"
```

---

### Task 9: Delivery worker + fake transport

**Files:**
- Create: `internal/push/worker.go`, `internal/push/worker_test.go`

This task uses a fake transport and a fake store seam to test outcome transitions without DB or network. The real store's `ClaimDue` returns a live tx; to keep the worker unit-testable, define a `deliveryQueue` interface the worker depends on, with a fake in tests and `*Store` as the prod impl. To avoid threading pgx.Tx through the interface, the worker claims a batch, and for each delivery calls back outcome methods keyed by id; the prod adapter wraps the tx.

- [ ] **Step 1: Worker interfaces + loop**

```go
package push

import (
	"context"
	"log/slog"
	"time"
)

// presenceChecker reports live-connection presence (internal/presence.Registry).
type presenceChecker interface {
	Connected(ctx context.Context, userID int) bool
}

// queue is the worker's view of delivery persistence. One runOnce call claims a
// batch and records each outcome; the prod impl wraps a single tx.
type queue interface {
	// Claim returns due deliveries and an outcome recorder bound to them.
	Claim(ctx context.Context, now time.Time, limit int) ([]claimedDelivery, Outcomes, error)
}

// Outcomes records per-delivery results and commits/rolls back the batch.
type Outcomes interface {
	Sent(id int64)
	Skipped(id int64, reason string)
	Failed(id int64, nextNotBefore time.Time, errMsg string)
	Dead(id int64, userID int, deviceID, errMsg string)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context)
}

// backoff schedule by attempts already made (0-indexed): 1m, 5m, 30m, then dead.
var backoffSchedule = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

type Worker struct {
	q         queue
	presence  presenceChecker
	transports map[string]Transport
	batch     int
	now       func() time.Time
	// parkedUntil tracks rate-limit parking per transport.
	parkedUntil map[string]time.Time
}

func NewWorker(q queue, presence presenceChecker, transports []Transport, now func() time.Time) *Worker {
	if now == nil {
		now = time.Now
	}
	m := make(map[string]Transport, len(transports))
	for _, t := range transports {
		m[t.Name()] = t
	}
	return &Worker{q: q, presence: presence, transports: m, batch: 100, now: now, parkedUntil: map[string]time.Time{}}
}

// RunOnce processes one batch. Returns the number of deliveries handled.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	now := w.now()
	items, out, err := w.q.Claim(ctx, now, w.batch)
	if err != nil {
		return 0, err
	}
	defer out.Rollback(ctx) // no-op after Commit

	for _, it := range items {
		w.handle(ctx, now, it, out)
	}
	if err := out.Commit(ctx); err != nil {
		return 0, err
	}
	return len(items), nil
}

func (w *Worker) handle(ctx context.Context, now time.Time, it claimedDelivery, out Outcomes) {
	// Presence gate: user actively connected → skip (in-app sufficed).
	if w.presence != nil && w.presence.Connected(ctx, it.UserID) {
		out.Skipped(it.ID, "user present")
		return
	}
	t := w.transports[it.Transport]
	if t == nil || !t.Configured() {
		out.Skipped(it.ID, "transport unconfigured")
		return
	}
	if until, ok := w.parkedUntil[it.Transport]; ok && now.Before(until) {
		// parked: defer via failed with not_before=until (no attempt charged ideally,
		// but charging is acceptable; keep simple)
		out.Failed(it.ID, until, "transport rate-limited")
		return
	}

	res, retryAfter, sendErr := t.Send(ctx, it.Token, it.Payload)
	switch res {
	case ResultSent:
		out.Sent(it.ID)
	case ResultDead:
		out.Dead(it.ID, it.UserID, it.DeviceID, errString(sendErr))
	case ResultSoftFail:
		if retryAfter > 0 {
			w.parkedUntil[it.Transport] = now.Add(retryAfter)
		}
		next, dead := nextBackoff(it.Attempts, now, retryAfter)
		if dead {
			out.Dead(it.ID, it.UserID, it.DeviceID, "max attempts: "+errString(sendErr))
		} else {
			out.Failed(it.ID, next, errString(sendErr))
		}
	}
	if sendErr != nil {
		slog.WarnContext(ctx, "push: send result", "delivery_id", it.ID, "transport", it.Transport, "result", res, "error", sendErr)
	}
}

func nextBackoff(attempts int, now time.Time, retryAfter time.Duration) (time.Time, bool) {
	if attempts >= len(backoffSchedule) {
		return time.Time{}, true // exhausted → dead
	}
	d := backoffSchedule[attempts]
	if retryAfter > d {
		d = retryAfter
	}
	return now.Add(d), false
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
```

- [ ] **Step 2: Tests with fakes**

```go
package push

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recordedOutcome struct {
	kind string // sent|skipped|failed|dead
	id   int64
	next time.Time
	msg  string
}

type fakeOutcomes struct {
	rec       []recordedOutcome
	committed bool
}

func (f *fakeOutcomes) Sent(id int64)                  { f.rec = append(f.rec, recordedOutcome{"sent", id, time.Time{}, ""}) }
func (f *fakeOutcomes) Skipped(id int64, r string)     { f.rec = append(f.rec, recordedOutcome{"skipped", id, time.Time{}, r}) }
func (f *fakeOutcomes) Failed(id int64, n time.Time, m string) { f.rec = append(f.rec, recordedOutcome{"failed", id, n, m}) }
func (f *fakeOutcomes) Dead(id int64, _ int, _, m string) { f.rec = append(f.rec, recordedOutcome{"dead", id, time.Time{}, m}) }
func (f *fakeOutcomes) Commit(context.Context) error   { f.committed = true; return nil }
func (f *fakeOutcomes) Rollback(context.Context)       {}

type fakeQueue struct {
	items []claimedDelivery
	out   *fakeOutcomes
}

func (q *fakeQueue) Claim(context.Context, time.Time, int) ([]claimedDelivery, Outcomes, error) {
	return q.items, q.out, nil
}

type fakeTransport struct {
	name       string
	configured bool
	res        SendResult
	retryAfter time.Duration
	err        error
	calls      int
}

func (t *fakeTransport) Name() string       { return t.name }
func (t *fakeTransport) Configured() bool    { return t.configured }
func (t *fakeTransport) Send(context.Context, string, Payload) (SendResult, time.Duration, error) {
	t.calls++
	return t.res, t.retryAfter, t.err
}

type fakePresence struct{ connected map[int]bool }

func (p fakePresence) Connected(_ context.Context, u int) bool { return p.connected[u] }

func deliveries(d ...claimedDelivery) []claimedDelivery { return d }

func cd(id int64, userID, attempts int, transport string) claimedDelivery {
	return claimedDelivery{Delivery: Delivery{ID: id, UserID: userID, Attempts: attempts, Transport: transport}, Token: "tok"}
}

func TestWorker_SentOnSuccess(t *testing.T) {
	out := &fakeOutcomes{}
	q := &fakeQueue{items: deliveries(cd(1, 7, 0, "apns")), out: out}
	w := NewWorker(q, fakePresence{}, []Transport{&fakeTransport{name: "apns", configured: true, res: ResultSent}}, func() time.Time { return time.Unix(100, 0) })
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(out.rec) != 1 || out.rec[0].kind != "sent" || !out.committed {
		t.Fatalf("got %+v committed=%v", out.rec, out.committed)
	}
}

func TestWorker_SkipsPresentUser(t *testing.T) {
	tr := &fakeTransport{name: "apns", configured: true, res: ResultSent}
	out := &fakeOutcomes{}
	q := &fakeQueue{items: deliveries(cd(1, 7, 0, "apns")), out: out}
	w := NewWorker(q, fakePresence{connected: map[int]bool{7: true}}, []Transport{tr}, nil)
	w.RunOnce(context.Background())
	if out.rec[0].kind != "skipped" || tr.calls != 0 {
		t.Fatalf("present user must skip without send; got %+v calls=%d", out.rec, tr.calls)
	}
}

func TestWorker_SkipsUnconfiguredTransport(t *testing.T) {
	out := &fakeOutcomes{}
	q := &fakeQueue{items: deliveries(cd(1, 7, 0, "fcm")), out: out}
	w := NewWorker(q, fakePresence{}, []Transport{&fakeTransport{name: "fcm", configured: false}}, nil)
	w.RunOnce(context.Background())
	if out.rec[0].kind != "skipped" {
		t.Fatalf("unconfigured → skipped; got %+v", out.rec)
	}
}

func TestWorker_SoftFailBackoffThenDead(t *testing.T) {
	now := time.Unix(1000, 0)
	// attempts=0 → failed at +1m
	out := &fakeOutcomes{}
	q := &fakeQueue{items: deliveries(cd(1, 7, 0, "apns")), out: out}
	w := NewWorker(q, fakePresence{}, []Transport{&fakeTransport{name: "apns", configured: true, res: ResultSoftFail, err: errors.New("503")}}, func() time.Time { return now })
	w.RunOnce(context.Background())
	if out.rec[0].kind != "failed" || !out.rec[0].next.Equal(now.Add(time.Minute)) {
		t.Fatalf("attempt0 → failed +1m; got %+v", out.rec[0])
	}
	// attempts=3 (exhausted) → dead
	out2 := &fakeOutcomes{}
	q2 := &fakeQueue{items: deliveries(cd(2, 7, 3, "apns")), out: out2}
	w2 := NewWorker(q2, fakePresence{}, []Transport{&fakeTransport{name: "apns", configured: true, res: ResultSoftFail, err: errors.New("503")}}, func() time.Time { return now })
	w2.RunOnce(context.Background())
	if out2.rec[0].kind != "dead" {
		t.Fatalf("exhausted → dead; got %+v", out2.rec[0])
	}
}

func TestWorker_DeadTokenOnHardFail(t *testing.T) {
	out := &fakeOutcomes{}
	q := &fakeQueue{items: deliveries(cd(1, 7, 0, "apns")), out: out}
	w := NewWorker(q, fakePresence{}, []Transport{&fakeTransport{name: "apns", configured: true, res: ResultDead, err: errors.New("Unregistered")}}, nil)
	w.RunOnce(context.Background())
	if out.rec[0].kind != "dead" {
		t.Fatalf("hard fail → dead; got %+v", out.rec)
	}
}
```

- [ ] **Step 3: Run until green**

```bash
GOWORK=off go test ./internal/push/ -run TestWorker -v
```

- [ ] **Step 4: Prod queue adapter (Store-backed) + task**

Add to `worker.go` the `*Store` adapter implementing `queue`/`Outcomes` over `ClaimDue`'s tx, and the task:

```go
// storeQueue adapts *Store to the worker's queue interface.
type storeQueue struct{ s *Store }

func NewStoreQueue(s *Store) queue { return &storeQueue{s: s} }

func (q *storeQueue) Claim(ctx context.Context, now time.Time, limit int) ([]claimedDelivery, Outcomes, error) {
	conn, tx, items, err := q.s.ClaimDue(ctx, now, limit)
	if err != nil {
		return nil, nil, err
	}
	return items, &txOutcomes{s: q.s, tx: tx, conn: conn}, nil
}

type txOutcomes struct {
	s    *Store
	tx   pgx.Tx
	conn *pgxpool.Conn
	done bool
	err  error
}

func (o *txOutcomes) Sent(id int64)              { o.run(func() error { return o.s.MarkSentTx(context.Background(), o.tx, id) }) }
func (o *txOutcomes) Skipped(id int64, r string) { o.run(func() error { return o.s.MarkSkippedTx(context.Background(), o.tx, id, r) }) }
func (o *txOutcomes) Failed(id int64, n time.Time, m string) { o.run(func() error { return o.s.MarkFailedTx(context.Background(), o.tx, id, n, m) }) }
func (o *txOutcomes) Dead(id int64, userID int, deviceID, m string) {
	o.run(func() error {
		if err := o.s.MarkDeadTx(context.Background(), o.tx, id, m); err != nil {
			return err
		}
		return o.s.PruneTokenTx(context.Background(), o.tx, userID, deviceID)
	})
}
func (o *txOutcomes) run(fn func() error) {
	if o.err != nil {
		return
	}
	o.err = fn()
}
func (o *txOutcomes) Commit(ctx context.Context) error {
	if o.done {
		return nil
	}
	o.done = true
	defer o.conn.Release()
	if o.err != nil {
		_ = o.tx.Rollback(ctx)
		return o.err
	}
	return o.tx.Commit(ctx)
}
func (o *txOutcomes) Rollback(ctx context.Context) {
	if o.done {
		return
	}
	o.done = true
	defer o.conn.Release()
	_ = o.tx.Rollback(ctx)
}
```

Imports needed in worker.go: `github.com/jackc/pgx/v5`, `github.com/jackc/pgx/v5/pgxpool`.

Task (`internal/push/task.go` or in worker.go) following `internal/taskmanager/tasks/notifications_digest.go` shape:

```go
// PushDeliveryTask drains due push deliveries on a short interval.
type PushDeliveryTask struct{ w *Worker }

func NewPushDeliveryTask(w *Worker) *PushDeliveryTask { return &PushDeliveryTask{w: w} }
func (t *PushDeliveryTask) Key() string         { return "push_delivery" }
func (t *PushDeliveryTask) Name() string        { return "Deliver push notifications" }
func (t *PushDeliveryTask) Description() string  { return "Sends queued push notifications to registered devices" }
func (t *PushDeliveryTask) Category() taskmanager.TaskCategory { return taskmanager.TaskCategorySystem }
func (t *PushDeliveryTask) IsHidden() bool       { return false }
func (t *PushDeliveryTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 15 * 1000}}
}
func (t *PushDeliveryTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.w == nil {
		progress.Report(100, "push delivery unavailable")
		return nil
	}
	// Drain until empty or a cap, so a burst clears within one tick.
	total := 0
	for i := 0; i < 20; i++ {
		n, err := t.w.RunOnce(ctx)
		if err != nil {
			return fmt.Errorf("push delivery: %w", err)
		}
		total += n
		if n == 0 {
			break
		}
	}
	progress.Report(100, fmt.Sprintf("processed %d deliveries", total))
	return nil
}
```

Place `PushDeliveryTask` in package `push` but it imports `taskmanager`; that's fine (one-way). Add imports `fmt`, `taskmanager`.

- [ ] **Step 5: Build, full push tests, commit**

```bash
GOWORK=off go build ./internal/push/ && GOWORK=off go test ./internal/push/ -v 2>&1 | tail -6
git add internal/push/worker.go internal/push/worker_test.go internal/push/task.go
git commit -m "feat(push): delivery worker, store-backed queue, scheduled task"
```

---

### Task 10: Web Push transport

**Files:**
- Create: `internal/push/transport_webpush.go`, `internal/push/transport_webpush_test.go`

- [ ] **Step 1: Add the dependency**

```bash
GOWORK=off go get github.com/SherClockHolmes/webpush-go@latest
```

- [ ] **Step 2: Implement**

The webpush token is a JSON subscription `{endpoint, keys:{p256dh, auth}}`. Send marshals the payload, signs with VAPID, and POSTs. Map results: 201/200 → Sent; 404/410 → Dead (subscription gone); 429 → SoftFail with `Retry-After`; other 5xx/timeout → SoftFail.

```go
package push

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

type WebPushTransport struct {
	cfg     func(ctx context.Context) WebPushConfig
	client  *http.Client
}

func NewWebPushTransport(cfg func(ctx context.Context) WebPushConfig) *WebPushTransport {
	return &WebPushTransport{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
}

func (t *WebPushTransport) Name() string { return TransportWebPush }
func (t *WebPushTransport) Configured() bool {
	return t.cfg(context.Background()).Configured()
}

func (t *WebPushTransport) Send(ctx context.Context, token string, payload Payload) (SendResult, time.Duration, error) {
	cfg := t.cfg(ctx)
	if !cfg.Configured() {
		return ResultSoftFail, 0, nil
	}
	var sub webpush.Subscription
	if err := json.Unmarshal([]byte(token), &sub); err != nil {
		return ResultDead, 0, err // malformed subscription is unrecoverable
	}
	body, _ := json.Marshal(map[string]any{
		"id":       payload.NotificationID,
		"title":    payload.Title,
		"body":     payload.Body,
		"link":     payload.Link,
		"category": payload.Category,
	})
	resp, err := webpush.SendNotificationWithContext(ctx, body, &sub, &webpush.Options{
		Subscriber:      cfg.Subject,
		VAPIDPublicKey:  cfg.VAPIDPublic,
		VAPIDPrivateKey: cfg.VAPIDPrivate,
		TTL:             86400,
		HTTPClient:      t.client,
	})
	if err != nil {
		return ResultSoftFail, 0, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return ResultSent, 0, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ResultDead, 0, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		ra := parseRetryAfter(resp.Header.Get("Retry-After"))
		return ResultSoftFail, ra, nil
	default:
		return ResultSoftFail, 0, nil
	}
}

func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		return time.Duration(secs) * time.Second
	}
	return 0
}
```

- [ ] **Step 3: Test (against the library's behavior with an httptest endpoint)**

Test `parseRetryAfter` directly (pure), and a `Configured()` false→ SoftFail short-circuit. Full HTTP-path testing of webpush-go requires a real subscription with valid keys; assert what's cheaply assertable (retry-after parsing, malformed-subscription → Dead, unconfigured → SoftFail). Document that end-to-end webpush is exercised in the integration smoke (Task 16) against a browser subscription.

```go
package push

import (
	"context"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	if parseRetryAfter("120") != 2*time.Minute {
		t.Fatal("seconds form")
	}
	if parseRetryAfter("") != 0 || parseRetryAfter("garbage") != 0 {
		t.Fatal("empty/garbage → 0")
	}
}

func TestWebPush_MalformedSubscriptionIsDead(t *testing.T) {
	tr := NewWebPushTransport(func(context.Context) WebPushConfig {
		return WebPushConfig{VAPIDPublic: "p", VAPIDPrivate: "k", Subject: "mailto:a@b.c"}
	})
	res, _, _ := tr.Send(context.Background(), "{not json", Payload{})
	if res != ResultDead {
		t.Fatalf("malformed sub → dead, got %v", res)
	}
}

func TestWebPush_UnconfiguredSoftFails(t *testing.T) {
	tr := NewWebPushTransport(func(context.Context) WebPushConfig { return WebPushConfig{} })
	res, _, _ := tr.Send(context.Background(), `{"endpoint":"x"}`, Payload{})
	if res != ResultSoftFail {
		t.Fatalf("unconfigured → soft fail, got %v", res)
	}
}
```

- [ ] **Step 4: Run, tidy, commit**

```bash
GOWORK=off go mod tidy && GOWORK=off go test ./internal/push/ -run 'WebPush|RetryAfter' -v
git add internal/push/transport_webpush.go internal/push/transport_webpush_test.go go.mod go.sum
git commit -m "feat(push): web push transport"
```

---

### Task 11: APNs transport

**Files:**
- Create: `internal/push/transport_apns.go`, `internal/push/transport_apns_test.go`

- [ ] **Step 1: Dependency**

```bash
GOWORK=off go get github.com/sideshow/apns2@latest
```

- [ ] **Step 2: Implement**

Token-based (.p8) APNs. Build the client lazily from config (P8Key/KeyID/TeamID); set topic = BundleID; collapse id = notification id. Map APNs reasons: `BadDeviceToken`/`Unregistered`/`DeviceTokenNotForTopic` → Dead; `TooManyRequests` → SoftFail+RetryAfter; 5xx/`InternalServerError`/timeout → SoftFail; 200 → Sent.

```go
package push

import (
	"context"
	"strconv"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"
)

type APNsTransport struct {
	cfg func(ctx context.Context) APNsConfig
}

func NewAPNsTransport(cfg func(ctx context.Context) APNsConfig) *APNsTransport {
	return &APNsTransport{cfg: cfg}
}

func (t *APNsTransport) Name() string       { return TransportAPNs }
func (t *APNsTransport) Configured() bool    { return t.cfg(context.Background()).Configured() }

func (t *APNsTransport) Send(ctx context.Context, deviceToken string, p Payload) (SendResult, time.Duration, error) {
	cfg := t.cfg(ctx)
	if !cfg.Configured() {
		return ResultSoftFail, 0, nil
	}
	authKey, err := token.AuthKeyFromBytes([]byte(cfg.P8Key))
	if err != nil {
		return ResultSoftFail, 0, err
	}
	client := apns2.NewTokenClient(&token.Token{AuthKey: authKey, KeyID: cfg.KeyID, TeamID: cfg.TeamID}).Production()
	// NOTE: Production() vs Development() should be configurable; default Production.

	notif := &apns2.Notification{
		DeviceToken: deviceToken,
		Topic:       cfg.BundleID,
		CollapseID:  strconv.FormatInt(p.NotificationID, 10),
		Payload: payload.NewPayload().
			AlertTitle(p.Title).
			AlertBody(p.Body).
			Custom("link", p.Link).
			Custom("category", p.Category).
			Custom("notification_id", p.NotificationID),
	}
	resp, err := client.PushWithContext(ctx, notif)
	if err != nil {
		return ResultSoftFail, 0, err // network/timeout
	}
	switch resp.Reason {
	case "":
		if resp.StatusCode == 200 {
			return ResultSent, 0, nil
		}
		return ResultSoftFail, 0, nil
	case apns2.ReasonBadDeviceToken, apns2.ReasonUnregistered, apns2.ReasonDeviceTokenNotForTopic:
		return ResultDead, 0, nil
	case apns2.ReasonTooManyRequests:
		return ResultSoftFail, 30 * time.Second, nil
	default:
		return ResultSoftFail, 0, nil
	}
}
```

Verify the exact apns2 API (`token.AuthKeyFromBytes`, `apns2.Reason*` constants, `PushWithContext`) against the installed version — the package's surface is stable but confirm constant names. Building a fresh client per Send is acceptable for v1 (low push volume); a cached client keyed by config hash is a future optimization.

- [ ] **Step 3: Test**

Unit-test the reason→result mapping by extracting it to a pure helper `apnsResult(reason string, status int) (SendResult, time.Duration)` and testing that table (BadDeviceToken→Dead, Unregistered→Dead, TooManyRequests→SoftFail+30s, ""+200→Sent, ""+503→SoftFail). Refactor `Send` to call the helper.

```go
func TestAPNsResultMapping(t *testing.T) {
	cases := []struct {
		reason string
		status int
		want   SendResult
	}{
		{"", 200, ResultSent},
		{apns2.ReasonUnregistered, 410, ResultDead},
		{apns2.ReasonBadDeviceToken, 400, ResultDead},
		{apns2.ReasonTooManyRequests, 429, ResultSoftFail},
		{"", 503, ResultSoftFail},
	}
	for _, c := range cases {
		got, _ := apnsResult(c.reason, c.status)
		if got != c.want {
			t.Fatalf("reason %q status %d: got %v want %v", c.reason, c.status, got, c.want)
		}
	}
}
```

- [ ] **Step 4: Run, tidy, commit**

```bash
GOWORK=off go mod tidy && GOWORK=off go test ./internal/push/ -run APNs -v
git add internal/push/transport_apns.go internal/push/transport_apns_test.go go.mod go.sum
git commit -m "feat(push): apns transport"
```

---

### Task 12: FCM transport

**Files:**
- Create: `internal/push/transport_fcm.go`, `internal/push/transport_fcm_test.go`

- [ ] **Step 1: Dependency**

```bash
GOWORK=off go get firebase.google.com/go/v4@latest google.golang.org/api@latest
```

- [ ] **Step 2: Implement**

FCM via the Admin SDK using service-account JSON (`option.WithCredentialsJSON`). Build the messaging client from config; send a `messaging.Message` with notification + data. Map errors: `messaging.IsUnregistered(err)`/`IsInvalidArgument` (bad token) → Dead; quota/unavailable → SoftFail; success → Sent.

```go
package push

import (
	"context"
	"strconv"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

type FCMTransport struct {
	cfg func(ctx context.Context) FCMConfig
}

func NewFCMTransport(cfg func(ctx context.Context) FCMConfig) *FCMTransport {
	return &FCMTransport{cfg: cfg}
}

func (t *FCMTransport) Name() string    { return TransportFCM }
func (t *FCMTransport) Configured() bool { return t.cfg(context.Background()).Configured() }

func (t *FCMTransport) Send(ctx context.Context, deviceToken string, p Payload) (SendResult, time.Duration, error) {
	cfg := t.cfg(ctx)
	if !cfg.Configured() {
		return ResultSoftFail, 0, nil
	}
	app, err := firebase.NewApp(ctx, nil, option.WithCredentialsJSON([]byte(cfg.ServiceAccountJSON)))
	if err != nil {
		return ResultSoftFail, 0, err
	}
	client, err := app.Messaging(ctx)
	if err != nil {
		return ResultSoftFail, 0, err
	}
	_, err = client.Send(ctx, &messaging.Message{
		Token: deviceToken,
		Notification: &messaging.Notification{Title: p.Title, Body: p.Body},
		Data: map[string]string{
			"notification_id": strconv.FormatInt(p.NotificationID, 10),
			"link":            p.Link,
			"category":        p.Category,
		},
	})
	return fcmResult(err)
}

func fcmResult(err error) (SendResult, time.Duration, error) {
	if err == nil {
		return ResultSent, 0, nil
	}
	if messaging.IsUnregistered(err) || messaging.IsInvalidArgument(err) {
		return ResultDead, 0, err
	}
	if messaging.IsQuotaExceeded(err) || messaging.IsUnavailable(err) {
		return ResultSoftFail, 30 * time.Second, err
	}
	return ResultSoftFail, 0, err
}
```

Verify the `messaging.Is*` predicate names against the installed `firebase.google.com/go/v4` version.

- [ ] **Step 3: Test the pure mapper**

```go
func TestFCMResult_Success(t *testing.T) {
	if r, _, _ := fcmResult(nil); r != ResultSent {
		t.Fatal("nil err → sent")
	}
}
```

(The `Is*` predicates require constructed FCM errors; assert the nil-success path here and document that token-class mapping is covered by the worker's dead-token test via the fake transport. Keep `fcmResult` exported-to-package so a follow-up can add predicate tests if the SDK exposes constructors.)

- [ ] **Step 4: Run, tidy, commit**

```bash
GOWORK=off go mod tidy && GOWORK=off go test ./internal/push/ -run FCM -v
git add internal/push/transport_fcm.go internal/push/transport_fcm_test.go go.mod go.sum
git commit -m "feat(push): fcm transport"
```

---

### Task 13: HTTP handlers

**Files:**
- Create: `internal/api/handlers/push.go`, `internal/api/handlers/push_test.go`
- Modify: `internal/api/router.go`

- [ ] **Step 1: Handler**

Reuse the claims + device-header + profile-header extraction pattern from `internal/api/handlers/settings.go` (`apimw.GetUserID`, `X-Silo-Device-Id`, `X-Profile-Id`). The handler depends on `*push.Store` and `*push.Config`.

```go
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/push"
)

type PushHandler struct {
	store  *push.Store
	config *push.Config
}

func NewPushHandler(store *push.Store, config *push.Config) *PushHandler {
	return &PushHandler{store: store, config: config}
}

func (h *PushHandler) deviceID(r *http.Request) string { return r.Header.Get("X-Silo-Device-Id") }
func (h *PushHandler) profileID(r *http.Request) string { return r.Header.Get("X-Profile-Id") }

func (h *PushHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	deviceID := h.deviceID(r)
	if userID == 0 || deviceID == "" || h.profileID(r) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "device and profile headers required")
		return
	}
	var req struct {
		Transport string `json:"transport"`
		Token     string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" ||
		(req.Transport != push.TransportAPNs && req.Transport != push.TransportFCM && req.Transport != push.TransportWebPush) {
		writeError(w, http.StatusBadRequest, "bad_request", "transport and token required")
		return
	}
	if err := h.store.RegisterToken(r.Context(), userID, h.profileID(r), deviceID, req.Transport, req.Token); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to register token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PushHandler) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 || h.deviceID(r) == "" || h.profileID(r) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "device and profile headers required")
		return
	}
	if err := h.store.RevokeToken(r.Context(), userID, h.profileID(r), h.deviceID(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to revoke")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PushHandler) HandleListDevices(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	devices, err := h.store.ListDevices(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list devices")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (h *PushHandler) HandleToggleDevice(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	deviceID := chi.URLParam(r, "device_id")
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || deviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "device id and enabled required")
		return
	}
	switch err := h.store.SetDeviceEnabled(r.Context(), userID, deviceID, req.Enabled); err {
	case nil:
		w.WriteHeader(http.StatusNoContent)
	case push.ErrNotFound:
		writeError(w, http.StatusNotFound, "not_found", "device not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to update device")
	}
}

func (h *PushHandler) HandleWebPushKey(w http.ResponseWriter, r *http.Request) {
	cfg := h.config.WebPush(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"vapid_public_key": cfg.VAPIDPublic})
}

func (h *PushHandler) HandleAdminStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.config.Status(r.Context()))
}
```

- [ ] **Step 2: Routes**

In `router.go`, user group (near the notifications routes):

```go
r.Route("/notifications/push", func(r chi.Router) {
	r.Put("/device", pushHandler.HandleRegister)
	r.Delete("/device", pushHandler.HandleRevoke)
	r.Get("/devices", pushHandler.HandleListDevices)
	r.Put("/devices/{device_id}", pushHandler.HandleToggleDevice)
	r.Get("/webpush-key", pushHandler.HandleWebPushKey)
})
```

Admin group (RequireAdmin):

```go
r.Get("/push/status", pushHandler.HandleAdminStatus)
```

Nil-guard handler construction like neighbors; add `*push.Store`/`*push.Config` to `Dependencies` and construct `pushHandler` where other handlers are built.

- [ ] **Step 3: Handler tests**

`push_test.go` with a fake or real store over a test pool — follow the package's handler-test convention (the notifications handler tests use in-package fakes; if `PushHandler` takes concrete `*push.Store`, either add a tiny interface for testability or test against a Postgres pool like store-level tests do). Cover: register 400 without headers, register 204 happy path, revoke 204, toggle 404 unknown device, webpush-key returns configured public key (empty when unconfigured), admin status booleans.

Decision: introduce small interfaces `pushRegistry` and `pushConfigReader` on the handler so tests use fakes (mirrors how `events_ws` and `notifications` handlers were made testable). Refactor `NewPushHandler` to accept those interfaces; `*push.Store`/`*push.Config` satisfy them.

- [ ] **Step 4: Build, test, commit**

```bash
GOWORK=off go build ./... && GOWORK=off go test ./internal/api/handlers/ -run TestPush -v
git add internal/api/handlers/push.go internal/api/handlers/push_test.go internal/api/router.go
git commit -m "feat(push): registration, device management, and status endpoints"
```

---

### Task 14: Retention for deliveries

**Files:**
- Modify: `internal/taskmanager/tasks/notifications_retention.go`

- [ ] **Step 1: Extend the retention task**

The retention task already purges old notifications. Add a `PushPurger` dependency (interface `PurgeTerminal(ctx, before) (int64, error)`, satisfied by `*push.Store`) and, in `Execute`, also delete terminal deliveries older than 7 days:

```go
// in the task struct: pushStore PushPurger  (nil-tolerant)
// in Execute, after the notifications purge:
if t.pushStore != nil {
	if n, err := t.pushStore.PurgeTerminal(ctx, time.Now().AddDate(0, 0, -7)); err != nil {
		slog.WarnContext(ctx, "push: purge terminal deliveries failed", "error", err)
	} else {
		purgedPush = n
	}
}
```

Add a `PushPurger` interface in the tasks package and a constructor param (or setter). Update the existing retention-task test to assert the push purge is invoked when the dependency is present (extend the fake).

- [ ] **Step 2: Build, test, commit**

```bash
GOWORK=off go build ./... && GOWORK=off go test ./internal/taskmanager/tasks/ -v 2>&1 | tail -4
git add internal/taskmanager/tasks/notifications_retention.go internal/taskmanager/tasks/notifications_retention_test.go
git commit -m "feat(push): purge terminal deliveries in retention task"
```

---

### Task 15: Wiring in main.go

**Files:**
- Modify: `cmd/silo/main.go`

- [ ] **Step 1: Presence registry**

Near the events hub / eventsHub setup. If Redis is configured, use the Redis registry; else memory:

```go
var presenceRegistry presence.Registry
if cfg.Redis.URL != "" {
	if rc, err := cache.NewRedisClient(cfg.Redis); err == nil {
		presenceRegistry = presence.NewRedisRegistry(rc, 60*time.Second)
	}
}
if presenceRegistry == nil {
	presenceRegistry = presence.NewMemoryRegistry()
}
```

Pass `presenceRegistry` into `NewEventsHandler` (the param added in Task 4).

- [ ] **Step 2: Push subsystem**

After the notifications service is constructed (Task-16-of-Phase-1 wiring, `notificationsSvc`):

```go
pushStore := push.NewStore(pool)
pushConfig := push.NewConfig(settingsRepo) // the encrypted settings repo
notificationsSvc.SetPushEnqueuer(push.NewEnqueuer(pushStore, 30*time.Second, time.Now))

pushTransports := []push.Transport{
	push.NewWebPushTransport(pushConfig.WebPush),
	push.NewAPNsTransport(pushConfig.APNs),
	push.NewFCMTransport(pushConfig.FCM),
}
pushWorker := push.NewWorker(push.NewStoreQueue(pushStore), presenceRegistry, pushTransports, time.Now)
```

`settingsRepo` must be the encrypted wrapper (`EncryptedSettingsRepo`) and satisfy `push.settingsReader` (`Get(ctx, key)`); verify and adapt.

- [ ] **Step 3: Register task + handler + retention dep**

```go
taskMgr.Register(push.NewPushDeliveryTask(pushWorker))
// retention task gains the push purger:
taskMgr.Register(tasks.NewNotificationsRetentionTask(notificationsStore, pushStore))
```

(Adjust the retention constructor to its Task-14 signature.) Construct `handlers.NewPushHandler(pushStore, pushConfig)` and thread into the router deps.

- [ ] **Step 4: Build + smoke boot**

```bash
GOWORK=off go build ./... && GOWORK=off go vet ./internal/push/ ./internal/presence/ ./cmd/...
```

Boot (port 18080 to avoid the 8080 SSH tunnel; fresh scratch DB if needed).
Commands assume the repository root is the cwd.

```bash
docker compose up -d postgres redis
GOWORK=off go build -o ./bin/silo-push ./cmd/silo && (PORT=18080 JF_PORT=18096 ./bin/silo-push > ./silo-push.log 2>&1 &)
sleep 8 && grep -iE 'listening|fatal|push' ./silo-push.log | head
```

Expected: listening, task manager started (push_delivery registered), no fatal.

- [ ] **Step 5: Commit**

```bash
git add cmd/silo/main.go
git commit -m "feat(push): wire presence, store, enqueuer, transports, worker, task"
```

---

### Task 16: Full verification + E2E smoke

- [ ] **Step 1: Suite + lint**

```bash
GOWORK=off go test ./internal/push/ ./internal/presence/ ./internal/notifications/ ./internal/api/handlers/ ./internal/taskmanager/tasks/ 2>&1 | tail -8
GOWORK=off go build ./... && make lint 2>&1 | tail -5 && make verify-local-paths
```

(Lint: only new-code issues block; pre-existing warnings allowed, consistent with Phase-1.)

- [ ] **Step 2: API smoke (Web Push path, end-to-end without a browser)**

Seed an admin user + profile + API key (as in Phase-1 smokes), set web push VAPID config via the admin settings path, then:

```bash
# register a (fake) webpush subscription token
curl -s -X PUT -H "Authorization: Bearer $KEY" -H 'X-Silo-Device-Id: devA' -H 'X-Profile-Id: p1' \
  -H 'Content-Type: application/json' \
  -d '{"transport":"webpush","token":"{\"endpoint\":\"https://example.invalid/x\",\"keys\":{\"p256dh\":\"...\",\"auth\":\"...\"}}"}' \
  -o /dev/null -w 'register: %{http_code}\n' http://localhost:18080/api/v1/notifications/push/device
# create a notification (e.g. an announcement to all) and confirm a push_deliveries row appears
# then watch the push_delivery task tick (it will attempt and mark the example.invalid token failed/dead)
docker exec silo-server-postgres-1 psql -U silo -d silo -tAc \
  "SELECT status, transport, attempts FROM push_deliveries ORDER BY id DESC LIMIT 3;"
curl -s -H "Authorization: Bearer $KEY" http://localhost:18080/api/v1/admin/push/status
```

Expected: register 204; a `push_deliveries` row created on notification; status JSON shows `webpush:true` once configured; the worker transitions the bogus-endpoint row to failed→dead over ticks (proving the loop). Presence gate: with a live WS connection open for the user, a freshly created notification's delivery should land `skipped` — verify by opening a WS (or note as a manual browser check).

- [ ] **Step 3: Cleanup scratch fixtures; final commit if needed**

- [ ] **Step 4: Branch** — work continues on `feat/core-notifications-server` (or a dedicated `feat/core-push` branch off it if you want push reviewed separately). PR body links the push spec + this plan.

---

## Self-review notes (resolved during writing)

- Import cycle avoided via `notifications.PushEnqueuer` interface; push imports notifications for the `*Notification` type (one direction).
- `user_devices` has no `updated_at`/`created_at` — `PruneTokenTx`/`RevokeToken` must not reference them (the plan includes a deliberate wrong-column line in Task 6 with the corrected version called out).
- Child suppression enforced in SQL (`EligibleDevices` join to `user_profiles.is_child`) so it applies uniformly to per-profile and user-wide notifications by inspecting each device's own profile.
- Presence fails open: `RedisRegistry.Connected` returns false on Redis error → push proceeds; the worker only skips on a positive presence result.
- Real transports (APNs/FCM) have thin pure mappers (`apnsResult`, `fcmResult`) that are unit-tested; the network paths are exercised by the worker's fake-transport tests and the integration smoke, because the SDKs need real credentials/tokens to exercise fully — flagged, not hidden.
- `not_before` is both the presence-grace clock (initial 30s) and the retry-backoff clock (1m/5m/30m), single mechanism.
- Several steps embed verify-greps (settings repo `Get` signature, apns2/FCM predicate names, `is_child` column, `user_devices` columns, taskmanager category constant) — align with reality rather than trusting the plan's guesses.
- Web Push is the only transport fully testable today (no mobile clients); APNs/FCM ship as built-and-unit-tested transports awaiting client integration (spec Future work).
