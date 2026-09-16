package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

func TestProfileAvatarStorePreservesPrivateBucketWithoutLookup(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPut || !strings.HasPrefix(r.URL.Path, "/private/profile-avatars/") {
			t.Errorf("unexpected storage request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	private := s3client.NewClient(s3client.BucketConfig{Endpoint: server.URL, Bucket: "private", PathStyle: true, AccessKey: "test", SecretKey: "test"})
	public := artworkstore.NewS3(s3client.NewClient(s3client.BucketConfig{Endpoint: server.URL, Bucket: "public", PathStyle: true, AccessKey: "test", SecretKey: "test"}))
	local, err := artworkstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, artwork := range []artworkstore.Store{public, local, nil} {
		store := NewProfileAvatarStore(artwork, private, "")
		before := requests.Load()
		kind, url := resolveProfileAvatar(context.Background(), store, time.Minute, "upload:profile-avatars/1/main/original.webp")
		if kind != "upload" || !strings.Contains(url, "/private/profile-avatars/") || !strings.Contains(url, "X-Amz-Signature=") {
			t.Fatalf("avatar URL: %s %s", kind, url)
		}
		if requests.Load() != before {
			t.Fatal("signing performed a storage lookup")
		}
		if err := store.Put(context.Background(), "profile-avatars/1/main/w256.webp", []byte("avatar")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProfileAvatarStoreAllowsOnlyLocalWithoutPrivateS3(t *testing.T) {
	local, backend, err := artworkstore.Open(context.Background(), artworkstore.Options{Backend: artworkstore.BackendLocal, LocalPath: t.TempDir(), Settings: avatarStorageSettings{}})
	if err != nil {
		t.Fatal(err)
	}
	if backend != artworkstore.BackendLocal {
		t.Fatal(backend)
	}
	store := NewProfileAvatarStore(local, nil, artworkstore.BackendLocal)
	if store != local {
		t.Fatal("local storage not selected")
	}
	if _, ok := store.(artworkstore.DirectURLer); ok {
		t.Fatal("local storage advertises direct URLs")
	}
	signer := artworkurl.NewSigner("test-secret", time.Minute)
	_, rawURL := resolveProfileAvatar(context.Background(), store, time.Minute, "upload:profile-avatars/1/main/original.webp", artworkurl.NewServerResolver(signer))
	signed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := strconv.ParseInt(signed.Query().Get("exp"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Verify(strings.TrimPrefix(signed.Path, "/api/v2/artwork/"), exp, signed.Query().Get("sig"), time.Now()); err != nil {
		t.Fatal(err)
	}
	public := artworkstore.NewS3(&s3client.Client{})
	if NewProfileAvatarStore(public, nil, artworkstore.BackendS3) != nil {
		t.Fatal("public S3 accepted for avatars")
	}
	if NewProfileAvatarStore(nil, nil, "") != nil {
		t.Fatal("missing storage accepted")
	}
}

type avatarStorageSettings struct{}

func (avatarStorageSettings) Get(context.Context, string) (string, error) { return "", nil }
func (avatarStorageSettings) SetIfAbsent(context.Context, string, string) (bool, error) {
	return true, nil
}
