package api

import (
	"context"
	"os"
	"strconv"
	"time"

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
		External:   repo,
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
		d.Notifier = syncNotifier{notifier}
	}
	return subsync.NewService(d)
}

// syncNotifier hands sync job updates to the playback realtime notifier in
// the shape players receive.
type syncNotifier struct {
	*playback.SubtitleReadyNotifier
}

func (n syncNotifier) SubtitleSyncUpdated(ctx context.Context, u subsync.Update) {
	n.SubtitleReadyNotifier.SubtitleSyncUpdated(ctx, playback.SubtitleSyncUpdate{
		FileID: u.Target.MediaFileID, SubtitleID: u.Target.StoredID, SyncKey: u.Target.Key(),
		Timing: syncTimingPayload(u.Timing), Job: syncJobPayload(u.Job),
	})
}

func syncTimingPayload(t subtitles.Timing) playback.SubtitleSyncTiming {
	n := t.Normalized()
	return playback.SubtitleSyncTiming{OffsetMS: n.OffsetMS, Scale: n.Scale}
}

// syncJobPayload mirrors the native API's SubtitleSyncJobState.
func syncJobPayload(job subsync.Job) playback.SubtitleSyncJob {
	instant := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
	out := playback.SubtitleSyncJob{
		ID: strconv.FormatInt(job.ID, 10), Status: job.Status, Trigger: job.Trigger,
		Phase: job.PublicPhase(), Progress: job.PublicProgress(), Failure: job.PublicFailure(),
		Confidence: job.Confidence, CreatedAt: instant(job.CreatedAt),
	}
	if job.Result != nil {
		out.Result = new(syncTimingPayload(*job.Result))
	}
	if job.FinishedAt != nil {
		out.FinishedAt = new(instant(*job.FinishedAt))
	}
	return out
}
