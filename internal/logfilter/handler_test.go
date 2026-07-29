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

func TestHandlerFiltersConfiguredPrefixes(t *testing.T) {
	buf := &bytes.Buffer{}
	h := New(slog.NewTextHandler(buf, nil), "metadata, scanner")
	logger := slog.New(h)

	logger.Info("metadata: cached image")
	logger.Info("scanner: walked folder")
	logger.Info("playback: session started")

	out := buf.String()
	if strings.Contains(out, "metadata:") || strings.Contains(out, "scanner:") {
		t.Fatalf("quiet-prefixed messages leaked: %s", out)
	}
	if !strings.Contains(out, "playback: session started") {
		t.Fatalf("unrelated message was dropped: %s", out)
	}
}

func TestHandlerAcceptsOptionalTrailingColonInConfiguredPrefix(t *testing.T) {
	buf := &bytes.Buffer{}
	h := New(slog.NewTextHandler(buf, nil), "metadata:")
	logger := slog.New(h)

	logger.Info("metadata: cached image")
	logger.Info("playback: session started")

	out := buf.String()
	if strings.Contains(out, "metadata: cached image") {
		t.Fatalf("colon-suffixed quiet prefix was treated as metadata:: %s", out)
	}
	if !strings.Contains(out, "playback: session started") {
		t.Fatalf("unrelated message was dropped: %s", out)
	}
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

func TestEmptyQuietPassesEverything(t *testing.T) {
	h, buf := newCapture()
	logger := slog.New(h)

	logger.Info("metadata: not quiet by default")
	if !strings.Contains(buf.String(), "metadata: not quiet by default") {
		t.Fatalf("empty quiet list dropped a message: %s", buf.String())
	}
}
