package artworksource

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestValidatePublicImageURLRejectsPrivateAndNonHTTPOrigins(t *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd",
		"http://127.0.0.1/poster.jpg",
		"http://[::1]/poster.jpg",
		"http://169.254.169.254/latest/meta-data",
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidatePublicImageURL(parsed); err == nil {
			t.Fatalf("ValidatePublicImageURL(%q) succeeded", raw)
		}
	}
	parsed, _ := url.Parse("https://203.0.113.10/poster.jpg")
	if err := ValidatePublicImageURL(parsed); err != nil {
		t.Fatalf("public HTTPS URL rejected: %v", err)
	}
}

func TestRateLimitedReaderHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	limiter := rate.NewLimiter(1, 5)
	if !limiter.AllowN(time.Now(), 5) {
		t.Fatal("failed to exhaust test limiter")
	}
	reader := &rateLimitedReader{ctx: ctx, reader: strings.NewReader("image"), limiter: limiter}
	buffer := make([]byte, 5)
	if _, err := reader.Read(buffer); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read = %v, want context.Canceled", err)
	}
}
