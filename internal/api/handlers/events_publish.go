package handlers

import (
	"context"
	"log/slog"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
)

func publishEventJob(ctx context.Context, hub *evt.Hub, eventName string, job *models.AdminJob) {
	if hub == nil || job == nil {
		return
	}
	if err := hub.PublishJSON(ctx, evt.ChannelJobs, eventName, job, evt.PublishOptions{
		AdminOnly: true,
	}); err != nil {
		slog.WarnContext(ctx, "events: failed to publish job event", "component", "api", "job_id", job.ID, "event", eventName, "error", err)
	}
}

func publishEventMetadataUpdate(ctx context.Context, hub *evt.Hub, libraryID int, contentID string) {
	if hub == nil {
		return
	}
	if err := hub.PublishJSON(ctx, evt.ChannelCatalog, "catalog.item.changed", map[string]any{
		"library_id": libraryID,
		"content_id": contentID,
		"change":     "metadata_updated",
	}, evt.PublishOptions{}); err != nil {
		slog.WarnContext(ctx, "events: failed to publish metadata update", "component", "api", "library_id", libraryID, "content_id", contentID, "error", err)
	}
}
