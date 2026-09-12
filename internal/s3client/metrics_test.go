package s3client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
