package executor

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

// Frozen registration-1 originals, copied from the catalog checkpoint before any
// v2 declaration existed. The selector refuses a changed original.
//
//go:embed testdata/device_poll_r1_originals.json
var devicePollR1Originals []byte

var devicePollR1IDs = []string{
	"device_poll.pending.r1", "device_poll.approved.r1", "device_poll.consumed.r1",
	"device_poll.remote_approved.r1", "device_poll.denied.r1", "device_poll.expired.r1",
	"device_poll.unknown.r1", "device_poll.missing.r1", "device_poll.rate_limited.r1",
}

const (
	devicePollR1Route     = "/api/v1/auth/device/poll"
	devicePollR1Operation = "pollDeviceLogin"
	devicePollR1Burst     = 30
	devicePollR1PerMinute = 120
)

type devicePollR1Case struct {
	file     string
	row      scenariocatalog.Row
	scenario scenariocatalog.Scenario
}

// devicePollR1Status is the accepted v2 status for each original.
func devicePollR1Status(id string) int {
	switch {
	case strings.Contains(id, ".unknown."):
		return 404
	case strings.Contains(id, ".missing."):
		return 422
	case strings.Contains(id, ".rate_limited."):
		return 429
	}
	return 200
}

func selectDevicePollR1(catalogs []*scenariocatalog.Catalog) ([]devicePollR1Case, error) {
	var originals []scenariocatalog.Scenario
	if err := json.Unmarshal(devicePollR1Originals, &originals); err != nil {
		return nil, err
	}
	if len(originals) != len(devicePollR1IDs) {
		return nil, fmt.Errorf("original count changed")
	}
	var selected []devicePollR1Case
	for _, id := range devicePollR1IDs {
		var found *devicePollR1Case
		for _, c := range catalogs {
			for _, row := range c.Rows {
				for _, s := range row.Scenarios {
					if s.ID != id {
						continue
					}
					if found != nil {
						return nil, fmt.Errorf("duplicate %s", id)
					}
					if row.Listener != "api" || row.Method != "POST" || row.Path != devicePollR1Route || row.RegistrationIndex != 1 {
						return nil, fmt.Errorf("wrong registration or route %s", id)
					}
					if s.V2Expectation == nil {
						return nil, fmt.Errorf("missing pair %s", id)
					}
					// The contextual validator keeps the default 16-exchange budget for
					// every case except the exact frozen 31-request burst.
					if err := scenariocatalog.ValidateScenarioPairing(row, s); err != nil {
						return nil, fmt.Errorf("%s: %w", id, err)
					}
					pair := s.V2Expectation
					if pair.OperationID != devicePollR1Operation || pair.Method != "POST" || pair.Request.Path != "/api/v2/auth/device/poll" || pair.Principal != nil || len(pair.Then) != 0 || len(s.Then) != 0 {
						return nil, fmt.Errorf("invalid poll pair %s", id)
					}
					original := s
					original.V2Expectation = nil
					got, _ := json.Marshal(original)
					var want []byte
					for _, o := range originals {
						if o.ID == id {
							want, _ = json.Marshal(o)
						}
					}
					if !bytes.Equal(got, want) {
						return nil, fmt.Errorf("original oracle/intent changed %s", id)
					}
					request := pair.Request
					request.Path = s.Request.Path
					requestJSON, _ := json.Marshal(request)
					originalRequestJSON, _ := json.Marshal(s.Request)
					if !bytes.Equal(requestJSON, originalRequestJSON) {
						return nil, fmt.Errorf("request intent changed %s", id)
					}
					if pair.Expect.Status != devicePollR1Status(id) {
						return nil, fmt.Errorf("wrong expected status %s", id)
					}
					found = &devicePollR1Case{c.File, row, s}
				}
			}
		}
		if found == nil {
			return nil, fmt.Errorf("missing %s", id)
		}
		selected = append(selected, *found)
	}
	return selected, nil
}

