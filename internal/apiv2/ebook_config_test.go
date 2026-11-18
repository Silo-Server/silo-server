package apiv2

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeEbookConfig struct {
	current *handlers.EbookReaderConfig
	calls   int
}

func (f *fakeEbookConfig) ReaderConfig(context.Context, int, string, string, catalogpkg.AccessFilter) (*handlers.EbookReaderConfig, error) {
	return f.current, nil
}
func (f *fakeEbookConfig) SaveReaderConfig(_ context.Context, config handlers.EbookReaderConfig, _ catalogpkg.AccessFilter, guard handlers.EbookConfigGuard) (*handlers.EbookReaderConfig, error) {
	f.calls++
	if err := guard(f.current); err != nil {
		return nil, err
	}
	config.UpdatedAt = time.Date(2026, 1, 2, 3, 4, 5, f.calls*1000000, time.UTC)
	f.current = &config
	return f.current, nil
}
