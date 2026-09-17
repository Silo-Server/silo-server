package handlers

import (
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

// NewProfileAvatarStore keeps existing and new avatar keys in private S3 when
// configured. Standalone installations store avatars locally and sign delivery
// URLs through the artwork resolver. Public artwork S3 is never eligible.
func NewProfileAvatarStore(artwork blobstore.Store, private *s3client.Client, backend string) blobstore.Store {
	if private != nil {
		return blobstore.NewS3(private)
	}
	if backend == blobstore.BackendLocal {
		return artwork
	}
	return nil
}
