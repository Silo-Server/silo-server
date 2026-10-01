package trickplay

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

type retentionURLs struct {
	base   time.Time
	before func()
}

func (u retentionURLs) ResolveURLs(_ context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	if u.before != nil {
		u.before()
	}
	resolved := make(map[string]catalog.ResolvedImageURL, len(keys))
	for i, key := range keys {
		expiry := u.base.Add(time.Duration(i) * time.Hour)
		resolved[key] = catalog.ResolvedImageURL{URL: "https://cdn.example/" + key, ExpiresAt: &expiry}
	}
	return resolved
}

func TestReaderProtectsLatestSheetExpiryDB(t *testing.T) {
	f := newFixture(t)
	file := f.file(t, f.library(t, "movies", true), "different-expiries")
	f.reconcile(t)
	f.generate(t, file, "server")
	base := time.Now().Add(72 * time.Hour).Truncate(time.Microsecond)
	reader := NewReader(f.pool, identityStore(testStore), retentionURLs{base: base})
	signed, ok, err := reader.SignedManifest(t.Context(), file)
	if err != nil || !ok || !signed.ExpiresAt.Equal(base) {
		t.Fatalf("manifest earliest expiry: %+v %v %v", signed, ok, err)
	}
	var savedExpiry time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT published_expires_at FROM public.media_file_trickplay WHERE media_file_id = $1`, file).Scan(&savedExpiry); err != nil {
		t.Fatal(err)
	}
	if expected := base.Add(time.Duration(len(signed.SheetURLs)-1) * time.Hour); !savedExpiry.Equal(expected) {
		t.Fatalf("protection expires at %v, want latest sheet expiry %v", savedExpiry, expected)
	}
}

func TestReaderWithholdsRevisionDisplacedWhileSigningDB(t *testing.T) {
	for _, retirement := range []string{"regenerate", "delete"} {
		t.Run(retirement, func(t *testing.T) {
			f := newFixture(t)
			file := f.file(t, f.library(t, "movies", true), retirement)
			f.reconcile(t)
			f.generate(t, file, "server")
			urls := retentionURLs{base: time.Now().Add(72 * time.Hour), before: func() {
				if retirement == "delete" {
					f.exec(t, `DELETE FROM public.media_files WHERE id = $1`, file)
					return
				}
				if _, err := f.repo.Regenerate(t.Context(), []int{file}); err != nil {
					t.Fatal(err)
				}
				f.generate(t, file, "server")
			}}
			reader := NewReader(f.pool, identityStore(testStore), urls)
			if signed, ok, err := reader.SignedManifest(t.Context(), file); err != nil || ok {
				t.Fatalf("displaced manifest returned: %+v %v %v", signed, ok, err)
			}
		})
	}
}

func TestRetiredTrickplayOutlivesIssuedURLsDB(t *testing.T) {
	const metadataLifetime = 72 * time.Hour
	cfg := s3client.BucketConfig{Endpoint: "https://s3.example.test", Region: "us-east-1", Bucket: "silo", AccessKey: "k", SecretKey: "s", PathStyle: true}
	tokenCfg := cfg
	tokenCfg.PublicEndpoint, tokenCfg.URLAuth = "https://cdn.example.test", s3client.URLAuthCloudflareToken
	tokenCfg.TokenSecret, tokenCfg.TokenTTL = "secret", int((96 * time.Hour).Seconds())
	s3Store, tokenStore := blobstore.NewS3(s3client.NewClient(cfg)), blobstore.NewS3(s3client.NewClient(tokenCfg))
	for _, delivery := range []struct {
		name     string
		resolver artworkurl.Resolver
	}{
		{"s3", artworkurl.NewDirectResolver(s3Store, metadataLifetime)},
		{"cloudflare token", artworkurl.NewDirectResolver(tokenStore, metadataLifetime)},
	} {
		for _, retirement := range []string{"regenerate", "opt-out", "delete"} {
			t.Run(delivery.name+"/"+retirement, func(t *testing.T) {
				f := newFixture(t)
				folder := f.library(t, "movies", true)
				file := f.file(t, folder, retirement)
				f.reconcile(t)
				revision := f.generate(t, file, "server")
				reader := NewReader(f.pool, identityStore(testStore), delivery.resolver)
				signed, ok, err := reader.SignedManifest(t.Context(), file)
				if err != nil || !ok || signed.ExpiresAt.IsZero() {
					t.Fatalf("manifest: %+v %v %v", signed, ok, err)
				}
				switch retirement {
				case "regenerate":
					if _, err := f.repo.Regenerate(t.Context(), []int{file}); err != nil {
						t.Fatal(err)
					}
					f.generate(t, file, "server")
				case "opt-out":
					f.exec(t, `UPDATE public.media_folders SET trickplay_enabled = false WHERE id = $1`, folder)
					f.reconcile(t)
				case "delete":
					f.exec(t, `DELETE FROM public.media_files WHERE id = $1`, file)
				}
				var notBefore time.Time
				if err := f.pool.QueryRow(t.Context(), `SELECT not_before FROM public.blob_gc_queue WHERE prefix = $1`, revisionPrefix(file, revision)).Scan(&notBefore); err != nil {
					t.Fatal(err)
				}
				if notBefore.Before(signed.ExpiresAt) {
					t.Fatalf("revision retires at %v while issued URLs expire at %v", notBefore, signed.ExpiresAt)
				}
			})
		}
	}
}
