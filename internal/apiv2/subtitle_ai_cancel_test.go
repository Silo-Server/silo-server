package apiv2

import (
	"context"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeSubtitleAICancel struct {
	calls   int
	id      int64
	filter  catalogpkg.AccessFilter
	failure error
}

func (f *fakeSubtitleAICancel) CancelSubtitleAIJob(_ context.Context, filter catalogpkg.AccessFilter, id int64) error {
	f.calls++
	f.id = id
	f.filter = filter
	return f.failure
}
