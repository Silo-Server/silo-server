package s3client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// statTestServer answers HEAD for one known object and 404s everything else.
func statTestServer(t *testing.T, wantPath string) (*httptest.Server, *string) {
	t.Helper()
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		if r.URL.Path != wantPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Content-Length", "1234")
		w.Header().Set("ETag", `"deadbeef"`)
		w.Header().Set("Last-Modified", "Tue, 14 Nov 2023 22:13:20 GMT")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, &seenPath
}

func TestStatObjectReturnsMetadataAndAppliesKeyPrefix(t *testing.T) {
	server, seenPath := statTestServer(t, "/silo/silo/dev/poster.webp")
	client := NewClient(BucketConfig{
		Endpoint:  server.URL,
		Region:    "us-east-1",
		Bucket:    "silo",
		KeyPrefix: "silo/dev",
		AccessKey: "test",
		SecretKey: "test",
		PathStyle: true,
	})

	info, err := client.StatObject(context.Background(), client.Bucket(), "poster.webp")
	if err != nil {
		t.Fatalf("StatObject: %v", err)
	}
	if *seenPath != "/silo/silo/dev/poster.webp" {
		t.Fatalf("request path = %q, want the prefixed object path", *seenPath)
	}
	// The caller gets its own logical key back, never the physical one.
	if info.Key != "poster.webp" {
		t.Errorf("Key = %q, want the logical key", info.Key)
	}
	if info.SizeBytes != 1234 {
		t.Errorf("SizeBytes = %d, want 1234", info.SizeBytes)
	}
	if info.ContentType != "image/webp" {
		t.Errorf("ContentType = %q, want image/webp", info.ContentType)
	}
	if info.ETag != `"deadbeef"` {
		t.Errorf("ETag = %q, want the quoted bucket ETag", info.ETag)
	}
	if info.LastModified == nil || !info.LastModified.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("LastModified = %v, want 2023-11-14T22:13:20Z", info.LastModified)
	}
}

func TestStatObjectMissingReturnsErrNotFound(t *testing.T) {
	server, _ := statTestServer(t, "/silo/present.webp")
	client := NewClient(BucketConfig{
		Endpoint:  server.URL,
		Region:    "us-east-1",
		Bucket:    "silo",
		AccessKey: "test",
		SecretKey: "test",
		PathStyle: true,
	})

	if _, err := client.StatObject(context.Background(), client.Bucket(), "absent.webp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("StatObject = %v, want ErrNotFound", err)
	}
}

// Unsigned public-endpoint URLs are permanent; every other mode expires.
func TestReadURLExpires(t *testing.T) {
	tests := []struct {
		name           string
		publicEndpoint string
		urlAuth        string
		want           bool
	}{
		{"presigned", "", URLAuthPresigned, true},
		{"presigned with a read endpoint", "https://cdn.example", URLAuthPresigned, true},
		{"cloudflare token", "https://cdn.example", URLAuthCloudflareToken, true},
		{"public without a read endpoint falls back to presigning", "", URLAuthPublic, true},
		{"public via a read endpoint", "https://cdn.example", URLAuthPublic, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient(BucketConfig{
				Endpoint:       "https://s3.example",
				PublicEndpoint: tc.publicEndpoint,
				Bucket:         "silo",
				URLAuth:        tc.urlAuth,
			})
			if got := client.ReadURLExpires(); got != tc.want {
				t.Fatalf("ReadURLExpires() = %v, want %v", got, tc.want)
			}
		})
	}
}
