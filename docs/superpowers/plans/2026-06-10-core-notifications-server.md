# Core Notifications — Server Implementation Plan (Phase 1 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persisted per-user notifications in silo-server: inbox storage, materializer over the event hub, request lifecycle events, `notifications.send` plugin contract, REST API, WS channel, announcements, digest + retention tasks.

**Architecture:** Extend the existing `internal/notifications` package (today it only wraps the events hub) with a Postgres-backed inbox: a Store (pgx), a Service (validation, preferences, dedup, WS publish), and a Materializer subscribed to `events.Hub` running per-category matchers. The requests service gains event publishing on a new `ChannelRequests`. Clients receive `notification.created` frames on a new `ChannelNotifications` through the existing `/api/v1/events/ws`.

**Tech Stack:** Go 1.26, chi router, pgx v5, Goose SQL migrations, existing `internal/events` hub, `internal/taskmanager` for scheduled jobs.

**Spec:** `docs/superpowers/specs/2026-06-10-core-notifications-design.md`

Commands assume the repository root is the cwd. Web UI (Phase 2) and mobile (Phase 3) are separate plans.

---

## File map

| File | Action | Responsibility |
|---|---|---|
| `migrations/sql/<timestamped>_core_notifications.sql` | create | tables: `notifications`, `notification_preferences`, `announcements` |
| `internal/events/types.go` | modify | add `ChannelNotifications`, `ChannelRequests` |
| `internal/notifications/types.go` | create | Notification, Preference, Announcement structs + category/type constants |
| `internal/notifications/store.go` | create | Store interface + pgx repository |
| `internal/notifications/store_test.go` | create | repository tests (fake-free SQL tests follow repo convention; service tests use fakes) |
| `internal/notifications/service.go` | create | Create/list/read/dismiss/prefs/announcements; WS publish |
| `internal/notifications/service_test.go` | create | service tests with fake store |
| `internal/notifications/materializer.go` | create | hub subscription, matcher registry, isolation |
| `internal/notifications/matcher_request.go` | create | `request.*` → notifications |
| `internal/notifications/matcher_send.go` | create | `notifications.send` contract |
| `internal/notifications/matcher_content.go` | create | watchlist/favorites/in-progress matching + burst guard |
| `internal/notifications/matcher_admin.go` | create | job/scan failures → admin users, throttled |
| `internal/notifications/matcher_test.go` | create | matcher tests with synthetic envelopes |
| `internal/requests/events.go` | create | EventPublisher hook + payload struct |
| `internal/requests/service.go` | modify | publish at submit/approve/decline/cancel/complete/fail |
| `internal/api/handlers/notifications.go` | create | REST handlers |
| `internal/api/handlers/notifications_test.go` | create | handler tests |
| `internal/api/handlers/events_ws.go` | modify | channel grants + unread-count snapshot |
| `internal/api/router.go` | modify | mount routes |
| `internal/taskmanager/tasks/notifications_digest.go` | create | nightly digest task |
| `internal/taskmanager/tasks/notifications_retention.go` | create | retention purge task |
| `cmd/silo/main.go` | modify | wire store/service/materializer, request publisher, register tasks |

---

### Task 1: Migration

**Files:**
- Create: `migrations/sql/<generated>_core_notifications.sql` (via make)

- [ ] **Step 1: Generate the migration file**

```bash
make migrate-create NAME=core_notifications
```

Expected: prints the created file path under `migrations/sql/` with a UTC timestamp prefix.

- [ ] **Step 2: Write the migration**

Replace the generated file's contents with:

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.notifications (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id    text NULL REFERENCES public.user_profiles(id) ON DELETE CASCADE,
    category      text NOT NULL CHECK (category IN ('request','content','announcement','system','admin')),
    type          text NOT NULL,
    title         text NOT NULL,
    body          text NOT NULL DEFAULT '',
    link          text NULL,
    item_id       text NULL,
    source_event  text NULL,
    dedup_ref     text NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    read_at       timestamptz NULL,
    dismissed_at  timestamptz NULL,
    expires_at    timestamptz NULL
);

CREATE INDEX notifications_inbox_idx
    ON public.notifications (user_id, created_at DESC)
    WHERE dismissed_at IS NULL;

CREATE UNIQUE INDEX notifications_dedup_idx
    ON public.notifications (user_id, type, dedup_ref)
    WHERE dedup_ref IS NOT NULL;

CREATE TABLE public.notification_preferences (
    user_id   integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    category  text NOT NULL CHECK (category IN ('request','content','system','admin','content_digest')),
    enabled   boolean NOT NULL,
    PRIMARY KEY (user_id, category)
);

CREATE TABLE public.announcements (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title       text NOT NULL,
    body        text NOT NULL DEFAULT '',
    audience    jsonb NOT NULL,
    created_by  integer NULL REFERENCES public.users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE public.announcements;
DROP TABLE public.notification_preferences;
DROP TABLE public.notifications;
-- +goose StatementEnd
```

Note: `item_id` is `text` because catalog content ids are text (`media_items.content_id`, see `user_watchlist.media_item_id`).

- [ ] **Step 3: Apply and verify**

```bash
docker compose up -d postgres redis
make migrate-up
make migrate-status
```

Expected: new migration listed as applied; no errors.

- [ ] **Step 4: Commit**

```bash
git add migrations/sql/
git commit -m "feat(notifications): add notifications, preferences, announcements tables"
```

---

### Task 2: Event channels

**Files:**
- Modify: `internal/events/types.go:10-30`

- [ ] **Step 1: Add the channels**

In the `const` block add:

```go
ChannelNotifications EventChannel = "notifications"
ChannelRequests      EventChannel = "requests"
```

Append both to `AllChannels`.

- [ ] **Step 2: Build**

```bash
go build ./internal/events/
```

Expected: success.

- [ ] **Step 3: Commit**

```bash
git add internal/events/types.go
git commit -m "feat(events): add notifications and requests channels"
```

---

### Task 3: Domain types

**Files:**
- Create: `internal/notifications/types.go`

- [ ] **Step 1: Write the types**

```go
package notifications

import "time"

type Category string

const (
	CategoryRequest      Category = "request"
	CategoryContent      Category = "content"
	CategoryAnnouncement Category = "announcement"
	CategorySystem       Category = "system"
	CategoryAdmin        Category = "admin"
	// CategoryContentDigest is a preference key only; digest rows are stored
	// with CategoryContent and type "content.digest".
	CategoryContentDigest Category = "content_digest"
)

// MutableCategories are valid notification_preferences rows.
var MutableCategories = []Category{
	CategoryRequest, CategoryContent, CategorySystem, CategoryAdmin, CategoryContentDigest,
}

type Notification struct {
	ID          int64      `json:"id"`
	UserID      int        `json:"user_id"`
	ProfileID   *string    `json:"profile_id,omitempty"`
	Category    Category   `json:"category"`
	Type        string     `json:"type"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	Link        *string    `json:"link,omitempty"`
	ItemID      *string    `json:"item_id,omitempty"`
	SourceEvent string     `json:"-"`
	DedupRef    string     `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
	DismissedAt *time.Time `json:"-"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

type Preference struct {
	Category Category `json:"category"`
	Enabled  bool     `json:"enabled"`
}

type Announcement struct {
	ID        int64      `json:"id"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Audience  Audience   `json:"audience"`
	CreatedBy *int       `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Audience: exactly one field set.
type Audience struct {
	All        bool  `json:"all,omitempty"`
	UserIDs    []int `json:"user_ids,omitempty"`
	LibraryIDs []int `json:"library_ids,omitempty"`
}

type ListFilter struct {
	UserID     int
	ProfileID  string // active profile; matches profile_id IS NULL OR profile_id = this
	ChildSafe  bool   // true => exclude request/system/admin categories
	UnreadOnly bool
	Category   Category // optional
	Cursor     int64    // created-before id cursor; 0 = first page
	Limit      int
}
```

- [ ] **Step 2: Build and commit**

```bash
go build ./internal/notifications/ && git add internal/notifications/types.go && git commit -m "feat(notifications): domain types"
```

---

### Task 4: Store

**Files:**
- Create: `internal/notifications/store.go`
- Test: `internal/notifications/service_test.go` (fake store defined here in Task 6 — the repository is exercised by handler/service integration paths; follow `internal/requests` convention where `repository.go` has no standalone unit test)

- [ ] **Step 1: Write the Store interface and pgx repository**

```go
package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("notification not found")

type Store interface {
	Insert(ctx context.Context, n *Notification) (created bool, err error)
	List(ctx context.Context, f ListFilter) ([]*Notification, error)
	UnreadCount(ctx context.Context, userID int, profileID string, childSafe bool) (int, error)
	MarkRead(ctx context.Context, userID int, ids []int64) error
	MarkAllRead(ctx context.Context, userID int) error
	Dismiss(ctx context.Context, userID int, id int64) error
	Preferences(ctx context.Context, userID int) (map[Category]bool, error)
	SetPreference(ctx context.Context, userID int, c Category, enabled bool) error
	InsertAnnouncement(ctx context.Context, a *Announcement) error
	ListAnnouncements(ctx context.Context) ([]*Announcement, error)
	DeleteAnnouncement(ctx context.Context, id int64) error
	DismissUnreadByTypeRef(ctx context.Context, typ, dedupPrefix string) error
	PurgeOld(ctx context.Context, dismissedBefore, allBefore time.Time) (int64, error)
	AdminUserIDs(ctx context.Context) ([]int, error)
	UserIDsWithLibraryAccess(ctx context.Context, libraryID int) ([]int, error)
}

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const notificationColumns = `id, user_id, profile_id, category, type, title, body, link,
	item_id, source_event, dedup_ref, created_at, read_at, dismissed_at, expires_at`

func (r *Repository) Insert(ctx context.Context, n *Notification) (bool, error) {
	var dedup *string
	if n.DedupRef != "" {
		dedup = &n.DedupRef
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO notifications
			(user_id, profile_id, category, type, title, body, link, item_id, source_event, dedup_ref, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (user_id, type, dedup_ref) WHERE dedup_ref IS NOT NULL DO NOTHING
		RETURNING id, created_at`,
		n.UserID, n.ProfileID, n.Category, n.Type, n.Title, n.Body, n.Link, n.ItemID,
		nullable(n.SourceEvent), dedup, n.ExpiresAt)
	if err := row.Scan(&n.ID, &n.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // dedup conflict — already exists
		}
		return false, fmt.Errorf("insert notification: %w", err)
	}
	return true, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *Repository) List(ctx context.Context, f ListFilter) ([]*Notification, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	q := `SELECT ` + notificationColumns + ` FROM notifications
		WHERE user_id = $1 AND dismissed_at IS NULL
		  AND (profile_id IS NULL OR profile_id = $2)
		  AND (expires_at IS NULL OR expires_at > now())`
	args := []any{f.UserID, f.ProfileID}
	if f.ChildSafe {
		q += ` AND category NOT IN ('request','system','admin')`
	}
	if f.UnreadOnly {
		q += ` AND read_at IS NULL`
	}
	if f.Category != "" {
		args = append(args, f.Category)
		q += fmt.Sprintf(` AND category = $%d`, len(args))
	}
	if f.Cursor > 0 {
		args = append(args, f.Cursor)
		q += fmt.Sprintf(` AND id < $%d`, len(args))
	}
	args = append(args, f.Limit)
	q += fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args))

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()
	var out []*Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func scanNotification(row pgx.Row) (*Notification, error) {
	n := &Notification{}
	var sourceEvent, dedupRef *string
	if err := row.Scan(&n.ID, &n.UserID, &n.ProfileID, &n.Category, &n.Type, &n.Title,
		&n.Body, &n.Link, &n.ItemID, &sourceEvent, &dedupRef,
		&n.CreatedAt, &n.ReadAt, &n.DismissedAt, &n.ExpiresAt); err != nil {
		return nil, fmt.Errorf("scan notification: %w", err)
	}
	if sourceEvent != nil {
		n.SourceEvent = *sourceEvent
	}
	if dedupRef != nil {
		n.DedupRef = *dedupRef
	}
	return n, nil
}

