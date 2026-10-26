// Package blobgc deletes derived objects that nothing references any more:
// images the server generates from media files, such as chapter thumbnails,
// whose rows are gone. It never touches artwork, which the artwork revision
// collector and storage sweep in internal/metadata own.
//
// Work flows through the blob_gc_queue table. Database triggers queue the
// prefix of each deleted media file's images, so every deletion path is
// covered, including cascades. The Collector deletes due prefixes. The
// Sweeper walks storage for objects whose rows vanished without being queued
// (before the triggers existed, or when a write outlived its row) and queues
// them too.
//
// Each namespace's owner decides how its keys group into deletable prefixes
// and which prefixes are still live; blobgc deletes only prefixes that a
// namespace claims and reports dead.
package blobgc

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// Querier is the query surface of a collector transaction or a sweep's locked
// connection. Namespace liveness checks must use it without acquiring another
// connection from the pool.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Namespace is a key namespace whose objects blobgc may delete.
type Namespace struct {
	// Root is the namespace's key prefix, such as "chapter-images/".
	Root string
	// Group returns the deletable prefix an object key belongs to, such as
	// "chapter-images/42/", or false for a key the namespace does not
	// recognize, which is never deleted. Directory groups end in "/";
	// individual objects return their exact key without a trailing slash.
	Group func(key string) (string, bool)
	// Live reports which of prefixes something still references. An error
	// stops the collector and the sweep: neither deletes on a guess.
	Live func(ctx context.Context, db Querier, prefixes []string) (map[string]bool, error)
}

// Store is the storage surface blobgc needs.
type Store interface {
	Delete(ctx context.Context, keys []string) (int, error)
	Stat(ctx context.Context, key string) (blobstore.ObjectInfo, error)
	DeletePrefix(ctx context.Context, prefix string) (int, error)
	List(ctx context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error)
}

// namespaceFor returns the namespace that owns prefix.
func namespaceFor(namespaces []Namespace, prefix string) (Namespace, bool) {
	for _, ns := range namespaces {
		if strings.HasPrefix(prefix, ns.Root) {
			if group, ok := ns.Group(prefix); ok && group == prefix {
				return ns, true
			}
		}
	}
	return Namespace{}, false
}
