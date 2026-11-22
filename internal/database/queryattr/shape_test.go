package queryattr

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"
)

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
