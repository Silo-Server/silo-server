package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const initialPassword = "initial-fixture-password"
const initialAgent = "SiloSyntheticInitialSetup/1"

type initialSetupEnv struct {
	pool     *pgxpool.Pool
	server   *httptest.Server
	jwt      *auth.JWTService
	tables   []string
	evidence []map[string]any
}
type initialRows map[string][]map[string]any

// This constructor never invokes New/Reseed. Every case consumes an independently
// allocated virgin database, refusing all non-system relations before migration.
func newInitialSetup(t *testing.T, key string) *initialSetupEnv {
	t.Helper()
	var dsns map[string]string
	if err := json.Unmarshal([]byte(os.Getenv("SILO_INITIAL_SETUP_DATABASES")), &dsns); err != nil {
		t.Fatal("missing private owned database map")
	}
	u, err := url.Parse(dsns[key])
	if err != nil || u == nil || u.Scheme != "postgres" || u.Hostname() != "127.0.0.1" || u.Port() == "" || slices.Contains([]string{"55443", "55445", "55446"}, u.Port()) || !strings.HasPrefix(u.Path, "/silo_worker_initial_") || os.Getenv("SILO_INITIAL_SETUP_OWNED") != "1" {
		t.Fatal("initial setup requires its owned ephemeral loopback virgin database")
	}
	pool, err := pgxpool.New(t.Context(), u.String())
	if err != nil {
		t.Fatal("connect initial setup resource")
	}
	t.Cleanup(pool.Close)
	var occupied int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname <> 'information_schema'`).Scan(&occupied); err != nil {
		t.Fatal(err)
	}
	if occupied != 0 {
		t.Fatal("initial setup refuses non-virgin database before migration")
	}
	if err := database.RunMigrations(t.Context(), pool, migrations.FS, "sql"); err != nil {
		t.Fatal(err)
	}
	cfg := (&Env{t: t}).config()
	cipher, err := secret.New([]byte(masterKey))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.NewRouter(api.Dependencies{Config: cfg, AppContext: t.Context(), DB: pool, SecretCipher: cipher, ClientIPResolver: clientip.NewResolver(nil), NodeID: "fixture-node", PublicURL: publicURL, UserStoreProvider: pgstore.NewPostgresProvider(pool)}))
	t.Cleanup(server.Close)
	e := &initialSetupEnv{pool: pool, server: server, jwt: auth.NewJWTService(cfg.Auth.JWTSecret, cfg.Auth.AccessTokenExpiry, cfg.Auth.RefreshTokenExpiry)}
	rows, err := pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		e.tables = append(e.tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"users", "user_profiles", "auth_sessions"} {
		var n int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil || n != 0 {
			t.Fatal("migration/router must leave setup uninitialized", table, n, err)
		}
	}
	t.Cleanup(func() {
		if dir := os.Getenv("SILO_INITIAL_SETUP_EVIDENCE"); dir != "" {
			data, err := json.MarshalIndent(e.evidence, "", "  ")
			if err != nil {
				t.Error(err)
				return
			}
			if err := os.WriteFile(filepath.Join(dir, key+"-snapshots.json"), data, 0600); err != nil {
				t.Error(err)
			}
		}
	})
	t.Logf("virgin guard/migration complete; full inventory %d tables", len(e.tables))
	return e
}
func (e *initialSetupEnv) snapshot(t *testing.T) initialRows {
	t.Helper()
	out := initialRows{}
	observations := map[string]any{}
	for _, table := range e.tables {
		var raw []byte
		if err := e.pool.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM `+pgx.Identifier{table}.Sanitize()+` t`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var entries []map[string]any
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatal(err)
		}
		out[table] = entries
		observations[table] = map[string]any{"rows": len(entries), "sha256": fmt.Sprintf("%x", sha256.Sum256(raw))}
	}
	e.evidence = append(e.evidence, observations)
	return out
}
func initialUnchanged(t *testing.T, before, after initialRows, except ...string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatal("table inventory changed")
	}
	for table, want := range before {
		if slices.Contains(except, table) {
			continue
		}
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(after[table])
		if !bytes.Equal(a, b) {
			t.Errorf("unexpected full-row effect in %s", table)
		}
	}
}
func (e *initialSetupEnv) request(t *testing.T, method, path string, body map[string]any, token string) response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, e.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", initialAgent)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	reply, err := send(req)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}
func initialObject(t *testing.T, reply response, status int) map[string]any {
	t.Helper()
	if reply.Status != status {
		t.Fatalf("status %d want%d; body omitted", reply.Status, status)
	}
	obj, ok := reply.Doc.(map[string]any)
	if !ok {
		t.Fatal("expected JSON object")
	}
	return obj
}
func initialTimestamp(t *testing.T, row map[string]any, key string, lower, upper time.Time) {
	t.Helper()
	s, ok := row[key].(string)
	if !ok {
		t.Fatalf("missing timestamp %s", key)
	}
	stamp, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || stamp.Before(lower.Add(-time.Second)) || stamp.After(upper.Add(time.Second)) {
		t.Errorf("%s outside actual request interval", key)
	}
	delete(row, key)
}
func initialExact(t *testing.T, label string, got, want map[string]any) {
	t.Helper()
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if !bytes.Equal(a, b) {
		t.Errorf("%s exact nonsecret fields got %s want %s", label, a, b)
	}
}
func initialString(t *testing.T, obj map[string]any, key string) string {
	t.Helper()
	s, ok := obj[key].(string)
	if !ok || s == "" {
		t.Fatalf("missing string %s", key)
	}
	return s
}

