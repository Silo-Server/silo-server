package executor

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestRequiredAvatarAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-avatar for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.AvatarAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	guardFrozenAPIKeyFixture(t)
	e := New(t)
	defer func() { e.Reseed(); e.guardScratchDatabase(); guardFrozenAPIKeyFixture(t) }()
	var results []Result
	requests, effects := 0, 0
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, s := range row.Scenarios {
				for _, transport := range []string{"v1", "v2"} {
					t.Run(s.ID+"/"+transport, func(t *testing.T) {
						// This focused runner supplies fresh state before AND after each transport,
						// including FreshState originals, and observes effects before teardown.
						e.Reseed()
						defer e.Reseed()
						objects := newAvatarObjects(t)
						base := e.live.URL
						if strings.HasPrefix(s.ID, "avatar_delete.") {
							cipher, err := secret.New([]byte(masterKey))
							if err != nil {
								t.Fatal(err)
							}
							live := httptest.NewServer(api.NewRouter(api.Dependencies{Config: e.config(), AppContext: e.ctx, DB: e.pool, SecretCipher: cipher, ClientIPResolver: clientip.NewResolver(nil), NodeID: "fixture-node", PublicURL: publicURL, UserStoreProvider: e.stores, PolicySystem: e.policy, S3Private: s3client.NewClient(s3client.BucketConfig{Endpoint: objects.server.URL, Region: "test", Bucket: avatarBucket, AccessKey: "synthetic", SecretKey: "synthetic", PathStyle: true})}))
							defer live.Close()
							base = live.URL
							avatarDeleteOverlay(t, e, s.ID, objects)
						}
						objectBefore, callsBefore := objects.snapshot()
						result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: c.File, Row: row.Key().String()}
						defer func() {
							if t.Failed() && len(result.Failures) == 0 {
								result.Failures = append(result.Failures, "scenario assertion failed; see test log")
							}
							results = append(results, result)
						}()
						if len(s.Settings) > 0 {
							settings := map[string]string{}
							for k, v := range s.Settings {
								resolved, err := e.substitute(v)
								if err != nil {
									t.Fatal(err)
								}
								settings[k] = resolved
							}
							defer e.applySettings(settings)()
						}
						snapshot := func() map[string]json.RawMessage {
							t.Helper()
							effects++
							var raw []byte
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM users x),'profiles',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_profiles x),'keys',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM api_keys x),'settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM server_settings x),'sessions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM auth_sessions x),'device_requests',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM device_login_requests x),'invitations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM invitations x),'invite_codes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM invite_codes x),'devices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_devices x),'legacy_device_settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_device_settings x),'canonical',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_setting_values x),'mutations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_setting_mutations x),'migration_rejects',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_setting_migration_rejects x),'legacy_settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_settings x))`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var rows map[string]json.RawMessage
							if err := json.Unmarshal(raw, &rows); err != nil {
								t.Fatal(err)
							}
							return rows
						}
						before := snapshot()
						if len(before) != 14 {
							t.Fatal("snapshot must contain fourteen full tables")
						}
						request, expect, principal, method := s.Request, s.Expect, s.Principal, row.Method
						if transport == "v2" {
							pair := s.V2Expectation
							request, expect, method = pair.Request, pair.Expect, pair.Method
							result.OperationID = pair.OperationID
							if pair.Principal != nil {
								principal = *pair.Principal
							}
						}
						requests += max(request.Repeat, 1)
						lower := time.Now()
						_, failures, err := e.exchange(base, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						upper := time.Now()
						after := snapshot()
						e.checkAvatarEffects(t, s.ID, before, after, lower, upper, objects, objectBefore, callsBefore)
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if !bytes.Equal(want, after[id]) {
								t.Errorf("unexpected stored change in %s", id)
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredAvatarScenarios); err != nil {
		t.Error(err)
	}
	if requests != 38 || effects != 76 {
		t.Errorf("paired avatar evidence %dHTTP/%dPG, want38/76", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}
