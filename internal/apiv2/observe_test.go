package apiv2

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/logredact"
)

// captureLogs routes slog through the production redaction handler into a
// buffer for the duration of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(logredact.New(slog.NewJSONHandler(&buf, nil))))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// counterValue reads one series of a CounterVec by its full label set.
func counterValue(t *testing.T, vec *prometheus.CounterVec, labels prometheus.Labels) float64 {
	t.Helper()
	c, err := vec.GetMetricWith(labels)
	if err != nil {
		t.Fatalf("labels %v: %v", labels, err)
	}
	return testutil.ToFloat64(c)
}

func TestV1PrefixSkipMatchesDelegation(t *testing.T) {
	if apimw.APIv2Prefix != Prefix+"/" || !strings.HasPrefix(DelegationPattern, apimw.APIv2Prefix) {
		t.Fatalf("apimw.APIv2Prefix = %q, delegation %q", apimw.APIv2Prefix, DelegationPattern)
	}
}

func TestRequestLabelsAreStable(t *testing.T) {
	buf := captureLogs(t)
	h := newTestHandler(t, parityDeps(false))

	// A public success by a named client.
	before := counterValue(t, requestsTotal, prometheus.Labels{"api_major": "2", "operation_id": "getSystemInfo", "method": "GET",
		"status_class": "2xx", "error_code": "none", "auth_class": "public", "client": "Silo Apple TV"})
	rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", map[string]string{"X-Silo-Client": " Silo Apple TV\t", "X-Silo-Client-Version": "1.2.3"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	after := counterValue(t, requestsTotal, prometheus.Labels{"api_major": "2", "operation_id": "getSystemInfo", "method": "GET",
		"status_class": "2xx", "error_code": "none", "auth_class": "public", "client": "Silo Apple TV"})
	if after != before+1 {
		t.Fatalf("public success series: %v -> %v", before, after)
	}
	line := buf.String()
	for _, want := range []string{`"component":"apiv2"`, `"api_major":2`, `"operation_id":"getSystemInfo"`, `"status_class":"2xx"`,
		`"error_code":"none"`, `"auth_class":"public"`, `"client_name":"Silo Apple TV"`, `"client_version":"1.2.3"`,
		`"path_pattern":"/api/v2/system/info"`, `"request_id":"` + requestIDHeader(rec) + `"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log line lacks %s: %s", want, line)
		}
	}
	if strings.Contains(line, `"path":`) || strings.Contains(line, `"user_agent"`) {
		t.Errorf("log line carries a raw path or user agent: %s", line)
	}

	// A session-authenticated denial: the gate's problem type is the error code.
	buf.Reset()
	rec = do(t, h, http.MethodPost, "/api/v2/probe/profile_scoped", `{"name":"x","cleared":null}`,
		with(bearer(memberToken), "X-Profile-Id", "p-locked"))
	requireProblem(t, rec, TypeProfileVerificationRequired)
	if v := counterValue(t, requestsTotal, prometheus.Labels{"api_major": "2", "operation_id": "probeprofileScoped", "method": "POST",
		"status_class": "4xx", "error_code": TypeProfileVerificationRequired.ID, "auth_class": "anonymous", "client": "none"}); v < 1 {
		t.Fatalf("denial series not counted")
	}
	if line := buf.String(); !strings.Contains(line, `"error_code":"profile_verification_required"`) || !strings.Contains(line, `"auth_class":"anonymous"`) {
		t.Errorf("denial log: %s", line)
	}

	// An admitted session credential.
	buf.Reset()
	rec = do(t, h, http.MethodPost, "/api/v2/probe/authenticated", `{"name":"x","cleared":null}`, bearer(memberToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if line := buf.String(); !strings.Contains(line, `"auth_class":"session"`) || !strings.Contains(line, `"user_id":1`) {
		t.Errorf("admitted log: %s", line)
	}

	// The router fallbacks have no operation; the label is "none", never the path.
	buf.Reset()
	rec = do(t, h, http.MethodGet, "/api/v2/nowhere/12345", "", nil)
	requireProblem(t, rec, TypeNotFound)
	if v := counterValue(t, requestsTotal, prometheus.Labels{"api_major": "2", "operation_id": "none", "method": "GET",
		"status_class": "4xx", "error_code": TypeNotFound.ID, "auth_class": "anonymous", "client": "none"}); v < 1 {
		t.Fatalf("404 series not counted")
	}
	if line := buf.String(); strings.Contains(line, "12345") || !strings.Contains(line, `"operation_id":"none"`) {
		t.Errorf("404 log: %s", line)
	}
}

func TestValidationFailureCounter(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	before := counterValue(t, validationFailures, prometheus.Labels{"operation_id": "probepublic"})
	rec := do(t, h, http.MethodPost, "/api/v2/probe/public", `{"count":99}`, nil)
	requireProblem(t, rec, TypeValidationFailed)
	if after := counterValue(t, validationFailures, prometheus.Labels{"operation_id": "probepublic"}); after != before+1 {
		t.Fatalf("validation counter %v -> %v", before, after)
	}
	// Registered from the start, even though nothing increments it yet.
	if v := counterValue(t, v1TombstoneRequests, prometheus.Labels{"method": "GET"}); v != 0 {
		t.Fatalf("tombstone counter = %v before any tombstone", v)
	}
	RecordV1Tombstone(http.MethodGet)
	if v := counterValue(t, v1TombstoneRequests, prometheus.Labels{"method": "GET"}); v != 1 {
		t.Fatalf("tombstone counter = %v", v)
	}
	var m dto.Metric
	if err := requestDuration.WithLabelValues("2", "probepublic", "POST").(prometheus.Histogram).Write(&m); err != nil || m.Histogram.GetSampleCount() == 0 {
		t.Fatalf("duration not observed: %v %v", err, m.Histogram.GetSampleCount())
	}
}

func TestSecretBearingRequestIsNotLogged(t *testing.T) {
	buf := captureLogs(t)
	h := newTestHandler(t, parityDeps(false))
	const secret = "sk-very-secret-0123456789"
	r := httptest.NewRequest(http.MethodGet, "/api/v2/system/info?token="+secret+"&api_key="+secret, nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	r.Header.Set("Cookie", "session="+secret)
	r.Header.Set("X-Silo-Client", "probe\x00"+secret[:2])
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireProblem(t, rec, TypeValidationFailed) // unknown query parameters
	out := buf.String()
	if out == "" {
		t.Fatal("no log line")
	}
	if strings.Contains(out, secret) || strings.Contains(out, "token=") || strings.Contains(out, "api_key") {
		t.Fatalf("log line leaks the request's secrets: %s", out)
	}
	if !strings.Contains(out, `"client_name":"probesk"`) {
		t.Errorf("client name not clamped: %s", out)
	}
	if !strings.Contains(out, `"error_code":"validation_failed"`) {
		t.Errorf("error code missing: %s", out)
	}
}

func TestClientLabelIsBounded(t *testing.T) {
	clientLabels.Lock()
	clientLabels.seen = map[string]bool{}
	clientLabels.Unlock()
	for i := 0; i < maxClientLabelValues; i++ {
		if got := clientLabel("client-" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26))); got == labelOther {
			t.Fatalf("bucketed before the bound at %d", i)
		}
	}
	if got := clientLabel("one-too-many"); got != labelOther {
		t.Fatalf("unbounded client label %q", got)
	}
	if got := clientLabel(""); got != labelNone {
		t.Fatalf("empty client label %q", got)
	}
	if got := statusClass(0); got != "abandoned" {
		t.Fatalf("statusClass(0) = %q", got)
	}
	_ = context.Background()
}

func TestMethodLabelIsBounded(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		if got := methodLabel(m); got != m {
			t.Errorf("methodLabel(%q) = %q", m, got)
		}
	}
	if got := methodLabel("BREW"); got != labelOther {
		t.Fatalf("methodLabel(BREW) = %q", got)
	}

	// The router answers an invented method with the 405 fallback; the
	// series it lands on carries "other", and no series names the method.
	buf := captureLogs(t)
	labels := prometheus.Labels{"api_major": "2", "operation_id": "none", "method": labelOther,
		"status_class": "4xx", "error_code": TypeMethodNotAllowed.ID, "auth_class": "anonymous", "client": "none"}
	before := counterValue(t, requestsTotal, labels)
	rec := do(t, h, "BREW", "/api/v2/system/info", "", nil)
	requireProblem(t, rec, TypeMethodNotAllowed)
	if after := counterValue(t, requestsTotal, labels); after != before+1 {
		t.Fatalf("other-method series: %v -> %v", before, after)
	}
	for _, vec := range []prometheus.Collector{requestsTotal, requestDuration} {
		ch := make(chan prometheus.Metric)
		go func() {
			vec.Collect(ch)
			close(ch)
		}()
		for metric := range ch {
			var m dto.Metric
			if err := metric.Write(&m); err != nil {
				t.Fatal(err)
			}
			for _, lp := range m.GetLabel() {
				if lp.GetName() == labelMethod && lp.GetValue() == "BREW" {
					t.Fatalf("series minted for raw method: %v", metric.Desc())
				}
			}
		}
	}
	if line := buf.String(); !strings.Contains(line, `"method":"other"`) || strings.Contains(line, "BREW") {
		t.Errorf("log line for an invented method: %s", line)
	}
}
