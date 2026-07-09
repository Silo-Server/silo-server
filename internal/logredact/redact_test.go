package logredact

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"testing/slogtest"
)

func TestSlogtestConformance(t *testing.T) {
	var buf bytes.Buffer
	h := New(slog.NewJSONHandler(&buf, nil))
	results := func() []map[string]any {
		var ms []map[string]any
		for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal(line, &m); err != nil {
				t.Fatalf("unmarshal %q: %v", line, err)
			}
			ms = append(ms, m)
		}
		return ms
	}
	if err := slogtest.TestHandler(h, results); err != nil {
		t.Fatalf("slogtest: %v", err)
	}
}

func TestSecretKey(t *testing.T) {
	for _, k := range []string{"password", "api_key", "apiKey", "X-Authorization", "session_token", "Cookie", "client_secret"} {
		if !SecretKey(k) {
			t.Errorf("SecretKey(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"user_id", "request_id", "component", "folder", "count"} {
		if SecretKey(k) {
			t.Errorf("SecretKey(%q) = true, want false", k)
		}
	}
}

func logAndCapture(t *testing.T, fn func(l *slog.Logger)) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	l := slog.New(New(base))
	fn(l)
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, buf.String())
	}
	return out
}

func TestHandlerRedactsRecordAttr(t *testing.T) {
	out := logAndCapture(t, func(l *slog.Logger) {
		l.InfoContext(context.Background(), "auth", "user_id", 7, "api_token", "s3cr3t")
	})
	if out["api_token"] != Placeholder {
		t.Errorf("api_token = %v, want %s", out["api_token"], Placeholder)
	}
	if out["user_id"].(float64) != 7 {
		t.Errorf("user_id = %v, want 7 (non-secret must pass through)", out["user_id"])
	}
	if strings.Contains(out["api_token"].(string), "s3cr3t") {
		t.Error("secret value leaked")
	}
}

func TestHandlerRedactsWithAttrs(t *testing.T) {
	out := logAndCapture(t, func(l *slog.Logger) {
		l.With("password", "hunter2", "component", "auth").InfoContext(context.Background(), "bound")
	})
	if out["password"] != Placeholder {
		t.Errorf("bound password = %v, want %s", out["password"], Placeholder)
	}
	if out["component"] != "auth" {
		t.Errorf("component = %v, want auth", out["component"])
	}
}

func TestHandlerRedactsGroup(t *testing.T) {
	out := logAndCapture(t, func(l *slog.Logger) {
		l.InfoContext(context.Background(), "grp", slog.Group("creds", "authorization", "Bearer x", "kind", "oauth"))
	})
	creds, ok := out["creds"].(map[string]any)
	if !ok {
		t.Fatalf("creds group missing: %v", out)
	}
	if creds["authorization"] != Placeholder {
		t.Errorf("grouped authorization = %v, want %s", creds["authorization"], Placeholder)
	}
	if creds["kind"] != "oauth" {
		t.Errorf("grouped kind = %v, want oauth", creds["kind"])
	}
}

func TestHandlerRedactsSecretGroupKey(t *testing.T) {
	// A group whose OWN key is a secret marker must have its entire subtree
	// masked, not just secret leaf keys within it.
	out := logAndCapture(t, func(l *slog.Logger) {
		l.InfoContext(context.Background(), "x",
			slog.Group("authorization", "scheme", "Bearer", "value", "abc123SECRET"))
	})
	if out["authorization"] != Placeholder {
		t.Errorf("secret group key = %v, want %s (whole subtree masked)", out["authorization"], Placeholder)
	}
	if strings.Contains(mustJSON(t, out), "abc123SECRET") {
		t.Error("secret value leaked from secret-keyed group")
	}
}

type secretValuer struct{ tok string }

func (s secretValuer) LogValue() slog.Value {
	return slog.GroupValue(slog.String("token", s.tok))
}

func TestHandlerResolvesLogValuer(t *testing.T) {
	out := logAndCapture(t, func(l *slog.Logger) {
		l.InfoContext(context.Background(), "x", "creds", secretValuer{tok: "abc123SECRET"})
	})
	if strings.Contains(mustJSON(t, out), "abc123SECRET") {
		t.Errorf("secret behind LogValuer leaked: %v", out)
	}
}

func TestHandlerRedactsUnderWithGroup(t *testing.T) {
	out := logAndCapture(t, func(l *slog.Logger) {
		l.WithGroup("req").InfoContext(context.Background(), "x", "api_key", "abc123SECRET", "path", "/v1")
	})
	req, ok := out["req"].(map[string]any)
	if !ok {
		t.Fatalf("req group missing: %v", out)
	}
	if req["api_key"] != Placeholder {
		t.Errorf("api_key under WithGroup = %v, want %s", req["api_key"], Placeholder)
	}
	if req["path"] != "/v1" {
		t.Errorf("path under WithGroup = %v, want /v1", req["path"])
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestHandlerPassThroughWhenClean(t *testing.T) {
	out := logAndCapture(t, func(l *slog.Logger) {
		l.InfoContext(context.Background(), "clean", "user_id", 1, "component", "api")
	})
	if out["user_id"].(float64) != 1 || out["component"] != "api" {
		t.Errorf("clean record altered: %v", out)
	}
}
