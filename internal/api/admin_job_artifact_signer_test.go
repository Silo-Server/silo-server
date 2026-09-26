package api

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

// The signer gates the streaming artifact route and, through it, the
// artifact_download job capability. It must exist only where a local
// operational store actually holds artifacts.
func TestAdminJobArtifactSignerOnlyForLocalOperationalStore(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	local := blobstoretest.New()

	for name, tc := range map[string]struct {
		deps Dependencies
		want bool
	}{
		"local operational store": {Dependencies{Config: cfg, Blobs: blobstore.Stores{Assets: local, Operational: local}}, true},
		"private s3 presigns":     {Dependencies{Config: cfg, S3Private: &s3client.Client{}, Blobs: blobstore.Stores{Operational: local}}, false},
		"s3 assets, no private":   {Dependencies{Config: cfg, Blobs: blobstore.Stores{Assets: local}}, false},
		"no config":               {Dependencies{Blobs: blobstore.Stores{Operational: local}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := newAdminJobArtifactSigner(&tc.deps) != nil; got != tc.want {
				t.Fatalf("signer present = %v, want %v", got, tc.want)
			}
		})
	}
}
