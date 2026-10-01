package handlers

import "github.com/Silo-Server/silo-server/internal/blobstore"

// NewProfileAvatarStore returns the store avatars live in, which is the
// operational store: private S3 when configured, preserving existing keys and
// presigned delivery, and otherwise the local root with signed delivery through
// the artwork resolver. Public artwork S3 is never eligible, and an S3
// deployment without a private bucket has nowhere to put avatars, so this
// returns nil and uploads stay unavailable.
func NewProfileAvatarStore(stores blobstore.Stores) blobstore.Store {
	return stores.Operational
}