// Reject wrong resources and unknown keys before New can migrate or reseed.
func guardDevicePollR1(t *testing.T) {
	t.Helper()
	u, err := url.Parse(os.Getenv(DatabaseEnv))
	if err != nil || u == nil || u.Scheme != "postgres" || u.Hostname() != "127.0.0.1" || u.Port() == "" || slices.Contains([]string{"55443", "55445", "55446"}, u.Port()) || !strings.HasPrefix(u.Path, "/silo_catalog_device_poll_") || os.Getenv("SILO_CATALOG_DEVICE_POLL_OWNED") != "1" {
		t.Fatal("device poll r1 requires its explicitly owned loopback scratch database")
	}
	pool, err := pgxpool.New(t.Context(), u.String())
	if err != nil {
		t.Fatal("connect owned device poll resource")
	}
	defer pool.Close()
	probe := &Env{t: t, ctx: t.Context(), pool: pool}
	probe.guardScratchDatabase()
	pool.Close()
	guardFrozenAPIKeyFixture(t)
}

// One statement per snapshot: every row of every effect table, ordered by content.
func devicePollR1SnapshotQuery() string {
	parts := make([]string, 0, len(signupEffectTables))
	for _, table := range signupEffectTables {
		parts = append(parts, fmt.Sprintf("'%s',(SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM %s t)", table, table))
	}
	return "SELECT jsonb_build_object(" + strings.Join(parts, ",") + ")"
}

