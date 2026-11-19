package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeEbookProgress struct {
	saved  *handlers.EbookReaderProgress
	filter catalogpkg.AccessFilter
	calls  int
}

func (f *fakeEbookProgress) ReaderProgress(_ context.Context, userID int, profileID, contentID string, filter catalogpkg.AccessFilter) (*handlers.EbookReaderProgress, error) {
	f.calls++
	f.filter = filter
	return f.saved, nil
}
func (f *fakeEbookProgress) SaveReaderProgress(_ context.Context, progress handlers.EbookReaderProgress, filter catalogpkg.AccessFilter) (*handlers.EbookReaderProgress, error) {
	f.calls++
	f.filter = filter
	f.saved = &progress
	return f.saved, nil
}

func (f *fakeEbookProgress) ReaderCapability(context.Context) handlers.EbookReaderCapability {
	return handlers.EbookReaderCapability{Progress: true}
}
