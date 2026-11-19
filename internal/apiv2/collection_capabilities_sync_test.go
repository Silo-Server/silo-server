package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (f *fakeAdminCollections) AdminCollectionFeatures(context.Context) userstore.CollectionFeatures {
	f.featureReads++
	return userstore.CollectionFeatures{Groups: true, Imports: true, Artwork: true, ItemReorder: true}
}

func (f *fakeCollectionImports) MDBListConfigured() bool { return f.configured }
