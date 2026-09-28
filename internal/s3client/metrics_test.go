package s3client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestS3MetricsRetryStreamingAndPrivacy(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Length", "7")
		_, _ = io.WriteString(w, "fixture")
	}))
	defer srv.Close()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(old); _ = provider.Shutdown(context.Background()) })
	c := NewClient(BucketConfig{Endpoint: srv.URL, Bucket: "private-bucket", AccessKey: "private-key", SecretKey: "private-secret", PathStyle: true, Role: "checks"})
	c.s3Client = s3.New(c.s3Client.Options(), func(o *s3.Options) { o.Retryer = retry.AddWithMaxBackoffDelay(retry.NewStandard(), time.Millisecond) })
	before := testutil.ToFloat64(s3Bytes.WithLabelValues("checks", "download"))
	retries := testutil.ToFloat64(s3Retries.WithLabelValues("checks", "GetObject"))
	body, err := c.GetObjectStream(t.Context(), c.Bucket(), "private-object")
	if err != nil {
		t.Fatal(err)
	}
	if testutil.ToFloat64(s3Bytes.WithLabelValues("checks", "download")) != before {
		t.Fatal("instrumentation drained the streamed body")
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(data) != "fixture" {
		t.Fatalf("stream corrupted: %q %v", data, err)
	}
	if testutil.ToFloat64(s3Bytes.WithLabelValues("checks", "download")) != before+7 {
		t.Fatal("streamed bytes were not counted")
	}
	if attempts.Load() != 2 || testutil.ToFloat64(s3Retries.WithLabelValues("checks", "GetObject")) != retries+1 {
		t.Fatal("retry was not counted")
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "s3.GetObject" {
		t.Fatalf("unexpected operation spans: %v", spans)
	}
	encoded, err := json.Marshal(spans)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-bucket", "private-object", "private-secret", "private-key", srv.URL} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("trace exposed %q", secret)
		}
	}
	if _, err := c.PresignGetURL(t.Context(), c.Bucket(), "private-object", time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(exporter.GetSpans()) != 1 {
		t.Fatal("local URL signing was counted as a storage request")
	}
}

func TestS3DialsCounter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	c := NewClient(BucketConfig{Endpoint: srv.URL, Bucket: "b", AccessKey: "k", SecretKey: "s", PathStyle: true, Role: "checks"})
	before := testutil.ToFloat64(s3Dials.WithLabelValues("checks"))
	if _, err := c.ObjectExists(context.Background(), "b", "k"); err != nil {
		t.Fatal(err)
	}
	if testutil.ToFloat64(s3Dials.WithLabelValues("checks")) < before+1 {
		t.Fatal("dial counter did not increase")
	}
}

// traceHTTPClient allows late and overlapping transport callbacks without
// relying on DNS, TLS, or scheduler timing to produce the interleaving.
type traceHTTPClient func(*http.Request) (*http.Response, error)

func (f traceHTTPClient) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestS3DialsOverlapAndLateCompletion(t *testing.T) {
	success := s3Dials.WithLabelValues("checks")
	beforeSuccess := testutil.ToFloat64(success)
	var trace *httptrace.ClientTrace
	c := observedHTTPClient{role: "checks", inner: traceHTTPClient(func(r *http.Request) (*http.Response, error) {
		trace = httptrace.ContextClientTrace(r.Context())
		trace.GetConn("first")
		trace.GotConn(httptrace.GotConnInfo{Reused: true})
		trace.GetConn("redirect")
		trace.ConnectDone("tcp", "first", nil)
		trace.ConnectDone("tcp", "redirect", nil)
		trace.GotConn(httptrace.GotConnInfo{Reused: false})
		return nil, context.Canceled
	})}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Do(req)
	// Background dial callbacks can arrive after Do returns, even when no
	// GotConn follows (cancellation or a subsequent TLS handshake failure).
	trace.ConnectDone("tcp", "late", nil)
	trace.ConnectDone("tcp", "failed", errors.New("dial failed"))
	if got := testutil.ToFloat64(success) - beforeSuccess; got != 3 {
		t.Errorf("successful dials = %v, want 3", got)
	}
}
