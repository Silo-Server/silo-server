package activitylog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/logstream"
)

func TestMiddlewareBoundsAuditUserAgentBeforeWrite(t *testing.T) {
	// A fixed 516-byte fixture, independent of the HTTP server's header limit.
	raw := strings.Repeat("agent/", 86)
	for _, path := range []string{"/api/v1/missing", "/api/v2/missing", "/Sessions"} {
		t.Run(path, func(t *testing.T) {
			w := &captureWriter{}
			handler := NewFilteredMiddleware(w, "test-node", nil)(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if r.UserAgent() != raw {
					t.Fatal("request header changed")
				}
				rw.WriteHeader(http.StatusUnauthorized)
			}))
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("User-Agent", raw)
			handler.ServeHTTP(httptest.NewRecorder(), req)
			if len(w.entries) != 1 {
				t.Fatalf("got %d audit rows, want one", len(w.entries))
			}
			entry := w.entries[0]
			if entry.UserID != nil || entry.StatusCode != http.StatusUnauthorized || entry.Path != path {
				t.Fatalf("failed-request audit metadata changed: %+v", entry)
			}
			if len(entry.UserAgent) > 512 || entry.UserAgent != raw[:512] {
				t.Fatalf("captured User-Agent has %d bytes, want bounded prefix", len(entry.UserAgent))
			}
			if req.UserAgent() != raw {
				t.Fatal("original request header changed")
			}
		})
	}
}

func TestAuditUserAgentMatchesSafeText(t *testing.T) {
	for _, raw := range []string{"", "Silo/1.0", "a界😀b", "a\xff\xfeb", "a\x00b", "\xff\x00\xfe"} {
		if got, want := auditmutation.BoundUserAgent(raw), logstream.SafeText(raw); got != want {
			t.Fatalf("normalizing %q: got %q, want %q", raw, got, want)
		}
	}
}

type metadataQuery func(context.Context, string, ...any) []any

func (q metadataQuery) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return &metadataRows{values: q(ctx, sql, args...)}, nil
}

type metadataRows struct {
	pgx.Rows
	values []any
	read   bool
}

func (r *metadataRows) Next() bool { wasRead := r.read; r.read = true; return !wasRead }
func (*metadataRows) Close()       {}
func (*metadataRows) Err() error   { return nil }
func (r *metadataRows) Scan(dst ...any) error {
	if len(dst) != len(r.values) {
		return fmt.Errorf("scan columns: got %d, want %d", len(dst), len(r.values))
	}
	for i, value := range r.values {
		target := reflect.ValueOf(dst[i]).Elem()
		if value == nil {
			target.SetZero()
		} else {
			target.Set(reflect.ValueOf(value))
		}
	}
	return nil
}

func auditMetadataRow(raw string) []any {
	return []any{int64(1), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "192.0.2.1", nil, nil,
		"", "", "request-1", "test-node", "GET", "/synthetic", "/synthetic", 401, raw, 1}
}

func TestAuditPersistenceAndProjectionsBoundUserAgent(t *testing.T) {
	for _, raw := range []string{"Silo/1.0", "a\x00\xff\xfeb", strings.Repeat("界", 172)} {
		want := auditmutation.BoundUserAgent(raw)
		hub := logstream.NewHub("test-node", nil)
		tail, unsubscribe := hub.Subscribe(nil)
		consumer := &Consumer{streamHub: hub, pool: metadataQuery(func(_ context.Context, _ string, args ...any) []any {
			if args[12] != want {
				t.Fatal("unbounded User-Agent in persistence arguments")
			}
			// Inject a legacy value at the row seam as well, without a database.
			return auditMetadataRow(raw)
		})}
		if err := consumer.insertBatch(t.Context(), []LogEntry{{UserAgent: raw}}); err != nil {
			t.Fatal(err)
		}
		select {
		case message := <-tail:
			var entry AuditEntry
			if len(message.Entry) > 2048 || json.Unmarshal(message.Entry, &entry) != nil || entry.UserAgent != want {
				t.Fatal("live serialization did not retain bounded metadata")
			}
		default:
			t.Fatal("ordinary audit visibility lost")
		}
		unsubscribe()
		hub.Close()

		repo := &Repo{pool: metadataQuery(func(_ context.Context, sql string, _ ...any) []any {
			projection := fmt.Sprintf("LEFT(COALESCE(user_agent, ''), %d)", auditmutation.MaxUserAgentBytes)
			if !strings.Contains(sql, projection) {
				t.Fatal("legacy SQL projection does not bound returned characters")
			}
			return append(auditMetadataRow(raw), "", "", "", nil)
		})}
		result, err := repo.List(t.Context(), ListOptions{})
		if err != nil || len(result.Entries) != 1 || result.Entries[0].UserAgent != want {
			t.Fatalf("retained projection: %+v, %v", result, err)
		}
		for _, entry := range []AuditEntry{result.Entries[0], WithoutDetails(result.Entries[0])} {
			encoded, err := json.Marshal(entry)
			if err != nil || len(encoded) > 2048 || entry.Path != "/synthetic" || entry.StatusCode != 401 || entry.RequestID != "request-1" {
				t.Fatal("v1/v2 audit projection changed ordinary fields or exceeded finite fixture size")
			}
		}
	}
}