func (r *Repository) UnreadCount(ctx context.Context, userID int, profileID string, childSafe bool) (int, error) {
	q := `SELECT count(*) FROM notifications
		WHERE user_id = $1 AND dismissed_at IS NULL AND read_at IS NULL
		  AND (profile_id IS NULL OR profile_id = $2)
		  AND (expires_at IS NULL OR expires_at > now())`
	if childSafe {
		q += ` AND category NOT IN ('request','system','admin')`
	}
	var count int
	err := r.pool.QueryRow(ctx, q, userID, profileID).Scan(&count)
	return count, err
}

func (r *Repository) MarkRead(ctx context.Context, userID int, ids []int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE notifications SET read_at = now() WHERE user_id = $1 AND id = ANY($2) AND read_at IS NULL`,
		userID, ids)
	return err
}

func (r *Repository) MarkAllRead(ctx context.Context, userID int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL`, userID)
	return err
}

func (r *Repository) Dismiss(ctx context.Context, userID int, id int64) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE notifications SET dismissed_at = now() WHERE user_id = $1 AND id = $2 AND dismissed_at IS NULL`,
		userID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) Preferences(ctx context.Context, userID int) (map[Category]bool, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT category, enabled FROM notification_preferences WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Category]bool{}
	for rows.Next() {
		var c Category
		var enabled bool
		if err := rows.Scan(&c, &enabled); err != nil {
			return nil, err
		}
		out[c] = enabled
	}
	return out, rows.Err()
}

func (r *Repository) SetPreference(ctx context.Context, userID int, c Category, enabled bool) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_preferences (user_id, category, enabled) VALUES ($1,$2,$3)
		ON CONFLICT (user_id, category) DO UPDATE SET enabled = EXCLUDED.enabled`,
		userID, c, enabled)
	return err
}

func (r *Repository) InsertAnnouncement(ctx context.Context, a *Announcement) error {
	return r.pool.QueryRow(ctx, `
		INSERT INTO announcements (title, body, audience, created_by, expires_at)
		VALUES ($1,$2,$3,$4,$5) RETURNING id, created_at`,
		a.Title, a.Body, a.Audience, a.CreatedBy, a.ExpiresAt).Scan(&a.ID, &a.CreatedAt)
}

