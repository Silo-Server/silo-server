package access

import (
	"context"
	"errors"
)

// ErrMetadataCurationScope is a policy refusal for an item or selected files.
var ErrMetadataCurationScope = errors.New("item is outside metadata curation scope")

type metadataCurationKey struct{}

// MetadataCurationAuthorizer evaluates the request's curation policy against
// one item's complete current library set. An existing item may have no libraries.
type MetadataCurationAuthorizer func(context.Context, []int) error

// WithMetadataCurationAuthorizer carries explicit curation authority into the
// repository transactions that discover additional destinations. Automatic
// scanner/provider work does not install this authority.
func WithMetadataCurationAuthorizer(ctx context.Context, authorize MetadataCurationAuthorizer) context.Context {
	return context.WithValue(ctx, metadataCurationKey{}, authorize)
}

func HasMetadataCurationAuthorizer(ctx context.Context) bool {
	authorize, _ := ctx.Value(metadataCurationKey{}).(MetadataCurationAuthorizer)
	return authorize != nil
}

func AuthorizeMetadataCurationLibraries(ctx context.Context, libraries []int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if authorize, _ := ctx.Value(metadataCurationKey{}).(MetadataCurationAuthorizer); authorize != nil {
		return authorize(ctx, libraries)
	}
	return nil
}

type metadataCurationScopeKey struct{}

// MetadataCurationScope is the library scope a delegated curator was granted
// for one request. It is plain data so work the request queues for later, such
// as an item refresh job that may run on another node, can carry it and hold
// that work to the same libraries.
type MetadataCurationScope struct {
	// LibraryIDs are the curator's assigned libraries. Nil means every library.
	LibraryIDs []int `json:"library_ids"`
}

// Authorizer allows a non-empty library set inside the scope, the same rule the
// legacy curation gate applies to a request.
func (s MetadataCurationScope) Authorizer() MetadataCurationAuthorizer {
	allowed := make(map[int]struct{}, len(s.LibraryIDs))
	for _, id := range s.LibraryIDs {
		allowed[id] = struct{}{}
	}
	restricted := s.LibraryIDs != nil
	return func(ctx context.Context, libraries []int) error {
		if len(libraries) == 0 {
			return ErrMetadataCurationScope
		}
		if restricted {
			for _, id := range libraries {
				if _, ok := allowed[id]; !ok {
					return ErrMetadataCurationScope
				}
			}
		}
		return ctx.Err()
	}
}

// WithMetadataCurationScope records the scope of a delegated curator's request.
// Acting admins carry no scope: their deferred work stays automatic.
func WithMetadataCurationScope(ctx context.Context, scope MetadataCurationScope) context.Context {
	return context.WithValue(ctx, metadataCurationScopeKey{}, scope)
}

// MetadataCurationScopeFrom returns the delegated curator scope recorded on ctx.
func MetadataCurationScopeFrom(ctx context.Context) (MetadataCurationScope, bool) {
	scope, ok := ctx.Value(metadataCurationScopeKey{}).(MetadataCurationScope)
	return scope, ok
}