func TestRequiredDevicePollR1Acceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("requires owned device poll r1 resource")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectDevicePollR1(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	guardDevicePollR1(t)
	e := New(t)
	defer func() { guardDevicePollR1(t); e.Reseed(); e.guardScratchDatabase() }()
	if e.jwt.RefreshExpiry() <= 24*time.Hour {
		t.Fatal("configured refresh lifetime must exceed the temporary session cap for this evidence")
	}
	byID := func(id string) devicePollR1Case {
		t.Helper()
		index := slices.IndexFunc(selected, func(c devicePollR1Case) bool { return c.scenario.ID == id })
		if index < 0 {
			t.Fatal("missing selected case", id)
		}
		return selected[index]
	}
	var results []Result
	requests, snapshots := 0, 0
	query := devicePollR1SnapshotQuery()
	snapshot := func(t *testing.T) map[string]json.RawMessage {
		t.Helper()
		snapshots++
		var raw []byte
		if err := e.pool.QueryRow(e.ctx, query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if len(out) != len(signupEffectTables) {
			t.Fatal("snapshot table inventory changed")
		}
		return out
	}
	dbNow := func(t *testing.T) time.Time {
		t.Helper()
		var now time.Time
		if err := e.pool.QueryRow(e.ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
			t.Fatal(err)
		}
		return now
	}
	for _, item := range selected {
		s := item.scenario
		for _, transport := range []string{"v1", "v2"} {
			t.Run(s.ID+"/"+transport, func(t *testing.T) {
				guardDevicePollR1(t)
				e.Reseed()
				defer e.Reseed()
				if !e.rowRateLimited(item.row) {
					t.Fatal("original registration must be rate limited")
				}
				e.resetRateLimits()
				defer e.resetRateLimits()
				cfg, err := ratelimit.LoadConfig(e.ctx, e.settings)
				if err != nil {
					t.Fatal(err)
				}
				if ep := cfg.AuthEndpoints["device_poll"]; !cfg.Enabled || ep.Burst != devicePollR1Burst || ep.RequestsPerMinute != devicePollR1PerMinute {
					t.Fatal("original device_poll rate configuration changed")
				}
				result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: item.file, Row: item.row.Key().String()}
				defer func() {
					if t.Failed() && len(result.Failures) == 0 {
						result.Failures = append(result.Failures, "assertion failed; see log")
					}
					results = append(results, result)
				}()
				expectOf := func(c devicePollR1Case) scenariocatalog.Expect {
					if transport == "v2" {
						return c.scenario.V2Expectation.Expect
					}
					return c.scenario.Expect
				}
				request := s.Request
				if transport == "v2" {
					request = s.V2Expectation.Request
					result.OperationID = s.V2Expectation.OperationID
				}
				count := max(request.Repeat, 1)
				request.Repeat = 1
				rateCase := s.ID == "device_poll.rate_limited.r1"
				consumedCase := s.ID == "device_poll.consumed.r1"
				burstStart := time.Time{}
				for i := range count {
					// The frozen oracle names the final request. Earlier requests carry the
					// original oracle of the state they must reach: the 30 admitted burst
					// requests are unknown-code refusals and the first consumed poll is the
					// approved collection. Both are executed and observed here, never assumed.
					stepExpect := expectOf(item)
					switch {
					case rateCase && i < count-1:
						stepExpect = expectOf(byID("device_poll.unknown.r1"))
					case consumedCase && i == 0:
						stepExpect = expectOf(byID("device_poll.approved.r1"))
					}
					before := snapshot(t)
					dbStart, appStart := dbNow(t), time.Now()
					if i == 0 {
						burstStart = appStart
					}
					req, err := e.buildRequest(e.liveLimited.URL, "POST", request, s.Principal)
					if err != nil {
						t.Fatal("construct poll request")
					}
					requests++
					resp, err := send(req)
					if err != nil {
						t.Fatal("poll transport failed")
					}
					appEnd, dbEnd := time.Now(), dbNow(t)
					after := snapshot(t)
					resolved, err := e.substituteExpect(stepExpect)
					if err != nil {
						t.Fatal(err)
					}
					if failures := check(resolved, resp); len(failures) > 0 {
						result.Failures = append(result.Failures, fmt.Sprintf("request %d oracle failed; body withheld", i+1))
						t.Errorf("request %d status %d oracle failed (body withheld): %d assertions", i+1, resp.Status, len(failures))
					}
					collects := s.ID == "device_poll.approved.r1" || s.ID == "device_poll.remote_approved.r1" || (consumedCase && i == 0)
					if collects {
						assertDevicePollCollection(t, e, s.ID, transport, resp, before, after, dbStart, dbEnd, appStart, appEnd)
					}
					for table, want := range before {
						if collects && (table == "auth_sessions" || table == "device_login_requests") {
							continue
						}
						if !bytes.Equal(want, after[table]) {
							t.Errorf("%s changed on request %d", table, i+1)
						}
					}
					if rateCase && i == count-1 {
						assertDevicePollBurst(t, transport, resp, burstStart, appStart, appEnd)
					}
				}
			})
		}
	}
	if err := requiredPairedResults(results, devicePollR1IDs); err != nil {
		t.Error(err)
	}
	if requests != 80 || snapshots != 160 {
		t.Errorf("got %d requests/%d snapshots, want80/160", requests, snapshots)
	}
	t.Logf("device poll r1: %d results/%d HTTP/%d snapshots/%d full-table observations", len(results), requests, snapshots, snapshots*len(signupEffectTables))
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// The 31st request must be refused by the real per-second bucket. The bucket
// refills one token every 500ms, so a burst that outlives that interval could
// admit request 31 without any threshold change; report the cause explicitly.
func assertDevicePollBurst(t *testing.T, transport string, resp response, burstStart, appStart, appEnd time.Time) {
	t.Helper()
	refill := time.Minute / devicePollR1PerMinute
	if appEnd.Sub(burstStart) >= refill {
		t.Errorf("burst took %s, crossing the real %s refill interval", appEnd.Sub(burstStart), refill)
	}
	retry, err := strconv.Atoi(resp.Headers.Get("Retry-After"))
	if err != nil || retry < 1 || retry > 1+int(refill.Seconds()) {
		t.Error("retry header outside real refill bound")
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Raw, &body); err != nil {
		t.Fatal("rate sequence JSON invalid")
	}
	if transport == "v1" {
		if body["retry_after"] != float64(retry) {
			t.Error("retry body/header mismatch")
		}
		reset, err := strconv.ParseInt(resp.Headers.Get("X-RateLimit-Reset"), 10, 64)
		if err != nil || reset < appStart.Unix() || reset > appEnd.Add(time.Second).Unix() {
			t.Error("reset outside limiter clock bounds")
		}
		return
	}
	for _, legacy := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if resp.Headers.Get(legacy) != "" {
			t.Error("v2 problem carried a legacy rate-limit header")
		}
	}
}