func (r *Repository) ListAnnouncements(ctx context.Context) ([]*Announcement, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, title, body, audience, created_by, created_at, expires_at
		FROM announcements ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Announcement
	for rows.Next() {
		a := &Announcement{}
		if err := rows.Scan(&a.ID, &a.Title, &a.Body, &a.Audience, &a.CreatedBy, &a.CreatedAt, &a.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) DeleteAnnouncement(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM announcements WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DismissUnreadByTypeRef dismisses unread rows whose dedup_ref starts with
// dedupPrefix (used when an announcement is deleted/expires).
func (r *Repository) DismissUnreadByTypeRef(ctx context.Context, typ, dedupPrefix string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notifications SET dismissed_at = now()
		WHERE type = $1 AND dedup_ref LIKE $2 || '%' AND read_at IS NULL AND dismissed_at IS NULL`,
		typ, dedupPrefix)
	return err
}

func (r *Repository) PurgeOld(ctx context.Context, dismissedBefore, allBefore time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM notifications
		WHERE (dismissed_at IS NOT NULL AND dismissed_at < $1)
		   OR created_at < $2
		   OR (expires_at IS NOT NULL AND expires_at < now() AND read_at IS NULL)`,
		dismissedBefore, allBefore)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) AdminUserIDs(ctx context.Context) ([]int, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id FROM users WHERE role = 'admin' AND enabled = true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UserIDsWithLibraryAccess returns enabled users whose library_ids is NULL
// (all libraries) or contains libraryID.
func (r *Repository) UserIDsWithLibraryAccess(ctx context.Context, libraryID int) ([]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id FROM users
		WHERE enabled = true AND (library_ids IS NULL OR $1 = ANY(library_ids))`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
```

- [ ] **Step 2: Build**

```bash
go build ./internal/notifications/
```

Expected: success.

- [ ] **Step 3: Commit**

```bash
git add internal/notifications/store.go
git commit -m "feat(notifications): pgx store"
```

---

### Task 5: Service — Create with preferences, dedup, WS publish

**Files:**
- Create: `internal/notifications/service.go`
- Test: `internal/notifications/service_test.go`

- [ ] **Step 1: Write the failing tests (fake store + recording hub)**

```go
package notifications

import (
	"context"
	"testing"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

type fakeStore struct {
	Store // embed for unimplemented methods (nil-panic = test failure, intended)
	inserted []*Notification
	prefs    map[int]map[Category]bool
	admins   []int
	libUsers map[int][]int
}

func (f *fakeStore) Insert(_ context.Context, n *Notification) (bool, error) {
	for _, existing := range f.inserted {
		if existing.UserID == n.UserID && existing.Type == n.Type &&
			n.DedupRef != "" && existing.DedupRef == n.DedupRef {
			return false, nil
		}
	}
	n.ID = int64(len(f.inserted) + 1)
	f.inserted = append(f.inserted, n)
	return true, nil
}

func (f *fakeStore) Preferences(_ context.Context, userID int) (map[Category]bool, error) {
	if p, ok := f.prefs[userID]; ok {
		return p, nil
	}
	return map[Category]bool{}, nil
}

func (f *fakeStore) AdminUserIDs(context.Context) ([]int, error) { return f.admins, nil }

func (f *fakeStore) UserIDsWithLibraryAccess(_ context.Context, lib int) ([]int, error) {
	return f.libUsers[lib], nil
}

func newTestService(store Store) (*Service, *evt.Hub) {
	hub := evt.NewHub("test-node", nil)
	return NewService(store, hub), hub
}

func TestCreate_InsertsAndPublishes(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	ch, unsub := hub.Subscribe()
	defer unsub()

	err := svc.Create(context.Background(), CreateInput{
		UserID: 7, Category: CategoryRequest, Type: "request.approved",
		Title: "Approved", DedupRef: "req-1:approved",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(store.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(store.inserted))
	}
	env := <-ch
	if env.Channel != evt.ChannelNotifications || env.Event != "notification.created" || env.UserID != 7 {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}

func TestCreate_MutedCategorySkipped(t *testing.T) {
	store := &fakeStore{prefs: map[int]map[Category]bool{
		7: {CategoryRequest: false},
	}}
	svc, _ := newTestService(store)

	err := svc.Create(context.Background(), CreateInput{
		UserID: 7, Category: CategoryRequest, Type: "request.approved", Title: "x",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(store.inserted) != 0 {
		t.Fatalf("muted category was inserted")
	}
}

func TestCreate_AnnouncementNotMutable(t *testing.T) {
	store := &fakeStore{prefs: map[int]map[Category]bool{
		7: {CategoryAnnouncement: false}, // bogus row must be ignored
	}}
	svc, _ := newTestService(store)
	err := svc.Create(context.Background(), CreateInput{
		UserID: 7, Category: CategoryAnnouncement, Type: "announcement", Title: "x",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(store.inserted) != 1 {
		t.Fatalf("announcement was muted; must not be mutable")
	}
}

func TestCreate_DedupNoDoubleInsertNoSecondPublish(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	ch, unsub := hub.Subscribe()
	defer unsub()

	in := CreateInput{UserID: 7, Category: CategoryRequest, Type: "request.approved",
		Title: "Approved", DedupRef: "req-1:approved"}
	_ = svc.Create(context.Background(), in)
	_ = svc.Create(context.Background(), in)

	if len(store.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(store.inserted))
	}
	<-ch
	select {
	case env := <-ch:
		t.Fatalf("second publish for deduped create: %+v", env)
	default:
	}
}

func TestCreate_ValidationErrors(t *testing.T) {
	svc, _ := newTestService(&fakeStore{})
	cases := []CreateInput{
		{Category: CategoryRequest, Type: "t", Title: "x"},             // no user
		{UserID: 1, Type: "t", Title: "x"},                             // no category
		{UserID: 1, Category: Category("bogus"), Type: "t", Title: "x"}, // bad category
		{UserID: 1, Category: CategoryRequest, Title: "x"},             // no type
		{UserID: 1, Category: CategoryRequest, Type: "t"},              // no title
	}
	for i, in := range cases {
		if err := svc.Create(context.Background(), in); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/notifications/ -run TestCreate -v
```

Expected: FAIL — `CreateInput`, `NewService`, `Service` undefined.

- [ ] **Step 3: Implement the service**

```go
package notifications

import (
	"context"
	"fmt"
	"log/slog"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

type CreateInput struct {
	UserID      int
	ProfileID   string // optional
	Category    Category
	Type        string
	Title       string
	Body        string
	Link        string
	ItemID      string
	SourceEvent string
	DedupRef    string
}

type Service struct {
	store Store
	hub   *evt.Hub
}

func NewService(store Store, hub *evt.Hub) *Service {
	return &Service{store: store, hub: hub}
}

func validCategory(c Category) bool {
	switch c {
	case CategoryRequest, CategoryContent, CategoryAnnouncement, CategorySystem, CategoryAdmin:
		return true
	}
	return false
}

// Create validates, applies preferences, inserts (idempotent on DedupRef) and
// publishes notification.created for live clients. A dedup conflict is not an
// error and publishes nothing.
func (s *Service) Create(ctx context.Context, in CreateInput) error {
	if in.UserID <= 0 {
		return fmt.Errorf("notifications: user_id required")
	}
	if !validCategory(in.Category) {
		return fmt.Errorf("notifications: invalid category %q", in.Category)
	}
	if in.Type == "" || in.Title == "" {
		return fmt.Errorf("notifications: type and title required")
	}

	if in.Category != CategoryAnnouncement {
		prefs, err := s.store.Preferences(ctx, in.UserID)
		if err != nil {
			return fmt.Errorf("notifications: load preferences: %w", err)
		}
		prefCategory := in.Category
		if in.Type == TypeContentDigest {
			prefCategory = CategoryContentDigest
		}
		if enabled, ok := prefs[prefCategory]; ok && !enabled {
			return nil
		}
		// content_digest is opt-in: absent row means disabled.
		if prefCategory == CategoryContentDigest {
			if enabled, ok := prefs[CategoryContentDigest]; !ok || !enabled {
				return nil
			}
		}
	}

	n := &Notification{
		UserID:      in.UserID,
		Category:    in.Category,
		Type:        in.Type,
		Title:       in.Title,
		Body:        in.Body,
		SourceEvent: in.SourceEvent,
		DedupRef:    in.DedupRef,
	}
	if in.ProfileID != "" {
		n.ProfileID = &in.ProfileID
	}
	if in.Link != "" {
		n.Link = &in.Link
	}
	if in.ItemID != "" {
		n.ItemID = &in.ItemID
	}

	created, err := s.store.Insert(ctx, n)
	if err != nil {
		return fmt.Errorf("notifications: insert: %w", err)
	}
	if !created {
		return nil
	}

	if err := s.hub.PublishJSON(ctx, evt.ChannelNotifications, "notification.created", n, evt.PublishOptions{
		UserID:    n.UserID,
		ProfileID: in.ProfileID,
		AdminOnly: n.Category == CategoryAdmin,
	}); err != nil {
		slog.Warn("notifications: publish failed", "error", err, "notification_id", n.ID)
	}
	return nil
}

const TypeContentDigest = "content.digest"
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/notifications/ -run TestCreate -v
```

Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/notifications/service.go internal/notifications/service_test.go
git commit -m "feat(notifications): create service with prefs, dedup, hub publish"
```

---

### Task 6: Service — announcements fan-out

**Files:**
- Modify: `internal/notifications/service.go`
- Test: `internal/notifications/service_test.go`

- [ ] **Step 1: Write failing tests**

Add to `service_test.go` (extend `fakeStore` with the announcement + list methods):

```go
func (f *fakeStore) InsertAnnouncement(_ context.Context, a *Announcement) error {
	a.ID = 1
	return nil
}
func (f *fakeStore) DismissUnreadByTypeRef(context.Context, string, string) error { return nil }
func (f *fakeStore) DeleteAnnouncement(context.Context, int64) error              { return nil }

func TestPublishAnnouncement_FanOutAll(t *testing.T) {
	store := &fakeStore{libUsers: map[int][]int{}, admins: []int{1}}
	store.allUsers = []int{1, 2, 3}
	svc, _ := newTestService(store)

	a := &Announcement{Title: "Maintenance", Audience: Audience{All: true}}
	if err := svc.PublishAnnouncement(context.Background(), a); err != nil {
		t.Fatalf("PublishAnnouncement: %v", err)
	}
	if len(store.inserted) != 3 {
		t.Fatalf("fanned out %d, want 3", len(store.inserted))
	}
	for _, n := range store.inserted {
		if n.Category != CategoryAnnouncement || n.DedupRef != "announcement-1" {
			t.Fatalf("bad row: %+v", n)
		}
	}
}

func TestPublishAnnouncement_FanOutUserIDs(t *testing.T) {
	store := &fakeStore{}
	svc, _ := newTestService(store)
	a := &Announcement{Title: "Hi", Audience: Audience{UserIDs: []int{5, 9}}}
	if err := svc.PublishAnnouncement(context.Background(), a); err != nil {
		t.Fatalf("PublishAnnouncement: %v", err)
	}
	if len(store.inserted) != 2 {
		t.Fatalf("fanned out %d, want 2", len(store.inserted))
	}
}
```

Add to `fakeStore`: field `allUsers []int` and method:

```go
func (f *fakeStore) AllEnabledUserIDs(context.Context) ([]int, error) { return f.allUsers, nil }
```

Add `AllEnabledUserIDs(ctx context.Context) ([]int, error)` to the `Store` interface in `store.go` and to `Repository`:

```go
func (r *Repository) AllEnabledUserIDs(ctx context.Context) ([]int, error) {
	rows, err := r.pool.Query(ctx, `SELECT id FROM users WHERE enabled = true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/notifications/ -run TestPublishAnnouncement -v
```

Expected: FAIL — `PublishAnnouncement` undefined.

- [ ] **Step 3: Implement**

Add to `service.go`:

```go
// PublishAnnouncement stores the announcement and fans it out to notifications
// rows at publish time, resolving the audience to user ids now.
func (s *Service) PublishAnnouncement(ctx context.Context, a *Announcement) error {
	if a.Title == "" {
		return fmt.Errorf("notifications: announcement title required")
	}
	set := 0
	if a.Audience.All {
		set++
	}
	if len(a.Audience.UserIDs) > 0 {
		set++
	}
	if len(a.Audience.LibraryIDs) > 0 {
		set++
	}
	if set != 1 {
		return fmt.Errorf("notifications: audience must set exactly one of all/user_ids/library_ids")
	}

	if err := s.store.InsertAnnouncement(ctx, a); err != nil {
		return fmt.Errorf("notifications: insert announcement: %w", err)
	}

	userIDs, err := s.resolveAudience(ctx, a.Audience)
	if err != nil {
		return err
	}
	for _, uid := range userIDs {
		in := CreateInput{
			UserID:   uid,
			Category: CategoryAnnouncement,
			Type:     "announcement",
			Title:    a.Title,
			Body:     a.Body,
			DedupRef: fmt.Sprintf("announcement-%d", a.ID),
		}
		if err := s.Create(ctx, in); err != nil {
			slog.Warn("notifications: announcement fan-out failed", "user_id", uid, "error", err)
		}
	}
	return nil
}

func (s *Service) resolveAudience(ctx context.Context, a Audience) ([]int, error) {
	switch {
	case a.All:
		return s.store.AllEnabledUserIDs(ctx)
	case len(a.UserIDs) > 0:
		return a.UserIDs, nil
	default:
		seen := map[int]struct{}{}
		var out []int
		for _, lib := range a.LibraryIDs {
			ids, err := s.store.UserIDsWithLibraryAccess(ctx, lib)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				if _, ok := seen[id]; !ok {
					seen[id] = struct{}{}
					out = append(out, id)
				}
			}
		}
		return out, nil
	}
}

// DeleteAnnouncement removes the announcement and dismisses its unread rows.
func (s *Service) DeleteAnnouncement(ctx context.Context, id int64) error {
	if err := s.store.DeleteAnnouncement(ctx, id); err != nil {
		return err
	}
	return s.store.DismissUnreadByTypeRef(ctx, "announcement", fmt.Sprintf("announcement-%d", id))
}
```

Note: expires_at on the announcement is copied onto each fanned-out row by setting `ExpiresAt` — add `ExpiresAt *time.Time` to `CreateInput`, set `n.ExpiresAt = in.ExpiresAt` in `Create`, and pass `ExpiresAt: a.ExpiresAt` in the fan-out loop. List/UnreadCount already exclude expired rows.

- [ ] **Step 4: Run tests, then full package**

```bash
go test ./internal/notifications/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notifications/
git commit -m "feat(notifications): announcement publish with audience fan-out"
```

---

### Task 7: Request lifecycle events

**Files:**
- Create: `internal/requests/events.go`
- Modify: `internal/requests/service.go` (`Approve` at :504, `Decline` at :522, `Cancel`, `CreateRequest`, and the reconciliation completion path in `targets.go`)
- Test: `internal/requests/service_test.go`

- [ ] **Step 1: Write the publisher hook**

`internal/requests/events.go`:

```go
package requests

import (
	"context"
	"log/slog"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// RequestEventPayload is the wire shape for request.* events on ChannelRequests.
type RequestEventPayload struct {
	RequestID string `json:"request_id"`
	UserID    int    `json:"user_id"`
	ProfileID string `json:"profile_id,omitempty"`
	Title     string `json:"title"`
	MediaType string `json:"media_type"`
	Quality   string `json:"quality,omitempty"`
	Status    string `json:"status"`
}

// SetEventsHub wires the events hub; nil is allowed (publishing becomes a no-op),
// matching the optional-dependency style of SetSecretResolver.
func (s *Service) SetEventsHub(hub *evt.Hub) { s.eventsHub = hub }

func (s *Service) publishRequestEvent(ctx context.Context, event string, req Request) {
	if s.eventsHub == nil {
		return
	}
	payload := RequestEventPayload{
		RequestID: req.ID,
		UserID:    req.RequestedByUserID,
		ProfileID: req.RequestedByProfileID,
		Title:     req.Title,
		MediaType: string(req.MediaType),
		Status:    string(req.Status),
	}
	if err := s.eventsHub.PublishJSON(ctx, evt.ChannelRequests, event, payload, evt.PublishOptions{
		UserID:    req.RequestedByUserID,
		ProfileID: req.RequestedByProfileID,
	}); err != nil {
		slog.Warn("requests: event publish failed", "event", event, "request_id", req.ID, "error", err)
	}
}
```

Add field `eventsHub *evt.Hub` to the `Service` struct in `service.go:65-74`.

Field names above (`RequestedByUserID`, `RequestedByProfileID`, `Title`, `MediaType`, `Status`) must match `internal/requests/types.go` — verify with `grep -n 'RequestedBy\|MediaType\|Title' internal/requests/types.go` and adjust to the actual struct fields before compiling.

- [ ] **Step 2: Publish at each transition**

- `CreateRequest` (after successful store insert): `s.publishRequestEvent(ctx, "request.submitted", created)`
- `Approve` (`service.go:519`, after `submitApprovedRequest` succeeds): `s.publishRequestEvent(ctx, "request.approved", *approved)`
- `Decline` (after `SetStatus` to declined): `s.publishRequestEvent(ctx, "request.declined", *declined)`
- `Cancel`: `s.publishRequestEvent(ctx, "request.cancelled", *cancelled)`
- Reconciliation completion/failure: in `targets.go`, where target status transitions land the request in completed or failed (locate with `grep -n 'StatusCompleted\|completed' internal/requests/targets.go`), publish `request.completed` / `request.failed`.

- [ ] **Step 3: Write failing test**

Add to `service_test.go` (it already constructs Services against a fake store — follow the file's existing fixture style):

```go
func TestApprove_PublishesRequestApproved(t *testing.T) {
	// Arrange a pending request in the existing fake store fixture,
	// attach a hub, subscribe, approve as admin viewer.
	hub := evt.NewHub("test", nil)
	ch, unsub := hub.Subscribe()
	defer unsub()

	svc := newServiceWithPendingRequest(t) // reuse/extend the file's existing helper for a pending request fixture
	svc.SetEventsHub(hub)

	if _, err := svc.Approve(context.Background(), adminViewer(), "req-1"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	env := <-ch
	if env.Channel != evt.ChannelRequests || env.Event != "request.approved" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}
```

(`newServiceWithPendingRequest` / `adminViewer`: if no equivalent helpers exist in `service_test.go`, add them following that file's fixture conventions — the test file already covers Approve flows, so a pending-request fixture exists to copy.)

- [ ] **Step 4: Run, implement until green**

```bash
go test ./internal/requests/ -run TestApprove_Publishes -v
```

Expected: PASS after wiring.

- [ ] **Step 5: Full requests package test + commit**

```bash
go test ./internal/requests/
git add internal/requests/
git commit -m "feat(requests): publish lifecycle events on ChannelRequests"
```

---

### Task 8: Materializer + request matcher

**Files:**
- Create: `internal/notifications/materializer.go`, `internal/notifications/matcher_request.go`
- Test: `internal/notifications/matcher_test.go`

- [ ] **Step 1: Write failing tests**

```go
package notifications

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

func publishAndSettle(t *testing.T, hub *evt.Hub, m *Materializer, env evt.Envelope) {
	t.Helper()
	if err := hub.Publish(context.Background(), env); err != nil {
		t.Fatalf("publish: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.Processed() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRequestMatcher_ApprovedCreatesNotification(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	data, _ := json.Marshal(RequestEventData{
		RequestID: "req-1", UserID: 7, Title: "Dune", MediaType: "movie", Status: "approved",
	})
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelRequests, Event: "request.approved", Data: data, UserID: 7,
	})

	if len(store.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(store.inserted))
	}
	n := store.inserted[0]
	if n.Category != CategoryRequest || n.Type != "request.approved" || n.UserID != 7 ||
		n.DedupRef != "req-1:approved" {
		t.Fatalf("bad notification: %+v", n)
	}
}

func TestRequestMatcher_SubmittedDoesNotNotifyRequester(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)

	data, _ := json.Marshal(RequestEventData{RequestID: "r", UserID: 7, Status: "pending"})
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelRequests, Event: "request.submitted", Data: data, UserID: 7,
	})
	if len(store.inserted) != 0 {
		t.Fatalf("request.submitted must not notify the requester")
	}
}

func TestMaterializer_MatcherPanicIsolated(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	m.register("panicker", func(context.Context, evt.Envelope) error { panic("boom") })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)

	data, _ := json.Marshal(RequestEventData{RequestID: "r", UserID: 7, Title: "T", Status: "approved"})
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelRequests, Event: "request.approved", Data: data, UserID: 7,
	})
	if len(store.inserted) != 1 {
		t.Fatalf("panicking matcher blocked others: inserted = %d", len(store.inserted))
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/notifications/ -run 'TestRequestMatcher|TestMaterializer' -v
```

Expected: FAIL — `Materializer`, `RequestEventData` undefined.

- [ ] **Step 3: Implement materializer**

`internal/notifications/materializer.go`:

```go
package notifications

import (
	"context"
	"log/slog"
	"sync/atomic"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

type matcherFunc func(ctx context.Context, env evt.Envelope) error

type namedMatcher struct {
	name string
	fn   matcherFunc
}

// Materializer subscribes to the events hub and turns events into
// notifications rows via registered matchers. Matcher failures are isolated:
// one matcher erroring or panicking never blocks the hub or other matchers.
type Materializer struct {
	hub       *evt.Hub
	svc       *Service
	content   ContentResolver // nil disables the content matcher (Task 10)
	matchers  []namedMatcher
	processed atomic.Int64
	unsub     func()
}

func NewMaterializer(hub *evt.Hub, svc *Service, content ContentResolver) *Materializer {
	m := &Materializer{hub: hub, svc: svc, content: content}
	m.register("request", m.matchRequest)
	m.register("send", m.matchSend)
	if content != nil {
		m.register("content", m.matchContent)
	}
	m.register("admin", m.matchAdmin)
	return m
}

func (m *Materializer) register(name string, fn matcherFunc) {
	m.matchers = append(m.matchers, namedMatcher{name: name, fn: fn})
}

// Processed reports how many envelopes have been fully processed (test hook).
func (m *Materializer) Processed() int64 { return m.processed.Load() }

func (m *Materializer) Start(ctx context.Context) error {
	ch, unsub := m.hub.Subscribe()
	m.unsub = unsub
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case env, ok := <-ch:
				if !ok {
					return
				}
				m.handle(ctx, env)
				m.processed.Add(1)
			}
		}
	}()
	return nil
}

func (m *Materializer) Stop() {
	if m.unsub != nil {
		m.unsub()
		m.unsub = nil
	}
}

func (m *Materializer) handle(ctx context.Context, env evt.Envelope) {
	for _, matcher := range m.matchers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("notifications: matcher panicked", "matcher", matcher.name, "event", env.Event, "panic", r)
				}
			}()
			if err := matcher.fn(ctx, env); err != nil {
				slog.Warn("notifications: matcher failed", "matcher", matcher.name, "event", env.Event, "error", err)
			}
		}()
	}
}
```

(`matchSend`, `matchContent`, `matchAdmin` arrive in Tasks 9–11; for this task create them as no-op stubs in their own files so the package compiles, each returning nil for non-matching channels — the stub bodies are replaced by their tasks:)

```go
// matcher_send.go    — func (m *Materializer) matchSend(ctx context.Context, env evt.Envelope) error { return nil }
// matcher_content.go — func (m *Materializer) matchContent(ctx context.Context, env evt.Envelope) error { return nil }
//                      type ContentResolver interface{} // replaced in Task 10
// matcher_admin.go   — func (m *Materializer) matchAdmin(ctx context.Context, env evt.Envelope) error { return nil }
```

`internal/notifications/matcher_request.go`:

```go
package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// RequestEventData mirrors requests.RequestEventPayload (kept separate to
// avoid an import cycle: requests must not import notifications).
type RequestEventData struct {
	RequestID string `json:"request_id"`
	UserID    int    `json:"user_id"`
	ProfileID string `json:"profile_id,omitempty"`
	Title     string `json:"title"`
	MediaType string `json:"media_type"`
	Status    string `json:"status"`
}

var requestEventTitles = map[string]string{
	"request.approved":  "Request approved",
	"request.declined":  "Request declined",
	"request.completed": "Request available",
	"request.failed":    "Request failed",
}

func (m *Materializer) matchRequest(ctx context.Context, env evt.Envelope) error {
	if env.Channel != evt.ChannelRequests {
		return nil
	}
	title, notify := requestEventTitles[env.Event]
	if !notify {
		return nil // request.submitted / request.cancelled: requester acted, no notification
	}
	var data RequestEventData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return fmt.Errorf("decode %s: %w", env.Event, err)
	}
	if data.UserID <= 0 {
		return fmt.Errorf("%s without user_id", env.Event)
	}
	status := data.Status
	if status == "" {
		status = env.Event
	}
	return m.svc.Create(ctx, CreateInput{
		UserID:      data.UserID,
		ProfileID:   data.ProfileID,
		Category:    CategoryRequest,
		Type:        env.Event,
		Title:       title,
		Body:        data.Title,
		Link:        "/requests",
		SourceEvent: env.Event,
		DedupRef:    fmt.Sprintf("%s:%s", data.RequestID, statusSuffix(env.Event)),
	})
}

func statusSuffix(event string) string {
	switch event {
	case "request.approved":
		return "approved"
	case "request.declined":
		return "declined"
	case "request.completed":
		return "completed"
	case "request.failed":
		return "failed"
	}
	return event
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/notifications/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notifications/
git commit -m "feat(notifications): materializer with isolated matchers; request matcher"
```

---

### Task 9: `notifications.send` contract matcher

**Files:**
- Modify: `internal/notifications/matcher_send.go` (replace stub)
- Test: `internal/notifications/matcher_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestSendContract_CreatesNotification(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)

	payload := map[string]any{
		"user_id": 5, "category": "request", "type": "request.fulfilled",
		"title": "Your audiobook is ready", "body": "Dune by Frank Herbert",
		"dedup_ref": "ab-req-9:fulfilled",
	}
	data, _ := json.Marshal(payload)
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelPlugins, Event: "plugin.silo.audiobook-requests.notifications.send", Data: data,
	})

	if len(store.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(store.inserted))
	}
	if store.inserted[0].SourceEvent != "plugin.silo.audiobook-requests.notifications.send" {
		t.Fatalf("source event not recorded: %+v", store.inserted[0])
	}
}

func TestSendContract_MalformedDropped(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)

	for _, raw := range []string{
		`{"category":"request","type":"t","title":"x"}`,         // no user_id
		`{"user_id":5,"category":"announcement","type":"t","title":"x"}`, // announcement not allowed from plugins
		`not json`,
	} {
		publishAndSettle(t, hub, m, evt.Envelope{
			Channel: evt.ChannelPlugins, Event: "notifications.send", Data: json.RawMessage(raw),
		})
	}
	if len(store.inserted) != 0 {
		t.Fatalf("malformed payloads were inserted: %d", len(store.inserted))
	}
}
```

Note: `publishAndSettle` from Task 8 waits on `Processed()` reaching a value; for multi-publish loops capture the count before publishing and wait for `before+1`. Adjust the helper:

```go
func publishAndSettle(t *testing.T, hub *evt.Hub, m *Materializer, env evt.Envelope) {
	t.Helper()
	before := m.Processed()
	if err := hub.Publish(context.Background(), env); err != nil {
		t.Fatalf("publish: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.Processed() <= before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/notifications/ -run TestSendContract -v
```

Expected: FAIL (stub matcher inserts nothing for the first test).

- [ ] **Step 3: Implement**

Replace `matcher_send.go` stub:

```go
package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// sendPayload is the documented notifications.send contract for plugins.
type sendPayload struct {
	UserID    int    `json:"user_id"`
	ProfileID string `json:"profile_id,omitempty"`
	Category  string `json:"category"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	Link      string `json:"link,omitempty"`
	DedupRef  string `json:"dedup_ref,omitempty"`
}

// matchSend consumes "notifications.send" events. Plugin-published events
// arrive prefixed ("plugin.<id>.notifications.send"); both forms accepted.
// Malformed payloads are dropped with a warning — plugin bugs must not error
// core paths. Plugins may not create announcements.
func (m *Materializer) matchSend(ctx context.Context, env evt.Envelope) error {
	if env.Event != "notifications.send" && !strings.HasSuffix(env.Event, ".notifications.send") {
		return nil
	}
	var p sendPayload
	if err := json.Unmarshal(env.Data, &p); err != nil {
		slog.Warn("notifications: malformed notifications.send dropped", "event", env.Event, "error", err)
		return nil
	}
	cat := Category(p.Category)
	if p.UserID <= 0 || p.Type == "" || p.Title == "" || !validCategory(cat) || cat == CategoryAnnouncement {
		slog.Warn("notifications: invalid notifications.send dropped", "event", env.Event, "user_id", p.UserID)
		return nil
	}
	return m.svc.Create(ctx, CreateInput{
		UserID: p.UserID, ProfileID: p.ProfileID, Category: cat, Type: p.Type,
		Title: p.Title, Body: p.Body, Link: p.Link,
		SourceEvent: env.Event, DedupRef: p.DedupRef,
	})
}
```

- [ ] **Step 4: Run tests, commit**

```bash
go test ./internal/notifications/ -v
git add internal/notifications/
git commit -m "feat(notifications): notifications.send plugin contract"
```

---

### Task 10: Content matcher

**Files:**
- Modify: `internal/notifications/matcher_content.go` (replace stub)
- Modify: `internal/notifications/store.go` (resolver queries)
- Test: `internal/notifications/matcher_test.go`

The content matcher consumes `catalog.item.changed` events with `change == "item_added"` semantics — but per `internal/notifications/hub.go:139-155`, per-item adds surface as `catalog.item.changed`. Scan-level `library.item_added` only carries counts, so per-item matching keys off `catalog.item.changed` (`library_id`, `content_id`).

- [ ] **Step 1: Define the resolver interface and queries**

In `matcher_content.go`:

```go
package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// ContentResolver answers "who cares about this item" from catalog state.
// Implemented by ContentResolverRepo below; faked in tests.
type ContentResolver interface {
	// ItemContext resolves the added item's display title, its series/parent
	// content id (empty when standalone), and library id.
	ItemContext(ctx context.Context, contentID string) (title string, seriesID string, libraryID int, err error)
	// InterestedProfiles returns (user_id, profile_id) pairs whose watchlist
	// or favorites contain contentID or seriesID, plus profiles with playback
	// of the series within the in-progress window, excluding child profiles
	// without their own match, restricted to users with access to libraryID.
	InterestedProfiles(ctx context.Context, contentID, seriesID string, libraryID int, inProgressSince time.Time) ([]ProfileRef, error)
}

type ProfileRef struct {
	UserID    int
	ProfileID string
}

const inProgressWindow = 21 * 24 * time.Hour

// burstGuard collapses multi-episode imports: one notification per series per
// profile per hour. In-memory is sufficient — a missed suppression after a
// restart only risks one duplicate toast, and dedup_ref still prevents
// duplicate rows for the same series+hour bucket.
func (m *Materializer) matchContent(ctx context.Context, env evt.Envelope) error {
	if env.Channel != evt.ChannelCatalog || env.Event != "catalog.item.changed" {
		return nil
	}
	var payload struct {
		LibraryID int    `json:"library_id"`
		ContentID string `json:"content_id"`
		Change    string `json:"change"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil || payload.ContentID == "" {
		return nil
	}
	if payload.Change != "item_added" {
		return nil
	}

	title, seriesID, libraryID, err := m.content.ItemContext(ctx, payload.ContentID)
	if err != nil {
		return fmt.Errorf("item context %s: %w", payload.ContentID, err)
	}
	if libraryID == 0 {
		libraryID = payload.LibraryID
	}

	refs, err := m.content.InterestedProfiles(ctx, payload.ContentID, seriesID,
		libraryID, time.Now().Add(-inProgressWindow))
	if err != nil {
		return fmt.Errorf("interested profiles: %w", err)
	}

	groupID := seriesID
	if groupID == "" {
		groupID = payload.ContentID
	}
	hourBucket := time.Now().UTC().Format("2006010215")
	for _, ref := range refs {
		if err := m.svc.Create(ctx, CreateInput{
			UserID:      ref.UserID,
			ProfileID:   ref.ProfileID,
			Category:    CategoryContent,
			Type:        "content.added",
			Title:       "New content for you",
			Body:        title,
			Link:        "/items/" + payload.ContentID,
			ItemID:      payload.ContentID,
			SourceEvent: env.Event,
			DedupRef:    fmt.Sprintf("%s:%s:%s", groupID, ref.ProfileID, hourBucket),
		}); err != nil {
			return err
		}
	}
	return nil
}
```

The hour-bucketed `dedup_ref` IS the burst guard: every episode of a season import maps to the same `(user, type, dedup_ref)` within the hour and `ON CONFLICT DO NOTHING` drops repeats. No separate in-memory state needed.

- [ ] **Step 2: Implement ContentResolverRepo in store.go**

```go
// ContentResolverRepo implements ContentResolver against catalog tables.
type ContentResolverRepo struct{ pool *pgxpool.Pool }

func NewContentResolverRepo(pool *pgxpool.Pool) *ContentResolverRepo {
	return &ContentResolverRepo{pool: pool}
}

func (r *ContentResolverRepo) ItemContext(ctx context.Context, contentID string) (string, string, int, error) {
	var title string
	var seriesID *string
	var libraryID *int
	err := r.pool.QueryRow(ctx, `
		SELECT mi.title,
		       (SELECT e.series_id FROM episodes e WHERE e.content_id = mi.content_id LIMIT 1),
		       (SELECT mil.library_id FROM media_item_libraries mil WHERE mil.content_id = mi.content_id LIMIT 1)
		FROM media_items mi WHERE mi.content_id = $1`, contentID).
		Scan(&title, &seriesID, &libraryID)
	if err != nil {
		return "", "", 0, err
	}
	sid := ""
	if seriesID != nil {
		sid = *seriesID
	}
	lid := 0
	if libraryID != nil {
		lid = *libraryID
	}
	return title, sid, lid, nil
}

func (r *ContentResolverRepo) InterestedProfiles(ctx context.Context, contentID, seriesID string, libraryID int, inProgressSince time.Time) ([]ProfileRef, error) {
	// Watchlist + favorites on the item or its series; in-progress on the series.
	rows, err := r.pool.Query(ctx, `
		WITH interested AS (
			SELECT w.user_id, w.profile_id FROM user_watchlist w
			WHERE w.media_item_id IN ($1, $2)
			UNION
			SELECT f.user_id, f.profile_id FROM user_favorites f
			WHERE f.media_item_id IN ($1, $2)
			UNION
			SELECT ph.user_id, ph.profile_id FROM playback_history ph
			JOIN episodes e ON e.content_id = ph.media_item_id
			WHERE $2 <> '' AND e.series_id = $2 AND ph.updated_at > $3
		)
		SELECT DISTINCT i.user_id, i.profile_id
		FROM interested i
		JOIN users u ON u.id = i.user_id
		WHERE u.enabled = true
		  AND (u.library_ids IS NULL OR $4 = ANY(u.library_ids))`,
		contentID, seriesID, inProgressSince, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProfileRef
	for rows.Next() {
		var ref ProfileRef
		if err := rows.Scan(&ref.UserID, &ref.ProfileID); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
```

Before compiling, verify the playback-history table/columns: `grep -n 'CREATE TABLE.*playback\|CREATE TABLE.*watch_history' migrations/sql/001_schema.sql` and adjust the in-progress CTE leg to the actual table (name and timestamp column). If no per-profile playback timestamp exists, drop the in-progress leg and note it in the commit message — watchlist/favorites are the primary signals.

- [ ] **Step 3: Write failing tests with a fake resolver**

```go
type fakeResolver struct {
	title    string
	seriesID string
	library  int
	refs     []ProfileRef
}

func (f *fakeResolver) ItemContext(context.Context, string) (string, string, int, error) {
	return f.title, f.seriesID, f.library, nil
}
func (f *fakeResolver) InterestedProfiles(context.Context, string, string, int, time.Time) ([]ProfileRef, error) {
	return f.refs, nil
}

func TestContentMatcher_NotifiesInterestedProfiles(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	resolver := &fakeResolver{title: "Dune S01E03", seriesID: "series-9", library: 2,
		refs: []ProfileRef{{UserID: 4, ProfileID: "p-a"}, {UserID: 8, ProfileID: "p-b"}}}
	m := NewMaterializer(hub, svc, resolver)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)

	data, _ := json.Marshal(map[string]any{"library_id": 2, "content_id": "ep-3", "change": "item_added"})
	publishAndSettle(t, hub, m, evt.Envelope{Channel: evt.ChannelCatalog, Event: "catalog.item.changed", Data: data})

	if len(store.inserted) != 2 {
		t.Fatalf("inserted = %d, want 2", len(store.inserted))
	}
}

func TestContentMatcher_BurstCollapsesViaDedup(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	resolver := &fakeResolver{title: "Dune", seriesID: "series-9", library: 2,
		refs: []ProfileRef{{UserID: 4, ProfileID: "p-a"}}}
	m := NewMaterializer(hub, svc, resolver)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)

	for _, ep := range []string{"ep-1", "ep-2", "ep-3"} {
		data, _ := json.Marshal(map[string]any{"library_id": 2, "content_id": ep, "change": "item_added"})
		publishAndSettle(t, hub, m, evt.Envelope{Channel: evt.ChannelCatalog, Event: "catalog.item.changed", Data: data})
	}
	if len(store.inserted) != 1 {
		t.Fatalf("season burst produced %d notifications, want 1", len(store.inserted))
	}
}

func TestContentMatcher_NonAddedChangeIgnored(t *testing.T) {
	store := &fakeStore{}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, &fakeResolver{refs: []ProfileRef{{UserID: 1, ProfileID: "p"}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)

	data, _ := json.Marshal(map[string]any{"library_id": 2, "content_id": "x", "change": "metadata_updated"})
	publishAndSettle(t, hub, m, evt.Envelope{Channel: evt.ChannelCatalog, Event: "catalog.item.changed", Data: data})
	if len(store.inserted) != 0 {
		t.Fatalf("metadata update must not notify")
	}
}
```

Check the actual `change` value the scanner emits for additions (`grep -rn 'item_added\|PublishCatalogItemChanged' internal/libraryingest/ internal/scanner/ | head`) and align the matcher's `payload.Change != "item_added"` check with reality before finishing.

- [ ] **Step 4: Run, fix, full package, commit**

```bash
go test ./internal/notifications/ -v
git add internal/notifications/
git commit -m "feat(notifications): content matcher with interest resolution and burst dedup"
```

---

### Task 11: Admin matcher

**Files:**
- Modify: `internal/notifications/matcher_admin.go` (replace stub)
- Test: `internal/notifications/matcher_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestAdminMatcher_JobFailedNotifiesAdmins(t *testing.T) {
	store := &fakeStore{admins: []int{1, 2}}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)

	data, _ := json.Marshal(map[string]any{"id": "job-1", "kind": "library_scan", "status": "failed"})
	publishAndSettle(t, hub, m, evt.Envelope{
		Channel: evt.ChannelJobs, Event: "job.failed", Data: data, AdminOnly: true,
	})

	if len(store.inserted) != 2 {
		t.Fatalf("inserted = %d, want 2 (one per admin)", len(store.inserted))
	}
	if store.inserted[0].Category != CategoryAdmin {
		t.Fatalf("category = %s, want admin", store.inserted[0].Category)
	}
}

func TestAdminMatcher_RepeatFailureThrottled(t *testing.T) {
	store := &fakeStore{admins: []int{1}}
	svc, hub := newTestService(store)
	m := NewMaterializer(hub, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)

	for i := 0; i < 3; i++ {
		data, _ := json.Marshal(map[string]any{"id": "job-1", "kind": "library_scan", "status": "failed"})
		publishAndSettle(t, hub, m, evt.Envelope{Channel: evt.ChannelJobs, Event: "job.failed", Data: data, AdminOnly: true})
	}
	if len(store.inserted) != 1 {
		t.Fatalf("repeat failures not throttled: %d rows", len(store.inserted))
	}
}
```

- [ ] **Step 2: Run to verify failure, then implement**

```bash
go test ./internal/notifications/ -run TestAdminMatcher -v
```

Replace `matcher_admin.go` stub:

```go
package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// adminAlertEvents maps failure events to notification titles.
var adminAlertEvents = map[string]string{
	"job.failed":  "Background job failed",
	"scan.failed": "Library scan failed",
	"task.failed": "Scheduled task failed",
}

// matchAdmin fans failure events out to all admin users. The hour-bucketed
// dedup_ref throttles repeats of the same source to one per hour.
func (m *Materializer) matchAdmin(ctx context.Context, env evt.Envelope) error {
	title, ok := adminAlertEvents[env.Event]
	if !ok {
		return nil
	}
	var payload struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(env.Data, &payload) // best-effort detail
	source := payload.Kind
	if source == "" {
		source = env.Event
	}

	admins, err := m.svc.store.AdminUserIDs(ctx)
	if err != nil {
		return fmt.Errorf("admin user ids: %w", err)
	}
	hourBucket := time.Now().UTC().Format("2006010215")
	body := source
	if payload.ID != "" {
		body = fmt.Sprintf("%s (%s)", source, payload.ID)
	}
	for _, uid := range admins {
		if err := m.svc.Create(ctx, CreateInput{
			UserID:      uid,
			Category:    CategoryAdmin,
			Type:        env.Event,
			Title:       title,
			Body:        body,
			Link:        "/admin/tasks",
			SourceEvent: env.Event,
			DedupRef:    fmt.Sprintf("%s:%s:%s", env.Event, source, hourBucket),
		}); err != nil {
			return err
		}
	}
	return nil
}
```

Verify real event names before finishing: `job.failed` exists (`internal/notifications/hub.go:25`); check scan/task failure event names with `grep -rn '"scan.failed"\|"task.failed"\|scans\.' internal/events internal/taskmanager --include='*.go' | head` and align `adminAlertEvents` keys (and tests) with what's actually published.

- [ ] **Step 3: Run tests, commit**

```bash
go test ./internal/notifications/ -v
git add internal/notifications/
git commit -m "feat(notifications): admin failure alerts with hourly throttle"
```

---

### Task 12: REST API handlers + router

**Files:**
- Create: `internal/api/handlers/notifications.go`
- Test: `internal/api/handlers/notifications_test.go`
- Modify: `internal/api/router.go`

- [ ] **Step 1: Write the handler**

Follow the conventions in `internal/api/handlers/favorites.go` (claims/profile extraction) and use the package's existing `writeJSON` / `writeError` helpers. Service additions used here (`List`, `UnreadCount`, `MarkRead`, `MarkAllRead`, `Dismiss`, `Preferences`, `SetPreferences`, `ListAnnouncements`) are thin pass-throughs to the store; add them to `service.go` as one-liners that apply the ChildSafe flag and clamp inputs:

```go
// service.go additions
func (s *Service) List(ctx context.Context, f ListFilter) ([]*Notification, error) {
	return s.store.List(ctx, f)
}
func (s *Service) UnreadCount(ctx context.Context, userID int, profileID string, childSafe bool) (int, error) {
	return s.store.UnreadCount(ctx, userID, profileID, childSafe)
}
func (s *Service) MarkRead(ctx context.Context, userID int, ids []int64) error {
	return s.store.MarkRead(ctx, userID, ids)
}
func (s *Service) MarkAllRead(ctx context.Context, userID int) error {
	return s.store.MarkAllRead(ctx, userID)
}
func (s *Service) Dismiss(ctx context.Context, userID int, id int64) error {
	return s.store.Dismiss(ctx, userID, id)
}
func (s *Service) GetPreferences(ctx context.Context, userID int) ([]Preference, error) {
	stored, err := s.store.Preferences(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Preference, 0, len(MutableCategories))
	for _, c := range MutableCategories {
		enabled, ok := stored[c]
		if !ok {
			enabled = c != CategoryContentDigest // digest defaults off
		}
		out = append(out, Preference{Category: c, Enabled: enabled})
	}
	return out, nil
}
func (s *Service) SetPreferences(ctx context.Context, userID int, prefs []Preference) error {
	for _, p := range prefs {
		valid := false
		for _, c := range MutableCategories {
			if p.Category == c {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("notifications: category %q is not configurable", p.Category)
		}
	}
	for _, p := range prefs {
		if err := s.store.SetPreference(ctx, userID, p.Category, p.Enabled); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) ListAnnouncements(ctx context.Context) ([]*Announcement, error) {
	return s.store.ListAnnouncements(ctx)
}
```

Handler (`internal/api/handlers/notifications.go`) — adapt the exact claims/profile helpers to what `favorites.go` uses (same accessor functions, same child-profile detection; if `favorites.go` exposes a profile struct with `IsChild`, reuse it):

```go
package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type NotificationsHandler struct {
	svc *notifications.Service
}

func NewNotificationsHandler(svc *notifications.Service) *NotificationsHandler {
	return &NotificationsHandler{svc: svc}
}

func (h *NotificationsHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	claims, profile, ok := viewerFromRequest(w, r) // same helper pattern as favorites.go
	if !ok {
		return
	}
	f := notifications.ListFilter{
		UserID:     claims.UserID,
		ProfileID:  profile.ID,
		ChildSafe:  profile.IsChild,
		UnreadOnly: r.URL.Query().Get("unread") == "1",
		Category:   notifications.Category(r.URL.Query().Get("category")),
	}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		f.Cursor, _ = strconv.ParseInt(cursor, 10, 64)
	}
	if limit := r.URL.Query().Get("limit"); limit != "" {
		f.Limit, _ = strconv.Atoi(limit)
	}
	items, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list notifications")
		return
	}
	var nextCursor *int64
	if len(items) > 0 && f.Limit > 0 && len(items) == f.Limit {
		nextCursor = &items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
}

func (h *NotificationsHandler) HandleUnreadCount(w http.ResponseWriter, r *http.Request) {
	claims, profile, ok := viewerFromRequest(w, r)
	if !ok {
		return
	}
	count, err := h.svc.UnreadCount(r.Context(), claims.UserID, profile.ID, profile.IsChild)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to count notifications")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": count})
}

func (h *NotificationsHandler) HandleMarkRead(w http.ResponseWriter, r *http.Request) {
	claims, _, ok := viewerFromRequest(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (!req.All && len(req.IDs) == 0) {
		writeError(w, http.StatusBadRequest, "bad_request", "ids or all required")
		return
	}
	var err error
	if req.All {
		err = h.svc.MarkAllRead(r.Context(), claims.UserID)
	} else {
		err = h.svc.MarkRead(r.Context(), claims.UserID, req.IDs)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to mark read")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *NotificationsHandler) HandleDismiss(w http.ResponseWriter, r *http.Request) {
	claims, _, ok := viewerFromRequest(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	switch err := h.svc.Dismiss(r.Context(), claims.UserID, id); err {
	case nil:
		w.WriteHeader(http.StatusNoContent)
	case notifications.ErrNotFound:
		writeError(w, http.StatusNotFound, "not_found", "notification not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to dismiss")
	}
}

func (h *NotificationsHandler) HandleGetPreferences(w http.ResponseWriter, r *http.Request) {
	claims, _, ok := viewerFromRequest(w, r)
	if !ok {
		return
	}
	prefs, err := h.svc.GetPreferences(r.Context(), claims.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load preferences")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preferences": prefs})
}

func (h *NotificationsHandler) HandlePutPreferences(w http.ResponseWriter, r *http.Request) {
	claims, _, ok := viewerFromRequest(w, r)
	if !ok {
		return
	}
	var req struct {
		Preferences []notifications.Preference `json:"preferences"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Preferences) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "preferences required")
		return
	}
	if err := h.svc.SetPreferences(r.Context(), claims.UserID, req.Preferences); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- admin: announcements ---

func (h *NotificationsHandler) HandleListAnnouncements(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListAnnouncements(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list announcements")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *NotificationsHandler) HandleCreateAnnouncement(w http.ResponseWriter, r *http.Request) {
	claims, _, ok := viewerFromRequest(w, r)
	if !ok {
		return
	}
	var a notifications.Announcement
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	a.CreatedBy = &claims.UserID
	if err := h.svc.PublishAnnouncement(r.Context(), &a); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (h *NotificationsHandler) HandleDeleteAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	switch err := h.svc.DeleteAnnouncement(r.Context(), id); err {
	case nil:
		w.WriteHeader(http.StatusNoContent)
	case notifications.ErrNotFound:
		writeError(w, http.StatusNotFound, "not_found", "announcement not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete announcement")
	}
}
```

`viewerFromRequest` is shorthand for whatever claims+profile extraction `favorites.go` actually uses — copy that file's exact accessor calls (`auth.ClaimsFromContext`-style + profile middleware getter) rather than inventing new ones.

- [ ] **Step 2: Mount routes in router.go**

In the authenticated user group (near the favorites routes):

```go
r.Route("/notifications", func(r chi.Router) {
	r.Get("/", notificationsHandler.HandleList)
	r.Get("/unread-count", notificationsHandler.HandleUnreadCount)
	r.Post("/read", notificationsHandler.HandleMarkRead)
	r.Post("/{id}/dismiss", notificationsHandler.HandleDismiss)
	r.Get("/preferences", notificationsHandler.HandleGetPreferences)
	r.Put("/preferences", notificationsHandler.HandlePutPreferences)
})
```

In the admin group (near the plugins admin block at `router.go:1986`):

```go
r.Route("/announcements", func(r chi.Router) {
	r.Get("/", notificationsHandler.HandleListAnnouncements)
	r.Post("/", notificationsHandler.HandleCreateAnnouncement)
	r.Delete("/{id}", notificationsHandler.HandleDeleteAnnouncement)
})
```

- [ ] **Step 3: Write handler tests**

In `notifications_test.go`, follow the existing handler-test conventions in the package (construct the handler with a Service over `fakeStore`-equivalent; the handlers package tests use `httptest`). Cover: list happy path, unread filter, mark-read validation (400 on empty), dismiss 404, prefs rejecting `announcement`, announcement create requiring exactly-one audience.

- [ ] **Step 4: Run and commit**

```bash
go test ./internal/api/handlers/ -run TestNotifications -v
go build ./...
git add internal/api/ internal/notifications/
git commit -m "feat(notifications): REST API for inbox, preferences, announcements"
```

---

### Task 13: WS channel exposure

**Files:**
- Modify: `internal/api/handlers/events_ws.go:273-288` (`allowedChannelsForRole`), snapshot switch in `snapshotForChannel`

- [ ] **Step 1: Grant the channels**

```go
func allowedChannelsForRole(role string) []evt.EventChannel {
	channels := []evt.EventChannel{
		evt.ChannelCatalog,
		evt.ChannelHistoryImport,
		evt.ChannelUserState,
		evt.ChannelNotifications,
		evt.ChannelRequests,
	}
	...
}
```

Per-user scoping needs no work: `allowsEventForClaims` (`events_ws.go:290`) already drops envelopes whose `UserID` doesn't match non-admin claims, and the service/requests publishers always stamp `UserID`.

- [ ] **Step 2: Snapshot = unread count**

In `snapshotForChannel`, add a case for `evt.ChannelNotifications` returning `{"unread_count": N}` via the notifications service (inject the service into `EventsHandler` the same way its other snapshot sources are injected — check the struct fields at the top of `events_ws.go` and mirror). `evt.ChannelRequests` returns an empty snapshot (`json.RawMessage("{}")`) — request state is fetched via REST.

- [ ] **Step 3: Test + commit**

Extend the existing tests in `events_ws_test.go` (if present — check) or add a focused test asserting `allowedChannelsForRole("user")` contains the two new channels and `allowedChannelsForRole("admin")` retains its extras.

```bash
go test ./internal/api/handlers/ -run TestEvents -v
git add internal/api/handlers/events_ws.go
git commit -m "feat(notifications): expose notifications and requests channels over WS"
```

---

### Task 14: System notifications (password change)

**Files:**
- Modify: `internal/api/handlers/admin.go` (user update path, Password handling near :488)
- Modify: whatever self-serve password-change handler exists (locate: `grep -rn 'password' internal/api/handlers/auth.go internal/api/handlers/users*.go | grep -i change`)

- [ ] **Step 1: Wire CreateSystem at password-change sites**

Add a helper to the notifications service consumers can call:

```go
// service.go
func (s *Service) CreateSystem(ctx context.Context, userID int, typ, title, body string) {
	if err := s.Create(ctx, CreateInput{
		UserID: userID, Category: CategorySystem, Type: typ, Title: title, Body: body,
	}); err != nil {
		slog.Warn("notifications: system notification failed", "type", typ, "user_id", userID, "error", err)
	}
}
```

At each site where a user's password is changed (admin update with `Password != nil`, self-serve change if present), after the change succeeds:

```go
notificationsSvc.CreateSystem(r.Context(), targetUserID,
	"system.password_changed", "Password changed",
	"Your account password was changed. If this wasn't you, contact your administrator.")
```

This requires passing the notifications service into the owning handler's constructor — follow how other cross-cutting deps reach that handler in `cmd/silo/main.go`.

New-device-login notifications are deferred to a follow-up (session issuance is in the auth login flow at `internal/api/handlers/auth.go:155`; deciding "unseen device" needs a device-fingerprint store that doesn't exist yet — out of v1 scope, noted in spec future work).

- [ ] **Step 2: Test + commit**

Extend the admin handler test fixture (see `internal/api/handlers/admin_test.go:96` which already exercises `UpdateUserInput{Password: &password}`) to assert a system notification lands in the fake notifications store after a password update.

```bash
go test ./internal/api/handlers/ -run TestAdmin -v
git add internal/api/ internal/notifications/
git commit -m "feat(notifications): system notification on password change"
```

---

### Task 15: Digest + retention tasks

**Files:**
- Create: `internal/taskmanager/tasks/notifications_digest.go`
- Create: `internal/taskmanager/tasks/notifications_retention.go`

Follow the template of `internal/taskmanager/tasks/sync_watch_providers.go:1-52` exactly (Key/Name/Description/Category/IsHidden/DefaultTriggers/Execute).

- [ ] **Step 1: Retention task**

```go
package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type NotificationPurger interface {
	PurgeOld(ctx context.Context, dismissedBefore, allBefore time.Time) (int64, error)
}

type NotificationsRetentionTask struct{ store NotificationPurger }

func NewNotificationsRetentionTask(store NotificationPurger) *NotificationsRetentionTask {
	return &NotificationsRetentionTask{store: store}
}

func (t *NotificationsRetentionTask) Key() string  { return "notifications_retention" }
func (t *NotificationsRetentionTask) Name() string { return "Purge old notifications" }
func (t *NotificationsRetentionTask) Description() string {
	return "Deletes dismissed notifications after 30 days and all notifications after 90 days"
}
func (t *NotificationsRetentionTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMaintenance // verify constant exists; use nearest match
}
func (t *NotificationsRetentionTask) IsHidden() bool { return false }
func (t *NotificationsRetentionTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 24 * 60 * 60 * 1000},
	}
}
func (t *NotificationsRetentionTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Purging old notifications")
	now := time.Now()
	purged, err := t.store.PurgeOld(ctx, now.AddDate(0, 0, -30), now.AddDate(0, 0, -90))
	if err != nil {
		return fmt.Errorf("purge notifications: %w", err)
	}
	progress.Report(100, fmt.Sprintf("Purged %d notifications", purged))
	return nil
}
```

(Check `taskmanager.TaskCategory*` constants with `grep -n 'TaskCategory' internal/taskmanager/*.go | head` and pick the maintenance-ish one that exists.)

- [ ] **Step 2: Digest task**

`notifications_digest.go` — same skeleton, `Key() "notifications_digest"`, daily interval. `Execute` calls a new service method:

```go
// service.go
// RunDailyDigest creates one digest notification per opted-in user summarizing
// catalog additions in their accessible libraries over the lookback window.
func (s *Service) RunDailyDigest(ctx context.Context, since time.Time) error {
	users, err := s.store.DigestSubscribers(ctx) // user ids with content_digest enabled=true
	if err != nil {
		return err
	}
	for _, uid := range users {
		count, err := s.store.AddedItemCountForUser(ctx, uid, since)
		if err != nil {
			slog.Warn("notifications: digest count failed", "user_id", uid, "error", err)
			continue
		}
		if count == 0 {
			continue
		}
		_ = s.Create(ctx, CreateInput{
			UserID:   uid,
			Category: CategoryContent,
			Type:     TypeContentDigest,
			Title:    "New in your libraries",
			Body:     fmt.Sprintf("%d new items were added in the last day", count),
			Link:     "/recently-added",
			DedupRef: "digest:" + time.Now().UTC().Format("20060102"),
		})
	}
	return nil
}
```

Store additions:

```go
func (r *Repository) DigestSubscribers(ctx context.Context) ([]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.user_id FROM notification_preferences p
		JOIN users u ON u.id = p.user_id AND u.enabled = true
		WHERE p.category = 'content_digest' AND p.enabled = true`)
	// ... scan ints as in AdminUserIDs
}

func (r *Repository) AddedItemCountForUser(ctx context.Context, userID int, since time.Time) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM media_items mi
		JOIN media_item_libraries mil ON mil.content_id = mi.content_id
		JOIN users u ON u.id = $1
		WHERE mi.created_at > $2
		  AND (u.library_ids IS NULL OR mil.library_id = ANY(u.library_ids))`,
		userID, since).Scan(&count)
	return count, err
}
```

(Verify `media_items.created_at` exists: `grep -n 'CREATE TABLE public.media_items' -A 25 migrations/sql/001_schema.sql | grep created` — if the column differs, use the actual added-timestamp column.)

Add both methods to the `Store` interface and `fakeStore`.

- [ ] **Step 3: Tests + commit**

Unit-test `RunDailyDigest` with the fake store (opted-in user with count>0 gets one row; count==0 gets none; second run same day dedups).

```bash
go test ./internal/notifications/ ./internal/taskmanager/tasks/ -v
git add internal/taskmanager/tasks/ internal/notifications/
git commit -m "feat(notifications): digest and retention scheduled tasks"
```

---

### Task 16: Wiring in main.go

**Files:**
- Modify: `cmd/silo/main.go`

- [ ] **Step 1: Construct and start**

Near the realtime hub setup (`cmd/silo/main.go:436` — `realtimeHub := notifications.NewHub(...)`; `eventsHub := realtimeHub.EventsHub()`):

```go
notificationsStore := notifications.NewRepository(pool)
notificationsSvc := notifications.NewService(notificationsStore, eventsHub)
notificationsMaterializer := notifications.NewMaterializer(
	eventsHub, notificationsSvc, notifications.NewContentResolverRepo(pool))
if err := notificationsMaterializer.Start(appCtx); err != nil {
	log.Fatalf("notifications materializer start: %v", err)
}
```

(`pool` = the pgx pool variable used by `mediarequests.NewRepository(deps.DB)` at main.go:1482 — match whichever of `pool`/`deps.DB` is in scope at the insertion point.)

- [ ] **Step 2: Wire into consumers**

- Requests: after both `mediarequests.NewService(...)` constructions (API service and `requestReconcileSvc` at `cmd/silo/main.go:1481`), call `svc.SetEventsHub(eventsHub)`.
- Handlers: construct `handlers.NewNotificationsHandler(notificationsSvc)` where other handlers are built and pass to the router (follow `pluginHandler`'s path into `internal/api/router.go`).
- EventsHandler: pass `notificationsSvc` for the unread-count snapshot (Task 13).
- Password-change handler: pass `notificationsSvc` (Task 14).
- Task registrations, next to the existing `taskMgr.Register(...)` block at `cmd/silo/main.go:1470-1480`:

```go
taskMgr.Register(tasks.NewNotificationsRetentionTask(notificationsStore))
taskMgr.Register(tasks.NewNotificationsDigestTask(notificationsSvc))
```

- [ ] **Step 3: Build, run, smoke-test**

```bash
go build ./...
make dev-backend &
sleep 5
curl -s http://localhost:8090/api/v1/notifications/unread-count -H "Authorization: Bearer <dev token>"
```

Expected: `{"count":0}` (use a dev login token; see `docs/` dev setup or an existing session).

- [ ] **Step 4: Commit**

```bash
git add cmd/silo/main.go
git commit -m "feat(notifications): wire store, service, materializer, tasks"
```

---

### Task 17: Full verification

- [ ] **Step 1: Full test suite + lint**

```bash
go test ./...
make lint
make verify-local-paths
```

Expected: all pass. Fix anything that fails before proceeding.

- [ ] **Step 2: End-to-end smoke against dev stack**

```bash
docker compose up -d postgres redis
make dev-backend
```

Then: create an announcement via `POST /api/v1/admin/announcements` with `{"title":"Test","body":"Hello","audience":{"all":true}}`, confirm `GET /api/v1/notifications` returns it for a non-admin user, `unread-count` is 1, `POST /notifications/read {"all":true}` zeroes it.

- [ ] **Step 3: Final commit & branch**

Work happens on a feature branch off `main` (e.g. `feat/core-notifications-server`). Squash-merge or PR per repo convention; PR body should link the spec and this plan.

---

## Self-review notes (resolved during writing)

- `internal/notifications` already exists as the realtime hub wrapper — the inbox code joins that package; no new package name.
- `item_id` is text (catalog content ids are text), not bigint as the spec sketch suggested.
- Burst guard implemented as hour-bucketed `dedup_ref` rather than in-memory state — simpler and restart-safe; spec intent (1/series/profile/hour) preserved.
- `request.submitted`/`request.cancelled` publish events (useful to admins/UIs) but do not create notifications for the requester (they performed the action).
- New-device login notifications deferred (no device-fingerprint store exists); password-change ships in v1. Spec's future-work list covers it.
- Several steps embed verify-greps (types.go field names, scan/task failure event names, playback-history table, task category constants, `created_at` column) — the implementer must run them and align code with reality rather than trusting this plan's guesses.
