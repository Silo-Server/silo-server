package push

import (
	"context"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	if parseRetryAfter("120") != 2*time.Minute {
		t.Fatal("seconds form")
	}
	if parseRetryAfter("") != 0 || parseRetryAfter("garbage") != 0 {
		t.Fatal("empty/garbage → 0")
	}
}

func TestWebPushTopic_ShortAndStable(t *testing.T) {
	tp := webpushTopic(123456789)
	if len(tp) > 32 || tp == "" {
		t.Fatalf("topic %q invalid length", tp)
	}
	if tp != webpushTopic(123456789) {
		t.Fatal("topic must be stable for the same id")
	}
}

func TestWebPush_MalformedSubscriptionIsDead(t *testing.T) {
	tr := NewWebPushTransport(func(context.Context) WebPushConfig {
		return WebPushConfig{VAPIDPublic: "p", VAPIDPrivate: "k", Subject: "mailto:a@b.c"}
	})
	res, _, _ := tr.Send(context.Background(), "{not json", Payload{})
	if res != ResultDead {
		t.Fatalf("malformed sub → dead, got %v", res)
	}
}

func TestWebPush_UnconfiguredSoftFails(t *testing.T) {
	tr := NewWebPushTransport(func(context.Context) WebPushConfig { return WebPushConfig{} })
	res, _, _ := tr.Send(context.Background(), `{"endpoint":"https://x","keys":{"p256dh":"a","auth":"b"}}`, Payload{})
	if res != ResultSoftFail {
		t.Fatalf("unconfigured → soft fail, got %v", res)
	}
	if tr.Name() != TransportWebPush {
		t.Fatalf("expected name %q, got %q", TransportWebPush, tr.Name())
	}
}
