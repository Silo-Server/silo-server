package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// This packet observes the real asynchronous tracker through committed SQL, without
// replacing auth or the updater. Each exchange gets a fresh real router/tracker:
// reusing a one-minute coalescer across RESTART IDENTITY reseeds would suppress
// writes for a different fixture incarnation. These originals do not prove retries.
func keyUsageRouter(t *testing.T, e *Env) (*httptest.Server, context.CancelFunc) {
	t.Helper()
	cipher, err := secret.New([]byte(masterKey))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	return httptest.NewServer(api.NewRouter(api.Dependencies{
		Config: e.config(), AppContext: ctx, DB: e.pool, SecretCipher: cipher,
		ClientIPResolver: clientip.NewResolver(nil), NodeID: "fixture-node", PublicURL: publicURL,
		UserStoreProvider: e.stores, PolicySystem: e.policy,
		AuthProviders: []auth.RegisteredProvider{{Info: auth.LoginProviderInfo{ID: fixtureProviderID, DisplayName: "Fixture Directory", Mode: "credentials"}, Provider: rejectingProvider{}}},
	})), cancel
}

func keyUsageGuard(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	probe := &Env{t: t, ctx: t.Context(), pool: pool}
	probe.guardScratchDatabase()
	var exists bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('public.api_keys') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		return
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM api_keys WHERE label NOT IN ('fixture-unscoped','fixture-scoped','fixture-member-key')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("non-fixture API keys: refuse constructor/reseed")
	}
}

