package push

import (
	"context"
	"testing"
)

func TestFCMResult_Success(t *testing.T) {
	if r, _ := fcmResult(nil); r != ResultSent {
		t.Fatal("nil err → sent")
	}
}

// TestFCMTransport_UnconfiguredSoftFails verifies that Send returns ResultSoftFail
// when no service-account JSON is configured, and that Name() returns "fcm".
//
// The Is* error-predicate paths (IsUnregistered → ResultDead, IsQuotaExceeded →
// ResultSoftFail+30s, etc.) require real FCM-typed errors which the SDK does not
// expose for direct construction. Those paths are exercised indirectly by the
// worker's dead-token integration test via the fake transport.
func TestFCMTransport_UnconfiguredSoftFails(t *testing.T) {
	tr := NewFCMTransport(func(context.Context) FCMConfig { return FCMConfig{} })
	res, _, _ := tr.Send(context.Background(), "tok", Payload{})
	if res != ResultSoftFail {
		t.Fatalf("unconfigured → soft fail, got %v", res)
	}
	if tr.Name() != "fcm" {
		t.Fatal("name")
	}
}
