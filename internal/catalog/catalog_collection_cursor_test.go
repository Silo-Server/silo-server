package catalog

import (
	"context"
	"errors"
	"testing"
)

func TestCollectionCursorFence(t *testing.T) {
	req := CatalogRequest{CollectionID: "selected"}
	revision := int64(7)
	read := func(context.Context, string) (int64, error) { return revision, nil }
	current, err := collectionFence(t.Context(), req, read)
	if err != nil || current != 7 {
		t.Fatalf("initial revision %d: %v", current, err)
	}
	result, err := finishCollectionCursor(t.Context(), req, current, read, &CatalogResult{}, nil)
	if err != nil || result.CursorScope == nil || result.CursorScope.Collection.Revision != 7 {
		t.Fatalf("missing seed on final page: %+v %v", result, err)
	}
	req.After = result.CursorScope
	if _, err := collectionFence(t.Context(), req, read); err != nil {
		t.Fatal(err)
	}
	revision++
	if _, err := collectionFence(t.Context(), req, read); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("continued changed collection: %v", err)
	}
	if _, err := finishCollectionCursor(t.Context(), req, current, read, &CatalogResult{}, nil); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("missed mutation during page: %v", err)
	}
	req.After = &QueryCursor{Collection: &CollectionCursor{ID: "other", Revision: revision}}
	if _, err := collectionFence(t.Context(), req, read); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("accepted another collection: %v", err)
	}
}

func TestCollectionCursorRecheckPreservesFailure(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("database unavailable")} {
		read := func(context.Context, string) (int64, error) { return 0, failure }
		_, err := finishCollectionCursor(t.Context(), CatalogRequest{CollectionID: "selected"}, 7, read, &CatalogResult{}, nil)
		if !errors.Is(err, failure) {
			t.Fatalf("replaced authoritative-read error %v with %v", failure, err)
		}
	}
}