func keyUsageSnapshot(t *testing.T, e *Env) map[string]json.RawMessage {
	t.Helper()
	var raw []byte
	err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object(
 'users',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM users t),
 'profiles',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM user_profiles t),
 'keys',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM api_keys t),
 'settings',(SELECT jsonb_agg(to_jsonb(t) ORDER BY key) FROM server_settings t),
 'sessions',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM auth_sessions t),
 'device_requests',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM device_login_requests t),
 'invitations',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM invitations t),
 'invite_codes',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM invite_codes t))`).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	var rows map[string]json.RawMessage
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 8 {
		t.Fatal("expected eight complete tables")
	}
	return rows
}

func keyUsageEffects(t *testing.T, before, after map[string]json.RawMessage, target string, touched bool, start, end time.Time) {
	t.Helper()
	for name, want := range before {
		if name != "keys" {
			if !bytes.Equal(want, after[name]) {
				t.Errorf("unexpected %s table change", name)
			}
			continue
		}
		var old, newRows []map[string]json.RawMessage
		if err := json.Unmarshal(want, &old); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(after[name], &newRows); err != nil {
			t.Fatal(err)
		}
		if len(old) != len(newRows) {
			t.Fatal("key row count changed")
		}
		matched := 0
		for i, row := range old {
			got := newRows[i]
			if touched && string(row["id"]) == target {
				matched++
				if string(row["last_used_at"]) != "null" {
					t.Fatal("fresh key unexpectedly already touched")
				}
				var stamp time.Time
				if err := json.Unmarshal(got["last_used_at"], &stamp); err != nil {
					t.Fatal(err)
				}
				if stamp.Before(start) || stamp.After(end) {
					t.Error("last_used_at outside database request bounds")
				}
				// Only this exact observed target column may differ, after checking its value.
				row["last_used_at"] = got["last_used_at"]
			}
			if len(row) != len(got) {
				t.Error("key column count changed")
			}
			for k, v := range row {
				if !bytes.Equal(v, got[k]) {
					t.Errorf("unexpected key column change: %s", k)
				}
			}
		}
		if touched && matched != 1 {
			t.Fatalf("matched %d usage targets", matched)
		}
	}
}

func TestRequiredKeyAuthorityUsageAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run required key-authority-usage acceptance explicitly")
	}
	dsn := os.Getenv(DatabaseEnv)
	if dsn == "" {
		t.Fatal(DatabaseEnv + " required; no skipped database acceptance")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.KeyAuthorityUsageAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	guardPool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	keyUsageGuard(t, guardPool)
	guardPool.Close()
	t.Log("key usage pre-constructor guards passed")
	e := New(t)
	// The observation table is packet-only, outside the eight application tables.
	// NOTIFY is delivered only at commit. No timestamp is changed by this trigger.
	_, err = e.pool.Exec(e.ctx, `CREATE TABLE key_usage_observations (key_id bigint NOT NULL, stamp timestamptz NOT NULL);
 CREATE FUNCTION observe_key_usage() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 INSERT INTO key_usage_observations VALUES(NEW.id,NEW.last_used_at);
 PERFORM pg_notify('key_authority_usage',NEW.id::text); RETURN NEW; END $$;
 CREATE TRIGGER observe_key_usage AFTER UPDATE OF last_used_at ON api_keys FOR EACH ROW EXECUTE FUNCTION observe_key_usage()`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, err := e.pool.Exec(context.Background(), `DROP TRIGGER observe_key_usage ON api_keys; DROP FUNCTION observe_key_usage(); DROP TABLE key_usage_observations`)
		if err != nil {
			t.Error("observer cleanup:", err)
		}
	}()
	var results []Result
	observations, touches := 0, 0
	safe := true
	defer func() {
		if safe {
			e.Reseed()
			keyUsageGuard(t, e.pool)
		}
		if err := WriteReport(results); err != nil {
			t.Error(err)
		}
	}()
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, s := range row.Scenarios {
				for _, transport := range []string{"v1", "v2"} {
					passed := t.Run(s.ID+"/"+transport, func(t *testing.T) {
						e.Reseed()
						keyUsageGuard(t, e.pool)
						if _, err := e.pool.Exec(e.ctx, `TRUNCATE key_usage_observations`); err != nil {
							t.Fatal(err)
						}
						listener, err := pgx.Connect(t.Context(), dsn)
						if err != nil {
							t.Fatal(err)
						}
						defer func() {
							if err := listener.Close(context.Background()); err != nil {
								t.Error("listener cleanup:", err)
							}
						}()
						if _, err = listener.Exec(t.Context(), `LISTEN key_authority_usage`); err != nil {
							t.Fatal(err)
						}
						server, stop := keyUsageRouter(t, e)
						defer stop()
						defer server.Close()
						before := keyUsageSnapshot(t, e)
						observations++
						var start, end time.Time
						if err = e.pool.QueryRow(e.ctx, `SELECT clock_timestamp()`).Scan(&start); err != nil {
							t.Fatal(err)
						}
						request, expect, principal, method := s.Request, s.Expect, s.Principal, row.Method
						result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: c.File, Row: row.Key().String()}
						defer func() {
							if t.Failed() && len(result.Failures) == 0 {
								result.Failures = []string{"effect/producer assertion failed; see log"}
							}
							results = append(results, result)
						}()
						if transport == "v2" {
							pair := s.V2Expectation
							request, expect, method = pair.Request, pair.Expect, pair.Method
							result.OperationID = pair.OperationID
						}
						// Public and scoped-refusal cases stop before Touch. All six unscoped
						// cases reach Touch even if a later handler refuses key management/logout.
						touched := principal.Class == "api_key" && len(principal.Scopes) == 0
						safe = false
						_, failures, err := e.exchange(server.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("exchange: %v", failures)
						}
						// Shut off further HTTP producers before draining the one admitted SQL write.
						server.Close()
						stop()
						target := e.fixtures["admin_api_key_id"]
						if touched {
							ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
							defer cancel()
							notification, err := listener.WaitForNotification(ctx)
							if err != nil {
								t.Fatal("usage producer not observed; do not reseed:", err)
							}
							if notification.Payload != target {
								t.Fatal("unexpected usage target")
							}
							touches++
						}
						var n int
						var id int64
						var stamp *time.Time
						if err = e.pool.QueryRow(e.ctx, `SELECT count(*),coalesce(min(key_id),0),min(stamp) FROM key_usage_observations`).Scan(&n, &id, &stamp); err != nil {
							t.Fatal(err)
						}
						expected := 0
						if touched {
							expected = 1
						}
						if n != expected || (touched && strconv.FormatInt(id, 10) != target) {
							t.Fatal("unexpected committed usage count/identity", n, id)
						}
						// One Touch has one bounded goroutine, no scheduled retries. Its committed
						// UPDATE is its final DB effect; no task can leak through the next reseed.
						safe = true
						after := keyUsageSnapshot(t, e)
						observations++
						if err = e.pool.QueryRow(e.ctx, `SELECT clock_timestamp()`).Scan(&end); err != nil {
							t.Fatal(err)
						}
						keyUsageEffects(t, before, after, target, touched, start, end)
						if touched && (stamp == nil || stamp.Before(start) || stamp.After(end)) {
							t.Error("observation timestamp outside bounds")
						}
						t.Logf("%s: producer_commits=%d; eight full tables compared; drained=%t", result.ID, n, safe)
						e.Reseed()
					})
					if !passed && !safe {
						return
					}
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredKeyAuthorityUsageScenarios); err != nil {
		t.Error(err)
	}
	if observations != 36 || touches != 12 {
		t.Errorf("observations/touches=%d/%d want36/12", observations, touches)
	}
	t.Logf("18 original exchanges, %d eight-table snapshots, %d committed usage effects; no blanket normalization", observations, touches)
}

// Keep packet helpers isolated from other simultaneously tested executor files.