func TestNewInitialSetupSequences(t *testing.T) {
	if os.Getenv("SILO_INITIAL_SETUP_REQUIRED") != "1" {
		t.Skip("requires separate NEW initial setup virgin resources")
	}
	for _, mode := range []string{"profile", "no_profile"} {
		for _, transport := range []string{"v1", "v2"} {
			t.Run(mode+"/"+transport, func(t *testing.T) {
				e := newInitialSetup(t, mode+"_"+transport)
				setupPath := "/api/" + transport + "/auth/setup"
				statusPath := setupPath
				if transport == "v2" {
					statusPath = "/api/v2/system/setup"
				}
				before := e.snapshot(t)
				status := initialObject(t, e.request(t, "GET", statusPath, nil, ""), 200)
				if status["needs_setup"] != true {
					t.Error("virgin setup status must be true")
				}
				initialUnchanged(t, before, e.snapshot(t))
				body := map[string]any{"username": "  Initial-Owner  ", "email": "  INITIAL-OWNER@SILO.EXAMPLE.TEST  ", "password": initialPassword}
				if mode == "profile" {
					body["create_default_profile"] = true
					body["default_profile_name"] = "  Owner  "
				}
				before = e.snapshot(t)
				lower := time.Now()
				reply := e.request(t, "POST", setupPath, body, "")
				upper := time.Now()
				pair := initialObject(t, reply, 201)
				if transport == "v2" && reply.Headers.Get("Cache-Control") != "no-store" {
					t.Error("credential response must be no-store")
				}
				accessToken := initialString(t, pair, "access_token")
				refreshToken := initialString(t, pair, "refresh_token")
				ac, err := e.jwt.ValidateToken(accessToken)
				if err != nil {
					t.Fatal("access signature invalid")
				}
				rc, err := e.jwt.ValidateToken(refreshToken)
				if err != nil {
					t.Fatal("refresh signature invalid")
				}
				if ac.TokenType != auth.TokenTypeAccess || rc.TokenType != auth.TokenTypeRefresh || ac.UserID != 1 || rc.UserID != 1 || ac.Role != "admin" || rc.Role != "admin" || ac.SessionID == "" || ac.SessionID != rc.SessionID || ac.ProfileID != "" || ac.ImpersonatorUserID != nil || ac.APIKeyID != 0 {
					t.Error("initial credentials must bind one ordinary admin login session")
				}
				if ac.ExpiresAt.Sub(ac.IssuedAt.Time) != e.jwt.AccessExpiry() || rc.ExpiresAt.Sub(rc.IssuedAt.Time) != e.jwt.RefreshExpiry() || pair["expires_in"] != e.jwt.AccessExpiry().Seconds() {
					t.Error("credential lifetime differs from configured expiry")
				}
				after := e.snapshot(t)
				initialUnchanged(t, before, after, "users", "user_profiles", "auth_sessions")
				e.checkCreated(t, after, mode, ac.SessionID, lower, upper)
				account, ok := pair["user"].(map[string]any)
				if !ok {
					t.Fatal("missing account")
				}
				if account["username"] != "Initial-Owner" || account["email"] != "INITIAL-OWNER@SILO.EXAMPLE.TEST" || account["role"] != "admin" || account["download_allowed"] != true {
					t.Error("wrong setup account")
				}
				if transport == "v2" && account["id"] != "1" || transport == "v1" && account["id"] != float64(1) {
					t.Error("wrong transport account ID shape")
				}
				accountID := any(float64(1))
				if transport == "v2" {
					accountID = "1"
				}
				initialExact(t, "account response", account, map[string]any{"id": accountID, "username": "Initial-Owner", "email": "INITIAL-OWNER@SILO.EXAMPLE.TEST", "role": "admin", "permissions": []any{"marker_edit", "metadata_curation"}, "download_allowed": true})
				delete(pair, "access_token")
				delete(pair, "refresh_token")
				delete(pair, "user")
				initialExact(t, "token envelope", pair, map[string]any{"expires_in": e.jwt.AccessExpiry().Seconds()})
				// Exercise issued authority against the real authenticated route; a refresh
				// credential must not act as an access credential. Neither read may mutate rows.
				before = e.snapshot(t)
				current := initialObject(t, e.request(t, "GET", "/api/v2/account/me", nil, accessToken), 200)
				initialExact(t, "current administrator", current, map[string]any{"id": "1", "username": "Initial-Owner", "email": "INITIAL-OWNER@SILO.EXAMPLE.TEST", "role": "admin", "permissions": []any{"marker_edit", "metadata_curation"}, "download_allowed": true})
				initialUnchanged(t, before, e.snapshot(t))
				before = e.snapshot(t)
				initialObject(t, e.request(t, "GET", "/api/v2/account/me", nil, refreshToken), 401)
				initialUnchanged(t, before, e.snapshot(t))
				// A different would-be administrator must be refused after completion.
				before = e.snapshot(t)
				body["username"] = "intruder"
				body["email"] = "intruder@silo.example.test"
				expected := 401
				if transport == "v2" {
					expected = 409
				}
				refusal := initialObject(t, e.request(t, "POST", setupPath, body, ""), expected)
				if transport == "v1" && refusal["error"] != "setup_complete" {
					t.Error("wrong completed setup refusal")
				}
				if transport == "v2" && !strings.HasSuffix(fmt.Sprint(refusal["type"]), "/conflict") {
					t.Error("wrong conflict Problem")
				}
				initialUnchanged(t, before, e.snapshot(t))
				before = e.snapshot(t)
				status = initialObject(t, e.request(t, "GET", statusPath, nil, ""), 200)
				if status["needs_setup"] != false {
					t.Error("completed setup status must be false")
				}
				initialUnchanged(t, before, e.snapshot(t))
				t.Logf("NEW setup sequence: 6HTTP/12full snapshots/%dtables; setup201 then%d, minted admin/session cryptographically bound", len(e.tables), expected)
			})
		}
	}
}

