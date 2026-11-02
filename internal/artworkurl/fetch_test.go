package artworkurl

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/netguard"
)

func TestFetchContextTrustsOnlyTheStoresDirectURLs(t *testing.T) {
	store := blobstoretest.NewDirect("http://media.minio.lan:9000/artwork")
	for _, tc := range []struct {
		name  string
		store blobstore.Store
		url   string
		want  bool
	}{
		{"store URL", store, "http://media.minio.lan:9000/artwork/tmdb/movies/1/poster/w500.webp?X-Amz-Signature=x", true},
		{"store host in another case", store, "HTTP://Media.MinIO.lan:9000/artwork/key", true},
		{"other bucket on the store host", store, "http://media.minio.lan:9000/other-bucket/key", false},
		{"bucket name as a longer prefix", store, "http://media.minio.lan:9000/artwork-admin/key", false},
		{"dot segments out of the bucket", store, "http://media.minio.lan:9000/artwork/../minio/admin", false},
		{"encoded dot segments", store, "http://media.minio.lan:9000/artwork/%2e%2e/minio/admin", false},
		{"other port", store, "http://media.minio.lan:9001/artwork/key", false},
		{"other scheme", store, "https://media.minio.lan:9000/artwork/key", false},
		{"other host", store, "http://10.0.0.5:9000/artwork/key", false},
		{"relative URL", store, "/api/v2/artwork/key?sig=x", false},
		{"store without direct URLs", blobstoretest.New(), "http://media.minio.lan:9000/artwork/key", false},
		// Unicode case folding matches these, but they resolve as other names.
		{"final sigma for sigma", blobstoretest.NewDirect("http://σ.example.test:8080/artwork"), "http://ς.example.test:8080/artwork/key", false},
		{"Kelvin sign for k", blobstoretest.NewDirect("http://k.example.test:8080/artwork"), "http://\u212a.example.test:8080/artwork/key", false},
		{"same internationalized host", blobstoretest.NewDirect("http://σ.example.test:8080/artwork"), "http://σ.example.test:8080/artwork/key", true},
		{"no store", nil, "http://media.minio.lan:9000/artwork/key", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := FetchContext(context.Background(), tc.store, tc.url)
			if got := netguard.PrivateAccess(ctx); got != tc.want {
				t.Fatalf("private access = %v, want %v", got, tc.want)
			}
		})
	}
}
