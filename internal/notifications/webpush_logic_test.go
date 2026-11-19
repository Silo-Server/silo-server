package notifications

import (
	"context"
	"errors"
	"testing"
)

func TestWebPushSubscribeValidation(t *testing.T) {
	// Validation failures reject before any repository access, so a nil repo
	// is safe here.
	service := newWebPushService(nil, NewSettings(nil), nil)
	ctx := context.Background()

	cases := []struct {
		name     string
		endpoint string
		p256dh   string
		auth     string
	}{
		{"missing endpoint", "", "k", "a"},
		{"missing keys", "https://push.example.com/x", "", ""},
		{"plain http", "http://push.example.com/x", "k", "a"},
		{"loopback", "https://127.0.0.1/x", "k", "a"},
		{"v4-mapped loopback", "https://[::ffff:127.0.0.1]/x", "k", "a"},
		{"private network", "https://192.168.1.10/x", "k", "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Subscribe(ctx, 1, "p1", tc.endpoint, tc.p256dh, tc.auth, "test", "")
			if !errors.Is(err, ErrWebPushInvalid) {
				t.Fatalf("Subscribe = %v, want ErrWebPushInvalid", err)
			}
		})
	}
}
