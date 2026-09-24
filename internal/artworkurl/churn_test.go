package artworkurl_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

// Clients and CDNs cache images by full URL, so every new URL for unchanged
// bytes costs a download the client already holds. Each delivery mode resolves
// one revisioned key through the production resolver once a minute for a
// simulated day, on two replicas; the second resolves 20 seconds after the
// first.
func TestRevisionedArtworkURLChurnOverADay(t *testing.T) {
	const key = "tmdb/movie/1/poster/w500.0123abcd.webp"
	presigned := s3client.BucketConfig{
		Endpoint: "https://s3.example.test", Region: "us-east-1", Bucket: "silo",
		AccessKey: "k", SecretKey: "s", PathStyle: true,
	}
	token := presigned
	token.PublicEndpoint = "https://cdn.example.test"
	token.URLAuth = s3client.URLAuthCloudflareToken
	token.TokenSecret = "secret"
	for _, tc := range []struct {
		name        string
		resolver    func() artworkurl.Resolver
		maxDistinct int
		minLifetime time.Duration
	}{
		{"local", func() artworkurl.Resolver {
			return artworkurl.NewServerResolver(artworkurl.NewSigner("secret", 4*time.Hour))
		}, 2, 4 * time.Hour},
		{"s3 presigned", func() artworkurl.Resolver { return directResolver(presigned) }, 2, 4 * time.Hour},
		// The WAF rule fixes a token's lifetime from its timestamp, so a token
		// holds for a quarter of the default three-hour token TTL.
		{"cloudflare token", func() artworkurl.Resolver { return directResolver(token) }, 33, 135 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := tc.resolver(), tc.resolver()
			synctest.Test(t, func(t *testing.T) {
				// The fake clock starts at midnight UTC; start mid-morning so
				// the day crosses one midnight.
				time.Sleep(3*time.Hour + 7*time.Minute)
				distinct := map[string]bool{}
				disagreements := 0
				for range 24 * 60 {
					now := time.Now()
					got := a.ResolveURLs(t.Context(), []string{key})[key]
					if got.URL == "" || got.ExpiresAt == nil {
						t.Fatalf("no URL at %s: %+v", now, got)
					}
					if lifetime := got.ExpiresAt.Sub(now); lifetime < tc.minLifetime {
						t.Fatalf("URL at %s is valid for %s, want at least %s", now, lifetime, tc.minLifetime)
					}
					distinct[got.URL] = true
					time.Sleep(20 * time.Second)
					if b.ResolveURLs(t.Context(), []string{key})[key].URL != got.URL {
						disagreements++
					}
					time.Sleep(40 * time.Second)
				}
				t.Logf("%d distinct URLs over 1440 per-minute resolves; replica disagreed %d times",
					len(distinct), disagreements)
				if len(distinct) > tc.maxDistinct || disagreements > 0 {
					t.Fatalf("got %d distinct URLs and %d replica disagreements, want at most %d and 0",
						len(distinct), disagreements, tc.maxDistinct)
				}
			})
		})
	}
}

// directResolver wires S3 delivery the way the server does.
func directResolver(cfg s3client.BucketConfig) artworkurl.Resolver {
	client := s3client.NewClient(cfg)
	return artworkurl.NewDirectResolver(blobstore.NewS3(client), client.EffectivePresignTTL(4*time.Hour))
}
