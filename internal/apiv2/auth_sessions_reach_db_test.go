package apiv2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// reachDB drives listSessions and deleteSession through the router against
// sessions stored in Postgres, each caller authenticated as one stored
// session.
type reachDB struct {
	pool    *pgxpool.Pool
	suffix  string
	users   map[int]*models.User
	claims  map[string]*auth.Claims
	handler http.Handler
}

func newReachDB(t *testing.T) *reachDB {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	d := &reachDB{pool: pool, suffix: strconv.FormatInt(time.Now().UnixNano(), 10), users: map[int]*models.User{}, claims: map[string]*auth.Claims{}}
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		_, _ = pool.Exec(ctx, `DELETE FROM auth_sessions WHERE user_id IN (SELECT id FROM users WHERE username LIKE $1)`, "reach-"+d.suffix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1`, "reach-"+d.suffix+"%")
	})
	users, sessions := auth.NewUserRepository(pool), auth.NewSessionRepository(pool)
	jwt := auth.NewJWTService("synthetic-session-reach-secret", time.Minute, time.Hour)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil)
	deps := parityDeps(false)
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{d.claims}, sessions, fakeAPIKeys{}, fakeUsers{d.users})
	deps.Sessions = handlers.NewAuthHandler(svc, jwt, nil)
	d.handler = newTestHandler(t, deps)
	return d
}

func (d *reachDB) account(t *testing.T) int {
	t.Helper()
	name := fmt.Sprintf("reach-%s-%d", d.suffix, len(d.users))
	var id int
	if err := d.pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'synthetic-hash', 'user', true) RETURNING id`, name, name+"@example.test").Scan(&id); err != nil {
		t.Fatal(err)
	}
	d.users[id] = &models.User{ID: id, Role: models.RoleUser, Enabled: true}
	return id
}

// reachSession is one stored session; via is nil for a session stored
// without a value, as before the column existed, and opened is empty for one
// with no record of how it was opened, as a server of the previous release
// stores it.
type reachSession struct {
	id, platform string
	opened       string
	via          *bool
	createdAt    time.Time
}

// store inserts the session with every member LoginSession shows set, and
// returns a bearer token that authenticates as it.
func (d *reachDB) store(t *testing.T, userID int, s reachSession) string {
	t.Helper()
	if s.id == "" {
		s.id = uuid.NewString()
	}
	if s.createdAt.IsZero() {
		s.createdAt = time.Now().Add(-time.Hour)
	}
	if _, err := d.pool.Exec(t.Context(), `INSERT INTO auth_sessions
		(id, user_id, device_name, ip_address, device_id, device_platform, via_device_sign_in, created_at, expires_at, last_seen_at, opened_via)
		VALUES ($1, $2, 'Device '||$1, '192.0.2.10', 'device-'||$1, NULLIF($3, ''), $4, $5, NOW() + INTERVAL '1 day', NOW(), NULLIF($6, ''))`,
		s.id, userID, s.platform, s.via, s.createdAt, s.opened); err != nil {
		t.Fatal(err)
	}
	token := "reach-token-" + s.id
	d.claims[token] = &auth.Claims{UserID: userID, Role: models.RoleUser, SessionID: s.id, TokenType: auth.TokenTypeAccess}
	return token
}

func (d *reachDB) live(t *testing.T, id string) bool {
	t.Helper()
	var live bool
	if err := d.pool.QueryRow(t.Context(), `SELECT revoked_at IS NULL FROM auth_sessions WHERE id = $1`, id).Scan(&live); err != nil {
		t.Fatal(err)
	}
	return live
}

// reachPage is a listSessions page with every member kept as raw JSON, so
// an absent member is told from an empty one.
type reachPage struct {
	Items          []map[string]json.RawMessage `json:"items"`
	CurrentSession map[string]json.RawMessage   `json:"current_session"`
	Page           struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
}

