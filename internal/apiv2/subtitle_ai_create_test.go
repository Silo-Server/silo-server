package apiv2

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

type fakeSubtitleAICreate struct {
	calls   int
	command handlers.SubtitleAICreateCommand
	filter  catalogpkg.AccessFilter
	failure error
}

func (f *fakeSubtitleAICreate) CreateSubtitleAIJob(_ context.Context, filter catalogpkg.AccessFilter, command handlers.SubtitleAICreateCommand) (handlers.SubtitleAICreateResult, error) {
	f.calls++
	f.command = command
	f.filter = filter
	return handlers.SubtitleAICreateResult{Job: &ai.Job{ID: 9007199254740993, MediaFileID: command.MediaFileID, Kind: command.Kind, SourceIndex: command.SourceIndex, SourceLanguage: command.SourceLanguage, TargetLanguage: command.TargetLanguage, CreatedAt: time.Unix(100, 0), UpdatedAt: time.Unix(100, 0), Status: ai.JobStatusPending}, LiveDeliveryAttached: true}, f.failure
}
