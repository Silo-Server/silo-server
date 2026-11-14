package auditmutation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/jackc/pgx/v5"
)

func TestBoundUserAgent(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		limit           int
	}{
		{"empty", "", "", 4},
		{"ordinary", "Silo/1.0", "Silo/1.0", 12},
		{"ascii", "abcdef", "abcd", 4},
		{"exact multibyte", "a界b", "a界", 4},
		{"split multibyte", "ab界c", "ab", 4},
		{"split emoji", "abc😀z", "abc", 6},
		{"invalid run", "a\xff\xfeb", "a�b", 8},
		{"nul", "a\x00b", "a�b", 8},
		{"replacement expansion", "a\x00\x00b", "a�", 5},
		{"invalid prefix", "\xff\xff\xff\xff\xff", "�", 4},
		{"no room for replacement", "abc\xffx", "abc", 4},
		{"zero budget", "abc", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := boundUserAgent(tc.raw, tc.limit)
			if got != tc.want || !utf8.ValidString(got) || len(got) > tc.limit || strings.ContainsRune(got, 0) {
				t.Fatalf("got %q (%d bytes), want %q", got, len(got), tc.want)
			}
			if boundUserAgent(got, tc.limit) != got {
				t.Fatal("normalization is not idempotent")
			}
		})
	}
}

func TestBoundUserAgentOwnsShortAndTruncatedPrefixes(t *testing.T) {
	backing := strings.Clone("prefix-Synthetic agent-suffix")
	for _, raw := range []string{backing, backing[7:22]} {
		for _, limit := range []int{5, 40} {
			got := boundUserAgent(raw, limit)
			if unsafe.StringData(got) == unsafe.StringData(raw) {
				t.Fatal("diagnostic prefix retains input backing allocation")
			}
		}
	}
}

type mutationMetadataTx struct {
	pgx.Tx
	userAgent string
}

func (tx *mutationMetadataTx) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	tx.userAgent = args[11].(string)
	return &mutationMetadataRows{}, nil
}

type mutationMetadataRows struct {
	pgx.Rows
	read bool
}

func (r *mutationMetadataRows) Next() bool { wasRead := r.read; r.read = true; return !wasRead }
func (*mutationMetadataRows) Close()       {}
func (*mutationMetadataRows) Err() error   { return nil }
func (*mutationMetadataRows) Scan(dst ...any) error {
	*dst[0].(*int64), *dst[1].(*string) = 1, "7"
	return nil
}

func TestMutationBoundsUserAgentBeforePersistenceAndCommit(t *testing.T) {
	for _, raw := range []string{"Silo/1.0", "a\x00\xff\xfeb", strings.Repeat("agent/", 86)} {
		req := httptest.NewRequest(http.MethodPatch, "/api/v2/admin/users/7", nil)
		req.Header.Set("User-Agent", raw)
		var published []Entry
		lc := &LogContext{Request: req, Started: time.Now(), Publish: func(e Entry) { published = append(published, e) }}
		ctx := SetLogContext(t.Context(), lc)
		tx := &mutationMetadataTx{}
		entry, err := RecordMutation(ctx, tx, "user.updated", "user", "7", http.StatusNoContent, []Change{{Field: "password"}})
		if err != nil || entry == nil {
			t.Fatalf("record: %v, %v", entry, err)
		}
		want := BoundUserAgent(raw)
		if tx.userAgent != want || entry.UserAgent != want || len(published) != 0 {
			t.Fatal("persistence did not receive bounded metadata or published before commit")
		}
		CommitMutation(ctx, entry)
		if len(published) != 1 || published[0].UserAgent != want || lc.Committed[0].UserAgent != want {
			t.Fatal("committed projection differs from bounded persisted metadata")
		}
		if req.UserAgent() != raw {
			t.Fatal("mutation capture changed request header")
		}
	}
}
