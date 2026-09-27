package executor

import (
	"encoding/json"
	"testing"
)

// accountSnapshot captures the shared account-scoped tables used by refusal
// scenarios. Keeping this query in one helper makes future schema additions a
// single test maintenance point while callers retain their own evidence counts.
func accountSnapshot(t *testing.T, e *Env) map[string]json.RawMessage {
	t.Helper()
	var raw []byte
	if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p),'keys',(SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM api_keys k),'settings',(SELECT jsonb_agg(to_jsonb(s) ORDER BY key) FROM server_settings s),'sessions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM auth_sessions a),'device_requests',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM device_login_requests d))`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var rows map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}