func (e *initialSetupEnv) checkCreated(t *testing.T, all initialRows, mode, sessionID string, lower, upper time.Time) {
	t.Helper()
	if len(all["users"]) != 1 || len(all["auth_sessions"]) != 1 {
		t.Fatal("setup must create exactly one account/session")
	}
	user := all["users"][0]
	hash := initialString(t, user, "password_hash")
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(initialPassword)) != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte("incorrect")) == nil {
		t.Error("stored password does not verify exactly")
	}
	delete(user, "password_hash")
	initialTimestamp(t, user, "created_at", lower, upper)
	initialTimestamp(t, user, "updated_at", lower, upper)
	initialExact(t, "user", user, map[string]any{"id": float64(1), "email": "INITIAL-OWNER@SILO.EXAMPLE.TEST", "username": "Initial-Owner", "local_password_login_enabled": true, "role": "admin", "permissions": []any{}, "enabled": true, "library_ids": nil, "max_playback_quality": nil, "max_streams": nil, "max_transcodes": nil, "transcode_allowed": nil, "audio_transcode_allowed": nil, "download_allowed": nil, "download_transcode_allowed": nil, "requests_allowed": nil, "max_profiles": float64(5), "access_group_id": nil, "access_policy_revision": float64(1), "admin_revision": float64(1)})
	session := all["auth_sessions"][0]
	initialTimestamp(t, session, "created_at", lower, upper)
	initialTimestamp(t, session, "expires_at", lower.Add(e.jwt.RefreshExpiry()), upper.Add(e.jwt.RefreshExpiry()))
	initialExact(t, "session", session, map[string]any{"id": sessionID, "user_id": float64(1), "device_name": initialAgent, "ip_address": "127.0.0.1", "revoked_at": nil, "impersonator_user_id": nil, "impersonation_started_at": nil})
	wantCount := 0
	if mode == "profile" {
		wantCount = 1
	}
	if len(all["user_profiles"]) != wantCount {
		t.Fatal("unexpected default profile count")
	}
	if wantCount == 0 {
		return
	}
	p := all["user_profiles"][0]
	if _, err := uuid.Parse(initialString(t, p, "id")); err != nil {
		t.Error("invalid profile ID")
	}
	delete(p, "id")
	initialTimestamp(t, p, "created_at", lower, upper)
	initialTimestamp(t, p, "updated_at", lower, upper)
	initialExact(t, "profile", p, map[string]any{"user_id": float64(1), "name": "Owner", "avatar": "", "pin_hash": "", "is_child": false, "is_primary": true, "max_content_rating": "", "quality_preference": "", "language": "", "preferred_metadata_language": "", "subtitle_language": "", "subtitle_mode": "", "auto_skip_intro": false, "auto_skip_credits": false, "auto_skip_recap": false, "auto_play_next_preview": false, "library_restrictions_enabled": false, "show_forced_subtitles": true, "max_playback_quality": "", "remove_watched_from_watchlist": true})
}

