package opslog

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Silo-Server/silo-server/internal/logstream"
)

type recordingWriter struct {
	mu      sync.Mutex
	entries []Entry
}

func (w *recordingWriter) Write(entry Entry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, entry)
}

func (w *recordingWriter) Close() error { return nil }

func (w *recordingWriter) messages() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.entries))
	for _, e := range w.entries {
		out = append(out, e.Message)
	}
	return out
}

// countingHandler stands in for the console sink.
type countingHandler struct {
	mu       sync.Mutex
	messages []string
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, r.Message)
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

func (h *countingHandler) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.messages...)
}

func TestHandlerLeavesPipelineRecordsToTheInnerHandler(t *testing.T) {
	t.Parallel()
	inner := &countingHandler{}
	writer := &recordingWriter{}
	logger := slog.New(NewHandler(inner, writer, slog.LevelInfo, "node-a"))

	logger.InfoContext(context.Background(), "captured")
	logger.WarnContext(withoutCapture(context.Background()), "console only")

	if got := writer.messages(); len(got) != 1 || got[0] != "captured" {
		t.Fatalf("captured entries = %q, want [captured]", got)
	}
	if got := inner.seen(); len(got) != 2 {
		t.Fatalf("inner handler saw %q, want both records", got)
	}
}

// TestConsumerFailureDoesNotFeedThePipeline runs the consumer against a
// database that refuses connections with the operational handler installed as
// the slog default, the way main wires it. The channel is closed, so the
// consumer is stopping and makes one attempt. The failed batch is counted and
// reported on the console, and nothing is written back into the pipeline.
func TestConsumerFailureDoesNotFeedThePipeline(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	inner := &countingHandler{}
	writer := &recordingWriter{}
	slog.SetDefault(slog.New(NewHandler(inner, writer, slog.LevelInfo, "node-a")))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	pool, err := pgxpool.New(context.Background(), "postgres://silo@"+addr+"/silo?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	dropped := insertFailedDrops(t)
	ch := make(chan Entry, 3)
	for range 3 {
		ch <- Entry{Level: "info", Component: "probe", Message: "probe"}
	}
	close(ch)
	NewConsumer(pool, logstream.NewHub("node-a", nil)).Run(context.Background(), ch)

	if got := insertFailedDrops(t) - dropped; got != 3 {
		t.Fatalf("insert_failed drops = %v, want 3", got)
	}
	if got := writer.messages(); len(got) != 0 {
		t.Fatalf("consumer wrote %q back into the pipeline, want nothing", got)
	}
	if got := inner.seen(); len(got) != 1 || got[0] != "opslog batch insert failed; entries dropped" {
		t.Fatalf("console saw %q, want the batch failure", got)
	}
}

// insertFailedDrops reads silo_log_writer_dropped_total for failed app inserts.
func insertFailedDrops(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "silo_log_writer_dropped_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["stream"] == string(logstream.StreamApp) && labels["reason"] == logstream.DropInsertFailed {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}
