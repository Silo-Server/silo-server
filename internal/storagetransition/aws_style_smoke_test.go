package storagetransition

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

// TestAWSStyleStorageTransitionSmoke is an opt-in end-to-end copy test for a
// TLS, virtual-host-style S3 endpoint. It covers pagination, a multipart-sized
// object, the fenced pass, and checkpoint-backed orphan cleanup.
func TestAWSStyleStorageTransitionSmoke(t *testing.T) {
	endpoint := os.Getenv("SILO_AWS_STYLE_SMOKE_ENDPOINT")
	bucket := os.Getenv("SILO_AWS_STYLE_SMOKE_BUCKET")
	accessKey := os.Getenv("SILO_AWS_STYLE_SMOKE_ACCESS_KEY")
	secretKey := os.Getenv("SILO_AWS_STYLE_SMOKE_SECRET_KEY")
	if endpoint == "" {
		t.Skip("SILO_AWS_STYLE_SMOKE_ENDPOINT is not set")
	}
	if bucket == "" || accessKey == "" || secretKey == "" {
		t.Fatal("bucket and credentials are required")
	}
	region := os.Getenv("SILO_AWS_STYLE_SMOKE_REGION")
	root := "qa/storage-transition-" + uuid.NewString()
	client := func(role, prefix string) *s3client.Client {
		return s3client.NewClient(s3client.BucketConfig{Role: role, Endpoint: endpoint, Region: region, Bucket: bucket, KeyPrefix: prefix, AccessKey: accessKey, SecretKey: secretKey, PathStyle: false})
	}
	sourceClient := client("qa-transition-source", root+"/source")
	targetClient := client("qa-transition-target", root+"/target")
	source := blobstore.NewS3(sourceClient)
	target := blobstore.NewS3(targetClient)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_, _ = sourceClient.DeletePrefix(cleanupCtx, bucket, "")
		_, _ = targetClient.DeletePrefix(cleanupCtx, bucket, "")
	})

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	for i := range 251 {
		if err := source.Put(ctx, fmt.Sprintf("tmdb/%03d.webp", i), []byte(fmt.Sprintf("image-%03d", i))); err != nil {
			t.Fatalf("seed paged object %d: %v", i, err)
		}
	}
	if err := source.Put(ctx, "tmdb/large.webp", bytes.Repeat([]byte("m"), 9*1024*1024)); err != nil {
		t.Fatalf("seed multipart-sized object: %v", err)
	}
	if err := target.Put(ctx, "unrelated/keep.webp", []byte("keep")); err != nil {
		t.Fatal(err)
	}

	service := New(nil, &memorySettings{values: map[string]string{}}, nil, source, nil)
	runID := uuid.NewString()
	listings := map[string]objectListing{}
	if _, _, _, err := service.copyPrefixPass(ctx, "aws-style", "public:", source, target, "", func(int, int, string) {}, 0, runID, listings, false); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Delete(ctx, []string{"tmdb/000.webp"}); err != nil {
		t.Fatal(err)
	}
	release, err := source.BeginMutationFence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, _, _, err := service.copyPrefixPass(ctx, "aws-style", "public:", source, target, "", func(int, int, string) {}, 0, runID, listings, true); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Stat(ctx, "tmdb/000.webp"); err == nil {
		t.Fatal("fenced orphan cleanup retained the deleted source object")
	}
	if _, err := target.Stat(ctx, "tmdb/large.webp"); err != nil {
		t.Fatalf("multipart-sized object missing from target: %v", err)
	}
	if _, err := target.Stat(ctx, "unrelated/keep.webp"); err != nil {
		t.Fatalf("unrelated target object was removed: %v", err)
	}
}
