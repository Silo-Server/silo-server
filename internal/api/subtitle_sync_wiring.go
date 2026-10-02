package api

import (
	"os"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsync"
)

// newSubtitleSyncService wires subtitle sync. Speech decoding runs on
// transcode nodes when the pool has any (see subtitles.sync_execution).
func newSubtitleSyncService(
	deps *Dependencies,
	manager *subtitles.Manager,
	repo *subtitles.PgRepository,
	settings catalog.SettingsStore,
	notifier *playback.SubtitleReadyNotifier,
) *subsync.Service {
	node, _ := os.Hostname()
	d := subsync.Deps{
		AppContext: deps.AppContext,
		Jobs:       subsync.NewStore(deps.DB),
		Subtitles:  manager,
		Rows:       repo,
		Files:      deps.FileRepo,
		Artifacts:  mediaartifact.NewStore(deps.DB),
		Settings:   settings,
		FFmpegPath: func() string { return deps.CurrentConfig().Playback.FFmpegPath },
		Node:       node,
	}
	if deps.TranscodePool != nil {
		d.Nodes = deps.TranscodePool
	}
	if notifier != nil {
		d.Notifier = notifier
	}
	return subsync.NewService(d)
}