// assertDevicePollCollection verifies the one poll that collects an approved
// request: exactly one new login session, exactly one request transition to
// consumed bound to that session, credentials bound to both, and the temporary
// profile authority when the approval was for remote playback.
func assertDevicePollCollection(t *testing.T, e *Env, id, transport string, resp response, before, after map[string]json.RawMessage, dbStart, dbEnd, appStart, appEnd time.Time) {
	t.Helper()
	type row = map[string]json.RawMessage
	rows := func(raw json.RawMessage) []row {
		t.Helper()
		var r []row
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	text := func(raw json.RawMessage) string {
		t.Helper()
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	encode := func(v any) json.RawMessage {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	equal := func(label string, want, got row) {
		t.Helper()
		if len(want) != len(got) {
			t.Errorf("%s column count changed", label)
		}
		for k, v := range want {
			if !bytes.Equal(v, got[k]) {
				t.Errorf("unexpected %s column effect: %s", label, k)
			}
		}
	}
	stamp := func(raw json.RawMessage) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339Nano, text(raw))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	bounds := func(label string, raw json.RawMessage, lo, hi time.Time) {
		t.Helper()
		if v := stamp(raw); v.Before(lo) || v.After(hi) {
			t.Errorf("%s %s outside [%s,%s]", label, v, lo, hi)
		}
	}
	find := func(rs []row, key string) row {
		t.Helper()
		for _, r := range rs {
			if string(r["id"]) == key {
				return r
			}
		}
		t.Fatal("required fixture row absent")
		return nil
	}
	memberID, err := strconv.Atoi(e.fixtures["member_user_id"])
	if err != nil {
		t.Fatal(err)
	}
	remote := id == "device_poll.remote_approved.r1"
	requestID, deviceName, deviceIP := "00000000-0000-4000-8000-0000000000e5", "Fixture TV", "127.0.0.1"
	if remote {
		requestID = "00000000-0000-4000-8000-0000000000e6"
	}
	sessionTTL := e.jwt.RefreshExpiry()
	if remote {
		sessionTTL = 24 * time.Hour
	}

	// Exactly one new login session for the approving account.
	oldSessions, newSessions := rows(before["auth_sessions"]), rows(after["auth_sessions"])
	if len(newSessions) != len(oldSessions)+1 {
		t.Fatal("collection must add exactly one login session")
	}
	known := map[string]row{}
	for _, r := range oldSessions {
		known[string(r["id"])] = r
	}
	var session row
	for _, r := range newSessions {
		if prior, ok := known[string(r["id"])]; ok {
			equal("existing session", prior, r)
			delete(known, string(r["id"]))
			continue
		}
		if session != nil {
			t.Fatal("more than one session added")
		}
		session = r
	}
	if session == nil || len(known) != 0 {
		t.Fatal("prior sessions must all remain and one must be added")
	}
	sid := text(session["id"])
	if _, err := uuid.Parse(sid); err != nil {
		t.Error("collected session id is not UUID")
	}
	wantSession := maps.Clone(find(oldSessions, string(encode(e.fixtures["member_session_id"]))))
	wantSession["id"] = session["id"]
	wantSession["user_id"] = encode(memberID)
	wantSession["device_name"] = encode(deviceName)
	wantSession["ip_address"] = encode(deviceIP)
	wantSession["impersonator_user_id"] = encode(nil)
	wantSession["impersonation_started_at"] = encode(nil)
	wantSession["revoked_at"] = encode(nil)
	bounds("session created_at", session["created_at"], dbStart, dbEnd)
	wantSession["created_at"] = session["created_at"]
	bounds("session expires_at", session["expires_at"], appStart.Add(sessionTTL).Truncate(time.Microsecond), appEnd.Add(sessionTTL))
	wantSession["expires_at"] = session["expires_at"]
	equal("session", wantSession, session)
	storedExpiry := stamp(session["expires_at"])
	if remote && !storedExpiry.Before(appStart.Add(e.jwt.RefreshExpiry())) {
		t.Error("temporary session was not capped below the configured refresh lifetime")
	}

	// Exactly one request row moves approved -> consumed, bound to that session.
	oldRequests, newRequests := rows(before["device_login_requests"]), rows(after["device_login_requests"])
	if len(oldRequests) != len(newRequests) {
		t.Fatal("collection changed device request row count")
	}
	for _, old := range oldRequests {
		got := find(newRequests, string(old["id"]))
		want := maps.Clone(old)
		if text(old["id"]) == requestID {
			if text(old["status"]) != "approved" {
				t.Fatal("fixture request was not approved before collection")
			}
			want["status"] = encode("consumed")
			want["auth_session_id"] = encode(sid)
			bounds("request consumed_at", got["consumed_at"], appStart.Truncate(time.Microsecond), appEnd)
			if !bytes.Equal(got["consumed_at"], got["updated_at"]) {
				t.Error("consumed_at and updated_at differ")
			}
			want["consumed_at"] = got["consumed_at"]
			want["updated_at"] = got["updated_at"]
		}
		equal("device request", want, got)
	}

	// Credentials on the wire bind the account and the new session.
	var wire struct {
		Status           string         `json:"status"`
		Access           string         `json:"access_token"`
		Refresh          string         `json:"refresh_token"`
		Expires          int            `json:"expires_in"`
		User             map[string]any `json:"user"`
		ProfileID        *string        `json:"profile_id"`
		ProfileToken     *string        `json:"profile_token"`
		Temporary        *bool          `json:"temporary"`
		SessionExpiresAt *string        `json:"session_expires_at"`
		Tokens           *struct {
			Access  string         `json:"access_token"`
			Refresh string         `json:"refresh_token"`
			Expires int            `json:"expires_in"`
			User    map[string]any `json:"user"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(resp.Raw, &wire); err != nil {
		t.Fatal("poll response JSON invalid")
	}
	if wire.Status != "approved" {
		t.Error("collecting poll must report approved")
	}
	access, refresh, expires, user := wire.Access, wire.Refresh, wire.Expires, wire.User
	if transport == "v2" {
		if wire.Tokens == nil {
			t.Fatal("v2 collection must nest tokens")
		}
		access, refresh, expires, user = wire.Tokens.Access, wire.Tokens.Refresh, wire.Tokens.Expires, wire.Tokens.User
	} else if wire.Tokens != nil {
		t.Error("v1 must not nest tokens")
	}
	if expires != int(e.jwt.AccessExpiry().Seconds()) {
		t.Error("token response expiry differs from configured lifetime")
	}
	wireID := any(float64(memberID))
	if transport == "v2" {
		wireID = strconv.Itoa(memberID)
	}
	if user["id"] != wireID || user["username"] != memberUser || user["email"] != memberEmail || user["role"] != "user" {
		t.Error("collected account differs from the approving member")
	}
	if _, ok := user["impersonation"]; ok {
		t.Error("collection must not claim impersonation")
	}
	for _, token := range []struct {
		raw, kind string
		ttl       time.Duration
	}{{access, auth.TokenTypeAccess, e.jwt.AccessExpiry()}, {refresh, auth.TokenTypeRefresh, e.jwt.RefreshExpiry()}} {
		claims, err := e.jwt.ValidateToken(token.raw)
		if err != nil {
			t.Fatal("issued token signature or claims invalid")
		}
		if claims.UserID != memberID || claims.SessionID != sid || claims.Role != "user" || claims.TokenType != token.kind || claims.ProfileID != "" || claims.ImpersonatorUserID != nil || claims.APIKeyID != 0 {
			t.Error("issued token identity differs from the collected account/session")
		}
		if claims.ExpiresAt == nil || claims.ExpiresAt.Before(appStart.Add(token.ttl).Truncate(time.Second)) || claims.ExpiresAt.After(appEnd.Add(token.ttl)) {
			t.Error("issued token lifetime outside acceptance bounds")
		}
	}

	// Temporary profile authority is present exactly when the approval was remote.
	if !remote {
		if transport == "v1" {
			if wire.ProfileID != nil || wire.ProfileToken != nil || wire.Temporary != nil || wire.SessionExpiresAt != nil {
				t.Error("full login must omit profile authority fields on v1")
			}
		} else if wire.ProfileID == nil || *wire.ProfileID != "" || wire.ProfileToken == nil || *wire.ProfileToken != "" || wire.Temporary == nil || *wire.Temporary || wire.SessionExpiresAt != nil {
			t.Error("full login must emit empty profile authority fields on v2")
		}
		return
	}
	if wire.ProfileID == nil || *wire.ProfileID != profilePrimary || wire.Temporary == nil || !*wire.Temporary || wire.ProfileToken == nil || *wire.ProfileToken == "" || wire.SessionExpiresAt == nil {
		t.Fatal("remote collection must carry profile, temporary flag, profile token and session expiry")
	}
	profileClaims, err := e.profileTok.Validate(*wire.ProfileToken)
	if err != nil {
		t.Fatal("profile token signature or claims invalid")
	}
	member := find(rows(before["users"]), strconv.Itoa(memberID))
	var revision int64
	if err := json.Unmarshal(member["access_policy_revision"], &revision); err != nil {
		t.Fatal(err)
	}
	if profileClaims.UserID != memberID || profileClaims.SessionID != sid || profileClaims.ProfileID != profilePrimary || profileClaims.PolicyRevision != revision {
		t.Error("profile token identity differs from the collected account/session/profile/policy")
	}
	profile := find(rows(before["user_profiles"]), string(encode(profilePrimary)))
	var owner int
	if err := json.Unmarshal(profile["user_id"], &owner); err != nil || owner != memberID {
		t.Error("approved profile is not owned by the approving member")
	}
	if text(profile["pin_hash"]) != "" {
		t.Error("fixture primary profile must be unlocked for this evidence")
	}
	// The wire instant is the stored expiry truncated to the transport's precision.
	layout, precision := time.RFC3339, time.Second
	if transport == "v2" {
		layout, precision = "2006-01-02T15:04:05.000Z07:00", time.Millisecond
	}
	wireExpiry, err := time.Parse(layout, *wire.SessionExpiresAt)
	if err != nil || len(*wire.SessionExpiresAt) != len(time.Time{}.UTC().Format(layout)) {
		t.Fatal("session_expires_at is not in the transport's exact layout")
	}
	if !wireExpiry.Equal(storedExpiry.Truncate(precision)) {
		t.Errorf("session_expires_at %s differs from stored expiry %s at %s precision", wireExpiry, storedExpiry, precision)
	}
}

func TestDevicePollR1Selection(t *testing.T) {
	load := func() []*scenariocatalog.Catalog {
		t.Helper()
		c, err := scenariocatalog.Load()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if _, err := selectDevicePollR1(load()); err != nil {
		t.Fatal(err)
	}
	for _, id := range devicePollR1IDs {
		for _, mutation := range []string{"missing", "duplicate", "unpaired", "oracle", "request", "repeat", "status", "registration", "sequence"} {
			t.Run(id+"/"+mutation, func(t *testing.T) {
				catalogs := load()
				for _, c := range catalogs {
					for ri := range c.Rows {
						row := &c.Rows[ri]
						for si := range row.Scenarios {
							s := &row.Scenarios[si]
							if s.ID != id {
								continue
							}
							switch mutation {
							case "missing":
								s.ID = "removed"
							case "duplicate":
								row.Scenarios = append(row.Scenarios, *s)
							case "unpaired":
								s.V2Expectation = nil
							case "oracle":
								s.Expect.Status = 299
							case "request":
								s.V2Expectation.Request.RawBody = new("changed")
							case "repeat":
								s.V2Expectation.Request.Repeat = max(s.Request.Repeat, 1) + 1
							case "status":
								s.V2Expectation.Expect.Status = 299
							case "registration":
								row.RegistrationIndex = 0
							case "sequence":
								s.V2Expectation.Then = []scenariocatalog.V2Step{{}}
							}
							break
						}
					}
				}
				if _, err := selectDevicePollR1(catalogs); err == nil {
					t.Fatal("invalid packet accepted")
				}
			})
		}
	}
}
