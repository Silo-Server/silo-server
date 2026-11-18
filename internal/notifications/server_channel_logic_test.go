package notifications

import (
	"testing"
	"time"
)

const (
	testMovieTitle       = "Dune"
	testSeriesTitle      = "Severance"
	testSeriesPosterCDN  = "https://image.tmdb.org/t/p/w500/severance.jpg"
	testSeriesPosterPath = "tmdb://poster/severance.jpg"
)

func TestServerChannelHeadersSigned(t *testing.T) {
	body := []byte(`{"event":"content.added"}`)
	now := time.Unix(1_700_000_000, 0)
	headers := serverChannelHeaders(ServerChannelEventContentAdded, "chan-1", "secret", now, body)
	if headers["X-Silo-Event"] != ServerChannelEventContentAdded {
		t.Fatalf("unexpected event header %q", headers["X-Silo-Event"])
	}
	if headers["X-Silo-Channel-Id"] != "chan-1" {
		t.Fatalf("unexpected channel header %q", headers["X-Silo-Channel-Id"])
	}
	want := SignGenericWebhook("secret", now.Unix(), body)
	if headers["X-Silo-Signature"] != want {
		t.Fatalf("signature mismatch: %q != %q", headers["X-Silo-Signature"], want)
	}
}