func (d *reachDB) list(t *testing.T, token, query string) reachPage {
	t.Helper()
	rec := do(t, d.handler, http.MethodGet, Prefix+"/auth/sessions"+query, "", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var page reachPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func itemByID(t *testing.T, page reachPage, id string) map[string]json.RawMessage {
	t.Helper()
	for _, item := range page.Items {
		if string(item["id"]) == `"`+id+`"` {
			return item
		}
	}
	t.Fatalf("session %s not listed", id)
	return nil
}

// requireCompleteItem checks an item shows its stored session in full.
func requireCompleteItem(t *testing.T, label string, item map[string]json.RawMessage, id string, current bool) {
	t.Helper()
	want := map[string]string{"device_name": `"Device ` + id + `"`, "ip_address": `"192.0.2.10"`, "device_id": `"device-` + id + `"`, "current": strconv.FormatBool(current)}
	for member, value := range want {
		if string(item[member]) != value {
			t.Fatalf("%s %s = %s, want %s", label, member, item[member], value)
		}
	}
	if string(item["last_seen_at"]) == "null" || len(item["created_at"]) == 0 || len(item["expires_at"]) == 0 {
		t.Fatalf("%s is not complete: %v", label, item)
	}
}

// TestSessionReachRuleDB is the rule's table, through both routes: for each
// kind of caller session, what another session's item shows and whether
// ending another session is allowed. Regression: a session a device signed
// in with by code read every session of the account and could end any. The
// rows without a value pin each TV spelling the apps send (tvOS, android-tv,
// roku), each in another case, against one platform that isn't a TV's and
// none.
func TestSessionReachRuleDB(t *testing.T) {
	d := newReachDB(t)
	yes, no := true, false
	for _, tc := range []struct {
		label    string
		opened   string
		via      *bool
		platform string
		limited  bool
	}{
		// A recorded opened_via decides, with or without the flag.
		{"recorded device sign-in, phone", models.SessionOpenedDeviceLogin, &yes, "iOS", true},
		{"recorded device sign-in, no flag", models.SessionOpenedDeviceLogin, nil, "", true},
		{"recorded password sign-in, TV", models.SessionOpenedPassword, &no, "tvOS", false},
		{"recorded password sign-in, no flag, TV", models.SessionOpenedPassword, nil, "tvOS", false},
		// The flag alone, as a server of the previous release writes it.
		{"device sign-in, TV", "", &yes, "tvOS", true},
		{"device sign-in, phone", "", &yes, "iOS", true},
		{"device sign-in, no platform", "", &yes, "", true},
		{"password sign-in, TV", "", &no, "tvOS", false},
		// Neither: a session older than both columns.
		{"before the column, tvOS", "", nil, "TVOS", true},
		{"before the column, Android TV", "", nil, "Android-TV", true},
		{"before the column, Roku", "", nil, "ROKU", true},
		{"before the column, phone", "", nil, "iOS", false},
		{"before the column, no platform", "", nil, "", false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			account := d.account(t)
			caller := reachSession{id: uuid.NewString(), opened: tc.opened, via: tc.via, platform: tc.platform, createdAt: time.Now().Add(-2 * time.Hour)}
			token := d.store(t, account, caller)
			other := uuid.NewString()
			d.store(t, account, reachSession{id: other, platform: "web"})

			page := d.list(t, token, "")
			if len(page.Items) != 2 {
				t.Fatalf("items = %d, want both sessions", len(page.Items))
			}
			requireCompleteItem(t, "own item", itemByID(t, page, caller.id), caller.id, true)
			requireCompleteItem(t, "current_session", page.CurrentSession, caller.id, true)
			item := itemByID(t, page, other)
			if tc.limited {
				requireRedactedItem(t, item, other, page.CurrentSession)
			} else {
				requireCompleteItem(t, "other item", item, other, false)
			}

			rec := do(t, d.handler, http.MethodDelete, Prefix+"/auth/sessions/"+other, "", bearer(token))
			if tc.limited {
				p := requireProblem(t, rec, TypePermissionDenied)
				if !strings.Contains(p.Detail, "signed in with a code can end only its own session") {
					t.Fatalf("detail = %q", p.Detail)
				}
				if !d.live(t, other) {
					t.Fatal("a refused delete ended the other session")
				}
			} else if rec.Code != http.StatusNoContent || d.live(t, other) {
				t.Fatalf("ordinary delete = %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// requireRedactedItem checks every member of another session's item as a
// session limited to itself sees it, and that the item has no member the
// redaction did not decide.
func requireRedactedItem(t *testing.T, item map[string]json.RawMessage, id string, own map[string]json.RawMessage) {
	t.Helper()
	want := map[string]string{
		"id":           `"` + id + `"`,
		"device_name":  `""`,
		"ip_address":   `""`,
		"last_seen_at": "null",
		"current":      "false",
		// Null like every session no app key opened, so it says nothing.
		"device_key_id": "null",
		"created_at":    string(own["created_at"]),
		"expires_at":    string(own["expires_at"]),
	}
	for member, value := range item {
		if want[member] != string(value) {
			t.Fatalf("redacted item %s = %s, want %q (device_id and device_platform are absent)", member, value, want[member])
		}
	}
	if len(item) != len(want) {
		t.Fatalf("redacted item members = %v, want exactly %v", item, want)
	}
}

// TestLimitedSessionDeleteDB: a limited session ends itself; another
// account's session and an unknown id answer exactly as they do for an
// ordinary caller, so the 403 says nothing about sessions outside the
// account.
func TestLimitedSessionDeleteDB(t *testing.T) {
	d := newReachDB(t)
	yes := true
	account, stranger := d.account(t), d.account(t)
	limited := d.store(t, account, reachSession{via: &yes, platform: "tvOS"})
	ordinary := d.store(t, account, reachSession{platform: "web"})
	foreign := uuid.NewString()
	d.store(t, stranger, reachSession{id: foreign, platform: "web"})
	for _, id := range []string{foreign, uuid.NewString()} {
		fromLimited := do(t, d.handler, http.MethodDelete, Prefix+"/auth/sessions/"+id, "", bearer(limited))
		fromOrdinary := do(t, d.handler, http.MethodDelete, Prefix+"/auth/sessions/"+id, "", bearer(ordinary))
		limitedProblem, ordinaryProblem := requireProblem(t, fromLimited, TypeNotFound), requireProblem(t, fromOrdinary, TypeNotFound)
		limitedProblem.Instance, ordinaryProblem.Instance = "", ""
		if fmt.Sprint(limitedProblem) != fmt.Sprint(ordinaryProblem) {
			t.Fatalf("limited %s, ordinary %s", fromLimited.Body.String(), fromOrdinary.Body.String())
		}
	}
	if !d.live(t, foreign) {
		t.Fatal("another account's session ended")
	}
	own := d.claims[limited].SessionID
	if rec := do(t, d.handler, http.MethodDelete, Prefix+"/auth/sessions/"+own, "", bearer(limited)); rec.Code != http.StatusNoContent || d.live(t, own) {
		t.Fatalf("ending its own session = %d %s", rec.Code, rec.Body.String())
	}
}

// TestLimitedSessionPagesByIDDB pages a limited caller one session at a
// time through sessions whose id order differs from their creation order:
// every id comes once, in id order, and each cursor decodes to an id only.
// Regression: the newest-first order and the created_at in the cursor would
// tell the caller which of the other sessions are newer.
func TestLimitedSessionPagesByIDDB(t *testing.T) {
	d := newReachDB(t)
	yes := true
	account := d.account(t)
	base := time.Now().Add(-time.Hour)
	// Four ids in ascending order, created in the order 3, 1, 4, 2.
	ids := slices.Sorted(slices.Values([]string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}))
	for i, minutes := range []int{2, 4, 1, 3} {
		d.store(t, account, reachSession{id: ids[i], platform: "web", createdAt: base.Add(time.Duration(minutes) * time.Minute)})
	}
	token := d.store(t, account, reachSession{via: &yes, createdAt: base})
	want := slices.Sorted(slices.Values(append([]string{d.claims[token].SessionID}, ids...)))

	var got []string
	cursor := ""
	for range want {
		query := "?limit=1"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		page := d.list(t, token, query)
		if len(page.Items) != 1 {
			t.Fatalf("page = %d items", len(page.Items))
		}
		var id string
		_ = json.Unmarshal(page.Items[0]["id"], &id)
		got = append(got, id)
		cursor = page.Page.NextCursor
		if cursor != "" {
			requireIDOnlyCursor(t, cursor, id)
		}
	}
	if cursor != "" || !slices.Equal(got, want) {
		t.Fatalf("paged %v (last cursor %q), want %v", got, cursor, want)
	}

	// A cursor minted for an ordinary caller of the same account is refused.
	ordinary := d.store(t, account, reachSession{platform: "web"})
	ordinaryCursor := d.list(t, ordinary, "?limit=1").Page.NextCursor
	requireProblem(t, do(t, d.handler, http.MethodGet, Prefix+"/auth/sessions?limit=1&cursor="+url.QueryEscape(ordinaryCursor), "", bearer(token)), TypeInvalidCursor)
	limitedCursor := d.list(t, token, "?limit=1").Page.NextCursor
	requireProblem(t, do(t, d.handler, http.MethodGet, Prefix+"/auth/sessions?limit=1&cursor="+url.QueryEscape(limitedCursor), "", bearer(ordinary)), TypeInvalidCursor)
}

// requireIDOnlyCursor decodes a cursor's signed body: its position is the
// last emitted id and nothing else.
func requireIDOnlyCursor(t *testing.T, cursor, lastID string) {
	t.Helper()
	body64, _, _ := strings.Cut(cursor, ".")
	body, err := base64.RawURLEncoding.DecodeString(body64)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Pos map[string]any `json:"p"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Pos) != 1 || envelope.Pos["i"] != lastID {
		t.Fatalf("cursor position = %s, want only the id %s", body, lastID)
	}
}
