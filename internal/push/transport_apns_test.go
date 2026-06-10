package push

import (
	"context"
	"testing"

	"github.com/sideshow/apns2"
)

func TestAPNsResultMapping(t *testing.T) {
	cases := []struct {
		reason string
		status int
		want   SendResult
	}{
		{"", 200, ResultSent},
		{apns2.ReasonUnregistered, 410, ResultDead},
		{apns2.ReasonBadDeviceToken, 400, ResultDead},
		{apns2.ReasonDeviceTokenNotForTopic, 400, ResultDead},
		{apns2.ReasonTooManyRequests, 429, ResultSoftFail},
		{"", 503, ResultSoftFail},
		{"PayloadTooLarge", 413, ResultSoftFail},
	}
	for _, c := range cases {
		got, _ := apnsResult(c.reason, c.status)
		if got != c.want {
			t.Fatalf("reason %q status %d: got %v want %v", c.reason, c.status, got, c.want)
		}
	}
	if _, ra := apnsResult(apns2.ReasonTooManyRequests, 429); ra == 0 {
		t.Fatal("429 should carry a retry-after")
	}
}

func TestAPNsTransport_UnconfiguredSoftFails(t *testing.T) {
	tr := NewAPNsTransport(func(context.Context) APNsConfig { return APNsConfig{} })
	res, _, _ := tr.Send(context.Background(), "tok", Payload{})
	if res != ResultSoftFail {
		t.Fatalf("unconfigured → soft fail, got %v", res)
	}
	if tr.Name() != "apns" {
		t.Fatal("name")
	}
}
