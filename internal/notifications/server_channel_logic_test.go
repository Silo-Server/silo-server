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
	if headers["X-Vio-Event"] != ServerChannelEventContentAdded {
		t.Fatalf("unexpected event header %q", headers["X-Vio-Event"])
	}
	if headers["X-Vio-Channel-Id"] != "chan-1" {
		t.Fatalf("unexpected channel header %q", headers["X-Vio-Channel-Id"])
	}
	want := SignGenericWebhook("secret", now.Unix(), body)
	if headers["X-Vio-Signature"] != want {
		t.Fatalf("signature mismatch: %q != %q", headers["X-Vio-Signature"], want)
	}
}
