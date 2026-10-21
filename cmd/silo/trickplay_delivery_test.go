package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

func TestTrickplayDeliveryBypassesLaggingExternalEndpoint(t *testing.T) {
	for _, delivery := range []string{"local", "s3", s3client.URLAuthPublic, s3client.URLAuthCloudflareToken} {
		t.Run(delivery, func(t *testing.T) {
			const key = "trickplay/42/123/0.123.jpg"
			data := []byte{0xff, 0xd8, 0xff, 0xd9}
			storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead && r.URL.Path == "/silo" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/silo/"+key {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				w.Header().Set("ETag", `"fixture"`)
				_, _ = w.Write(data)
			}))
			t.Cleanup(storage.Close)
			lagging := httptest.NewServer(http.NotFoundHandler())
			t.Cleanup(lagging.Close)
			cfg := &config.Config{}
			cfg.Auth.JWTSecret = "fixture-signing-secret"
			cfg.S3.MetadataPresignExpiry = 2 * time.Hour
			deps := &api.Dependencies{}
			if delivery == "local" {
				cfg.Artwork.StorageBackend = "local"
				cfg.Artwork.LocalPath = t.TempDir()
			} else {
				cfg.Artwork.StorageBackend = "s3"
				s3Cfg := s3client.BucketConfig{
					Endpoint: storage.URL, Bucket: "silo", Region: "us-east-1",
					AccessKey: "fixture", SecretKey: "fixture", PathStyle: true,
				}
				if delivery != "s3" {
					s3Cfg.PublicEndpoint = lagging.URL
					s3Cfg.URLAuth = delivery
					s3Cfg.TokenSecret = "fixture-token-secret"
					s3Cfg.TokenTTL = int((2 * time.Hour).Seconds())
				}
				deps.S3Public = s3client.NewClient(s3Cfg)
			}
			if err := configureBlobStorage(t.Context(), "api", cfg, deps, nil); err != nil {
				t.Fatal(err)
			}
			if delivery == "local" {
				if err := deps.Blobs.Assets.Put(t.Context(), key, data); err != nil {
					t.Fatal(err)
				}
			}
			external := delivery != "local" && delivery != "s3"
			if deps.ArtworkDelivery.External != external {
				t.Fatalf("external delivery = %v, want %v", deps.ArtworkDelivery.External, external)
			}
			if external {
				// The ordinary artwork URL really misses while the storage API
				// already has the published sheet bytes.
				ordinary := deps.ArtworkResolver.ResolveURLs(t.Context(), []string{key})[key]
				resp, err := lagging.Client().Get(ordinary.URL)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusNotFound {
					t.Fatalf("external delivery status = %d, want 404", resp.StatusCode)
				}
			}
			resolved := trickplayURLResolver(deps).ResolveURLs(t.Context(), []string{key})[key]
			if resolved.URL == "" || resolved.ExpiresAt == nil || time.Until(*resolved.ExpiresAt) < time.Hour {
				t.Fatal("sheet URL missing or lost its configured lifetime")
			}
			u, err := url.Parse(resolved.URL)
			if err != nil {
				t.Fatal(err)
			}
			readURL := resolved.URL
			if delivery == "s3" {
				if !strings.HasPrefix(readURL, storage.URL+"/silo/") || u.Query().Get("X-Amz-Signature") == "" {
					t.Fatal("standard S3 delivery lost its direct presigned route")
				}
			} else {
				if u.IsAbs() || u.Path != "/api/v2/artwork/"+key || u.Query().Get("sig") == "" {
					t.Fatal("native sheet did not use the signed server artwork route")
				}
				server := httptest.NewServer(apiv2.NewArtworkHandler(deps.Blobs.Assets, deps.ArtworkSigner, nil))
				t.Cleanup(server.Close)
				readURL = server.URL + resolved.URL
				badQuery := u.Query()
				badQuery.Set("sig", "invalid")
				badURL := *u
				badURL.RawQuery = badQuery.Encode()
				bad, err := server.Client().Get(server.URL + badURL.String())
				if err != nil {
					t.Fatal(err)
				}
				_ = bad.Body.Close()
				if bad.StatusCode != http.StatusNotFound {
					t.Fatalf("invalid capability status = %d, want 404", bad.StatusCode)
				}
			}
			resp, err := storage.Client().Get(readURL)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(body, data) {
				t.Fatalf("native sheet status = %d, bytes = %x, error = %v", resp.StatusCode, body, err)
			}
		})
	}
}
