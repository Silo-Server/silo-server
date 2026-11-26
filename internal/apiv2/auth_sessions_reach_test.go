package apiv2

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestLimitedLoginSessionViewDecidesEveryMember: another session's item, as a
// session limited to itself sees it, carries its id, the caller's created_at
// and expires_at, and nothing else. Regression: a member added to
// LoginSession would otherwise reach a device signed in by code before
// anyone decided whether it may see it; add the member here with its
// decision.
func TestLimitedLoginSessionViewDecidesEveryMember(t *testing.T) {
	created, expires := NewInstant(time.Unix(100, 0)), NewInstant(time.Unix(200, 0))
	own := LoginSession{ID: "own", DeviceName: "TV", IPAddress: "192.0.2.1", CreatedAt: created, ExpiresAt: expires,
		DeviceID: "device", DevicePlatform: "tvOS", LastSeenAt: NullableInstant{Valid: true, Time: created}, Current: true}
	got := reflect.ValueOf(limitedLoginSessionView("other", own))
	decided := map[string]any{
		"id":              ID("other"),
		"device_name":     "",
		"ip_address":      "",
		"created_at":      created,
		"expires_at":      expires,
		"device_id":       "",
		"device_platform": "",
		"last_seen_at":    NullableInstant{},
		"current":         false,
	}
	typ := got.Type()
	if typ.NumField() != len(decided) {
		t.Fatalf("LoginSession has %d members and %d are decided", typ.NumField(), len(decided))
	}
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		want, ok := decided[name]
		if !ok {
			t.Fatalf("LoginSession member %s has no decision for a session limited to itself", name)
		}
		if !reflect.DeepEqual(got.Field(i).Interface(), want) {
			t.Fatalf("%s = %v, want %v", name, got.Field(i).Interface(), want)
		}
	}
}
