package trickplay

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/blobgc"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
)

// PrivateStorageSetting keeps sheets in the private bucket instead of the
// assets store, for deployments whose public bucket bills requests. It is
// read at startup, where the stores are chosen.
const PrivateStorageSetting = config.TrickplayPrivateStorageSettingKey

// KeyRoot is the prefix of every sheet key, in whichever store holds them.
const KeyRoot = keyRoot

// StoreFor returns the store sheets are written to and read from: the
// operational store when private is set and a private bucket gives one
// separate from the assets store, otherwise the assets store. A change of
// store reads as a move: sheets published to the other store stop serving
// and are made again here (see Repository.Reconcile).
func StoreFor(stores blobstore.Stores, private bool) blobstore.Store {
	if private && stores.Operational != nil && stores.Operational != stores.Assets {
		return stores.Operational
	}
	return stores.Assets
}

// BlobNamespaceFor is BlobNamespace for sheets kept in sheets. With a
// private bucket configured, sheets published before the setting last
// changed sit in the other store until they are made again, and their
// revisions are queued like any other. Deletes therefore go to both stores;
// the sweep lists the store new sheets go to.
func BlobNamespaceFor(stores blobstore.Stores, sheets blobstore.Store) blobgc.Namespace {
	ns := BlobNamespace()
	other := otherStore(stores, sheets)
	if sheets == nil || other == nil || other == sheets {
		if sheets != stores.Assets {
			ns.Store = sheets
		}
		return ns
	}
	ns.Store = bothStores{current: sheets, previous: other}
	return ns
}

// ReadStoreFor is the store the signed artwork route reads sheets from:
// sheets, then the other store. After the setting changes, a published
// revision stays in the other store until it is made again and the URLs
// issued for it expire (see queueRevision), so a player that loaded its
// manifest before the restart keeps its previews.
func ReadStoreFor(stores blobstore.Stores, sheets blobstore.Store) blobstore.Store {
	return blobstore.Fallback(sheets, otherStore(stores, sheets))
}

// otherStore is the store sheets are not written to, or nil without one.
func otherStore(stores blobstore.Stores, sheets blobstore.Store) blobstore.Store {
	if sheets == stores.Operational {
		return stores.Assets
	}
	return stores.Operational
}

// bothStores deletes from the current store and the previous one, and reads
// and lists the current one.
type bothStores struct {
	current, previous blobgc.Store
}

func (s bothStores) Delete(ctx context.Context, keys []string) (int, error) {
	n, err := s.current.Delete(ctx, keys)
	if err != nil {
		return n, err
	}
	_, err = s.previous.Delete(ctx, keys)
	return n, err
}

func (s bothStores) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	n, err := s.current.DeletePrefix(ctx, prefix)
	if err != nil {
		return n, err
	}
	m, err := s.previous.DeletePrefix(ctx, prefix)
	return n + m, err
}

func (s bothStores) Stat(ctx context.Context, key string) (blobstore.ObjectInfo, error) {
	info, err := s.current.Stat(ctx, key)
	if errors.Is(err, blobstore.ErrNotFound) {
		return s.previous.Stat(ctx, key)
	}
	return info, err
}

func (s bothStores) List(ctx context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error) {
	return s.current.List(ctx, prefix, cursor, limit)
}
