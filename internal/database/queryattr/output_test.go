package queryattr

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&b, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &b
}
func logLines(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	return lines
}

type panicHandler struct{ slog.Handler }

func (panicHandler) Handle(context.Context, slog.Record) error { panic("sink failed") }
func TestSummaryRecoversSinkPanic(t *testing.T) {
	setup(t)
	buf := capture(t)
	handler := slog.Default().Handler()
	slog.SetDefault(slog.New(panicHandler{handler}))
	s := summary{previous: time.Unix(100, 0)}
	s.summarize(time.Unix(101, 0))
	slog.SetDefault(slog.New(handler))
	s.summarize(time.Unix(102, 0))
	lines := logLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("summary lines = %d, want 1", len(lines))
	}
	requireAttr(t, lines[0], "msg", "database query summary")
}
