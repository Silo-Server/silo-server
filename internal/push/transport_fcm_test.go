package push

import (
	"context"
	"testing"
	"time"
)

// TestFCMResult exercises the pure HTTP-status + FcmError-code classification
// helper. No network access is required.
func TestFCMResult(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		errorCode string
		wantRes   SendResult
		wantRetry time.Duration
	}{
		{"success", 200, "", ResultSent, 0},
		{"unregistered", 404, fcmErrUnregistered, ResultDead, 0},
		{"invalid_argument", 400, fcmErrInvalidArgument, ResultDead, 0},
		{"quota_exceeded", 429, fcmErrQuotaExceeded, ResultSoftFail, 30 * time.Second},
		{"unavailable", 503, fcmErrUnavailable, ResultSoftFail, 30 * time.Second},
		{"http_404_no_code", 404, "", ResultDead, 0},
		{"http_429_no_code", 429, "", ResultSoftFail, 30 * time.Second},
		{"http_503_no_code", 503, "", ResultSoftFail, 30 * time.Second},
		{"unknown", 500, "INTERNAL", ResultSoftFail, 0},
		{"unknown_no_code", 400, "", ResultSoftFail, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, retry := fcmResult(tt.status, tt.errorCode)
			if res != tt.wantRes {
				t.Fatalf("res: got %v want %v", res, tt.wantRes)
			}
			if retry != tt.wantRetry {
				t.Fatalf("retry: got %v want %v", retry, tt.wantRetry)
			}
		})
	}
}

// TestParseFCMErrorCode verifies extraction of the FcmError errorCode from a v1
// error response body, and graceful handling of missing/garbage bodies.
func TestParseFCMErrorCode(t *testing.T) {
	body := []byte(`{"error":{"status":"NOT_FOUND","details":[` +
		`{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`)
	if got := parseFCMErrorCode(body); got != "UNREGISTERED" {
		t.Fatalf("got %q want UNREGISTERED", got)
	}
	if got := parseFCMErrorCode([]byte("not json")); got != "" {
		t.Fatalf("garbage body: got %q want empty", got)
	}
	if got := parseFCMErrorCode([]byte(`{"error":{"status":"X"}}`)); got != "" {
		t.Fatalf("no details: got %q want empty", got)
	}
}

// TestFCMTransport_UnconfiguredSoftFails verifies that Send returns ResultSoftFail
// when no service-account JSON is configured, and that Name() returns "fcm".
//
// Live-network status/error-code paths are exercised by fcmResult /
// parseFCMErrorCode above and by the worker's dead-token integration test.
func TestFCMTransport_UnconfiguredSoftFails(t *testing.T) {
	tr := NewFCMTransport(func(context.Context) FCMConfig { return FCMConfig{} })
	res, _, err := tr.Send(context.Background(), "tok", Payload{})
	if res != ResultSoftFail {
		t.Fatalf("unconfigured → soft fail, got %v", res)
	}
	if err != nil {
		t.Fatalf("unconfigured → nil err, got %v", err)
	}
	if tr.Name() != "fcm" {
		t.Fatal("name")
	}
}
