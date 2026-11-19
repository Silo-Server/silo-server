package logfilter

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func newCapture() (*Handler, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := New(slog.NewTextHandler(buf, nil), "")
	return h, buf
}

func TestSetQuietAppliesToClones(t *testing.T) {
	h, buf := newCapture()

	// Clone first (as slog.With does), then change the quiet list — the
	// clone must see the update because the prefix list is shared.
	clone := slog.New(h.WithAttrs([]slog.Attr{slog.String("node", "a")}))
	clone.Info("metadata: before quiet")
	h.SetQuiet("metadata")
	clone.Info("metadata: after quiet")
	clone.Info("other: still logged")

	out := buf.String()
	if !strings.Contains(out, "metadata: before quiet") {
		t.Fatalf("message before SetQuiet was dropped: %s", out)
	}
	if strings.Contains(out, "metadata: after quiet") {
		t.Fatalf("clone did not pick up SetQuiet: %s", out)
	}
	if !strings.Contains(out, "other: still logged") {
		t.Fatalf("unrelated message was dropped: %s", out)
	}
}
