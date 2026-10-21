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
