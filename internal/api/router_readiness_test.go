package api

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

func TestReadinessS3CheckerExcludesCanonicalArtworkBucket(t *testing.T) {
	public := s3client.NewClient(s3client.BucketConfig{Bucket: "public"})
	private := s3client.NewClient(s3client.BucketConfig{Bucket: "private"})

	tests := []struct {
		name    string
		deps    Dependencies
		want    *s3client.Client
		wantNil bool
	}{
		{
			name: "public bucket stays fatal when local artwork is canonical",
			deps: Dependencies{S3Public: public, ArtworkStore: &artworkstore.Handle{Backend: artworkstore.BackendLocal}},
			want: public,
		},
		{
			name:    "canonical public artwork bucket is degradable",
			deps:    Dependencies{S3Public: public, ArtworkStore: &artworkstore.Handle{Backend: artworkstore.BackendS3}},
			wantNil: true,
		},
		{
			name: "private bucket remains fatal when public artwork is canonical",
			deps: Dependencies{S3Public: public, S3Private: private, ArtworkStore: &artworkstore.Handle{Backend: artworkstore.BackendS3}},
			want: private,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := readinessS3Checker(test.deps)
			if test.wantNil {
				if got != nil {
					t.Fatalf("readiness S3 checker = %v, want nil", got)
				}
				return
			}
			if got != test.want {
				t.Fatalf("readiness S3 checker = %v, want %v", got, test.want)
			}
		})
	}
}