// TestNewInitialSetupCompetingBoundary parks two distinct public callers on the
// production first-setup admission lock (auth.InitialSetupAdvisoryLock), which
// every setup transaction takes before its in-transaction emptiness recount.
// Holding that key at session level from the test makes both callers wait as
// two ungranted advisory waiters with zero rows written; releasing it lets the
// database serialize them. Exactly one caller may create the administrator; the
// other must recount under the lock and receive the completed-setup refusal.
func TestNewInitialSetupCompetingBoundary(t *testing.T) {
	if os.Getenv("SILO_INITIAL_SETUP_REQUIRED") != "1" {
		t.Skip("requires separate NEW initial setup virgin resources")
	}
	for _, transport := range []string{"v1", "v2"} {
		t.Run(transport, func(t *testing.T) {
			e := newInitialSetup(t, "competing_"+transport)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			lockID := auth.InitialSetupAdvisoryLock
			// pg_locks exposes a bigint advisory key as classid (high 32 bits)
			// and objid (low 32 bits) with objsubid 1.
			classID := int32(uint64(lockID) >> 32)      //nolint:gosec // exact bit split of the production key
			objID := int32(uint64(lockID) & 0xffffffff) //nolint:gosec // exact bit split of the production key
			held, err := e.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			if _, err := held.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
				t.Fatal(err)
			}
			released := false
			release := func() {
				if released {
					return
				}
				released = true
				if _, err := held.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockID); err != nil {
					t.Error("release admission barrier", err)
				}
			}
			defer release()
			before := e.snapshot(t)
			replies := make(chan int, 2)
			for i := range 2 {
				go func() {
					body, _ := json.Marshal(map[string]any{"username": "contender" + strconv.Itoa(i), "email": "contender" + strconv.Itoa(i) + "@silo.example.test", "password": initialPassword})
					req, _ := http.NewRequestWithContext(ctx, "POST", e.server.URL+"/api/"+transport+"/auth/setup", bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					reply, err := send(req)
					if err != nil {
						replies <- 0
						return
					}
					replies <- reply.Status
				}()
			}
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiters int
				if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND classid=$1 AND objid=$2 AND objsubid=1 AND NOT granted`, classID, objID).Scan(&waiters); err != nil {
					t.Fatal(err)
				}
				if waiters == 2 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("two requests did not park on the setup admission lock")
				case <-ticker.C:
				}
			}
			// Both callers wait for admission; neither has passed the recount.
			initialUnchanged(t, before, e.snapshot(t))
			var parked int
			if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users)+(SELECT count(*) FROM user_profiles)+(SELECT count(*) FROM auth_sessions)`).Scan(&parked); err != nil {
				t.Fatal(err)
			}
			if parked != 0 {
				t.Fatalf("%d rows written while both callers were parked before admission", parked)
			}
			release()
			codes := []int{<-replies, <-replies}
			slices.Sort(codes)
			expected := []int{201, 401}
			if transport == "v2" {
				expected = []int{201, 409}
			}
			var admins, sessions int
			if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users WHERE role='admin'),(SELECT count(*) FROM auth_sessions)`).Scan(&admins, &sessions); err != nil {
				t.Fatal(err)
			}
			initialUnchanged(t, before, e.snapshot(t), "users", "auth_sessions")
			t.Logf("serialized admission boundary: statuses=%v admins=%d sessions=%d", codes, admins, sessions)
			if !slices.Equal(codes, expected) || admins != 1 || sessions != 1 {
				t.Errorf("first-administrator boundary violated: want%v/oneadmin/onesession", expected)
			}
		})
	}
}
