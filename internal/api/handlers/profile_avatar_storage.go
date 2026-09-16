package handlers

import (
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

// NewProfileAvatarStore keeps existing and new avatar keys in private S3 when
// configured. Standalone installations store avatars locally and sign delivery
// URLs through the artwork resolver. Public artwork S3 is never eligible.
func NewProfileAvatarStore(artwork artworkstore.Store, private *s3client.Client, backend string) artworkstore.Store {
	if private != nil {
		return artworkstore.NewS3(private)
	}
	if backend == artworkstore.BackendLocal {
		return artwork
	}
	return nil
}
