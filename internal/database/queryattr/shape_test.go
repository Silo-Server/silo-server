package queryattr

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"
)

func TestShapeGrouping(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{"SELECT 1 WHERE a IN ($1,$2)", "SELECT 1 WHERE a IN ($1,$2,$3)", true},
		{"SELECT 1 WHERE a = 5", "SELECT 1 WHERE a = 7", true},
		{"SELECT 'a'", "SELECT 'b'", true},
		{"SELECT 1 /* first */", "SELECT 1 /* second */", true},
		{"SELECT 1 WHERE a", "SELECT 1 WHERE b", false},
	}
	for _, tc := range cases {
		t.Run(tc.a, func(t *testing.T) {
			setup(t)
			s := internSite("a")
			a, b := queryRow(s, tc.a), queryRow(s, tc.b)
			if (a.shape == b.shape) != tc.same {
				t.Errorf("shapes = %q, %q, want same=%v", a.shape, b.shape, tc.same)
			}
			if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(a.shape) {
				t.Errorf("shape = %q, want 16 lowercase hex digits", a.shape)
			}
		})
	}
}
func TestShapeCapUsesOther(t *testing.T) {
	setup(t)
	s := internSite("a")
	ctx := context.Background()
	for i := range 10 {
		r := queryRow(s, fmt.Sprintf("SELECT column%d FROM fixture", i))
		op := Op{row: r, kind: KindQuery}
		op.end(ctx, nil, 1, time.Time{})
	}
	if len(s.rows) != 9 {
		t.Fatalf("rows = %d, want 9", len(s.rows))
	}
	other := s.rows["other"]
	if other == nil || other.calls.Load() != 2 {
		t.Error("other row must hold last two calls")
	}
	for shape := range s.rows {
		if shape != "other" && !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(shape) {
			t.Errorf("unexpected shape %q", shape)
		}
	}
}
func TestShapeFixedKinds(t *testing.T) {
	setup(t)
	for _, tc := range []struct {
		kind  Kind
		shape string
	}{{KindBatch, "batch"}, {KindCopy, "copy"}} {
		op := Begin(context.Background(), tc.kind, "", nil, 1)
		if op.Shape() != tc.shape {
			t.Errorf("shape = %q, want %q", op.Shape(), tc.shape)
		}
	}
}
func TestShapeCacheClear(t *testing.T) {
	setup(t)
	s := internSite("a")
	for i := range 4097 {
		r := queryRow(s, fmt.Sprintf("SELECT %d", i))
		op := Op{row: r, kind: KindQuery}
		op.end(context.Background(), nil, 1, time.Time{})
	}
	if shapeCacheSize.Load() >= 4096 {
		t.Errorf("cache size = %d, want below cap after clear", shapeCacheSize.Load())
	}
	n := 0
	shapeCache.Range(func(_, _ any) bool { n++; return true })
	if n == 0 || n >= 4096 {
		t.Errorf("cache entries = %d, want 1..4095", n)
	}
	if len(s.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(s.rows))
	}
	for _, r := range s.rows {
		if r.calls.Load() != 4097 {
			t.Errorf("row calls = %d, want 4097", r.calls.Load())
		}
	}
}
func TestFrameRule(t *testing.T) {
	cases := []struct {
		name, display string
		skip          bool
	}{
		{modulePrefix + "internal/catalog.(*Repo).List", "catalog.(*Repo).List", false},
		{"main.main", "main.main", false},
		{modulePrefix + "internal/database.X", "database.X", true},
		{modulePrefix + "internal/database/queryattr.X", "database/queryattr.X", true},
		{"github.com/jackc/pgx/v5.X", "github.com/jackc/pgx/v5.X", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipFunction(tc.name); got != tc.skip {
				t.Errorf("skip = %v, want %v", got, tc.skip)
			}
			if got := displayName(tc.name); got != tc.display {
				t.Errorf("display = %q, want %q", got, tc.display)
			}
		})
	}
}
