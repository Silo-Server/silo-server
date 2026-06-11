package push

import (
	"context"
	"testing"
)

type fakeSettings map[string]string

func (f fakeSettings) Get(_ context.Context, key string) (string, error) { return f[key], nil }

func TestConfig_Status(t *testing.T) {
	s := fakeSettings{
		"push.webpush.vapid_public":  "pub",
		"push.webpush.vapid_private": "priv",
		"push.webpush.subject":       "mailto:a@b.com",
	}
	c := NewConfig(s)
	st := c.Status(context.Background())
	if !st.WebPush || st.APNs || st.FCM {
		t.Fatalf("status = %+v, want only webpush", st)
	}
}

func TestConfig_APNsConfiguredRequiresAll(t *testing.T) {
	c := NewConfig(fakeSettings{
		"push.apns.p8_key": "k", "push.apns.key_id": "id", "push.apns.team_id": "t",
		// bundle_id missing → not configured
	})
	if c.APNs(context.Background()).Configured() {
		t.Fatal("apns must require all four fields")
	}
}
