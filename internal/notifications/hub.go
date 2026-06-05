package notifications

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
)

type Type string

const (
	TypeCatalogLibraryChanged Type = "catalog.library.changed"
	TypeCatalogItemChanged    Type = "catalog.item.changed"

	TypeLibraryChanged   Type = "library.changed"    // legacy catalog event name
	TypeLibraryItemAdded Type = "library.item_added" // legacy catalog event name
	TypeMetadataUpdated  Type = "metadata.updated"   // legacy catalog event name
	TypeJobCreated       Type = "job.created"
	TypeJobProgress      Type = "job.progress"
	TypeJobCompleted     Type = "job.completed"
	TypeJobFailed        Type = "job.failed"
	TypeJobCancelled     Type = "job.cancelled"
)

type Envelope struct {
	Type                   Type             `json:"type"`
	Timestamp              time.Time        `json:"timestamp"`
	SourceID               string           `json:"source_id,omitempty"`
	AdminOnly              bool             `json:"admin_only,omitempty"`
	LibraryID              int              `json:"library_id,omitempty"`
	ContentID              string           `json:"content_id,omitempty"`
	Reason                 string           `json:"reason,omitempty"`
	Change                 string           `json:"change,omitempty"`
	New                    int              `json:"new,omitempty"`
	Updated                int              `json:"updated,omitempty"`
	Missing                int              `json:"missing,omitempty"`
	MatchedFiles           int              `json:"matched_files,omitempty"`
	RetriedItems           int              `json:"retried_items,omitempty"`
	StillUnmatchedWarnings int              `json:"still_unmatched_warnings,omitempty"`
	Job                    *models.AdminJob `json:"job,omitempty"`
}

type LibraryChangeEvent struct {
	LibraryID              int
	Reason                 string
	New                    int
	Updated                int
	Missing                int
	MatchedFiles           int
	RetriedItems           int
	StillUnmatchedWarnings int
}

type MetadataUpdateEvent struct {
	LibraryID int
	ContentID string
	Change    string
}

type Hub struct {
	inner *evt.Hub
}

func NewHub(sourceID string, eventBus cache.EventBus) *Hub {
	return &Hub{inner: evt.NewHub(sourceID, eventBus)}
}

func (h *Hub) Start(ctx context.Context) error {
	if h == nil || h.inner == nil {
		return nil
	}
	return h.inner.Start(ctx)
}

func (h *Hub) Subscribe() (<-chan Envelope, func()) {
	if h == nil || h.inner == nil {
		ch := make(chan Envelope)
		close(ch)
		return ch, func() {}
	}

	innerCh, unsubscribe := h.inner.Subscribe()
	out := make(chan Envelope, 32)
	go func() {
		defer close(out)
		for env := range innerCh {
			converted, ok := convertEnvelope(env)
			if !ok {
				continue
			}
			out <- converted
		}
	}()
	return out, unsubscribe
}

func (h *Hub) PublishLibraryChanged(ctx context.Context, event LibraryChangeEvent) error {
	if h == nil || h.inner == nil {
		return nil
	}

	payload := libraryChangePayload(event)
	if err := h.inner.PublishJSON(ctx, evt.ChannelCatalog, string(TypeLibraryChanged), payload, evt.PublishOptions{}); err != nil {
		return err
	}
	if event.New <= 0 {
		return nil
	}
	return h.inner.PublishJSON(ctx, evt.ChannelCatalog, string(TypeLibraryItemAdded), map[string]any{
		"library_id": event.LibraryID,
		"new":        event.New,
	}, evt.PublishOptions{})
}

func (h *Hub) PublishCatalogLibraryChanged(ctx context.Context, event LibraryChangeEvent) error {
	if h == nil || h.inner == nil {
		return nil
	}
	if err := h.inner.PublishJSON(ctx, evt.ChannelCatalog, string(TypeCatalogLibraryChanged), libraryChangePayload(event), evt.PublishOptions{}); err != nil {
		return err
	}
	return h.PublishLibraryChanged(ctx, event)
}

func (h *Hub) PublishMetadataUpdated(ctx context.Context, event MetadataUpdateEvent) error {
	if h == nil || h.inner == nil {
		return nil
	}
	return h.inner.PublishJSON(ctx, evt.ChannelCatalog, string(TypeMetadataUpdated), map[string]any{
		"library_id": event.LibraryID,
		"content_id": event.ContentID,
	}, evt.PublishOptions{})
}

func (h *Hub) PublishCatalogItemChanged(ctx context.Context, event MetadataUpdateEvent) error {
	if h == nil || h.inner == nil {
		return nil
	}
	change := event.Change
	if change == "" {
		change = "metadata_updated"
	}
	if err := h.inner.PublishJSON(ctx, evt.ChannelCatalog, string(TypeCatalogItemChanged), map[string]any{
		"library_id": event.LibraryID,
		"content_id": event.ContentID,
		"change":     change,
	}, evt.PublishOptions{}); err != nil {
		return err
	}
	return h.PublishMetadataUpdated(ctx, event)
}

func (h *Hub) PublishJob(ctx context.Context, eventType Type, job *models.AdminJob) error {
	if h == nil || h.inner == nil || job == nil {
		return nil
	}
	return h.inner.PublishJSON(ctx, evt.ChannelJobs, string(eventType), job, evt.PublishOptions{
		AdminOnly: true,
	})
}

func (h *Hub) EventsHub() *evt.Hub {
	if h == nil {
		return nil
	}
	return h.inner
}

func libraryChangePayload(event LibraryChangeEvent) map[string]any {
	reason := event.Reason
	if reason == "" {
		reason = "scan"
	}
	return map[string]any{
		"library_id":               event.LibraryID,
		"reason":                   reason,
		"new":                      event.New,
		"updated":                  event.Updated,
		"missing":                  event.Missing,
		"matched_files":            event.MatchedFiles,
		"retried_items":            event.RetriedItems,
		"still_unmatched_warnings": event.StillUnmatchedWarnings,
	}
}

func convertEnvelope(env evt.Envelope) (Envelope, bool) {
	converted := Envelope{
		Type:      Type(env.Event),
		Timestamp: env.Timestamp,
		SourceID:  env.SourceID,
		AdminOnly: env.AdminOnly,
	}
	if len(env.Data) == 0 {
		return converted, true
	}
	switch env.Channel {
	case evt.ChannelCatalog:
		var payload struct {
			LibraryID              int    `json:"library_id"`
			ContentID              string `json:"content_id"`
			Reason                 string `json:"reason"`
			Change                 string `json:"change"`
			New                    int    `json:"new"`
			Updated                int    `json:"updated"`
			Missing                int    `json:"missing"`
			MatchedFiles           int    `json:"matched_files"`
			RetriedItems           int    `json:"retried_items"`
			StillUnmatchedWarnings int    `json:"still_unmatched_warnings"`
		}
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			return Envelope{}, false
		}
		converted.LibraryID = payload.LibraryID
		converted.ContentID = payload.ContentID
		converted.Reason = payload.Reason
		converted.Change = payload.Change
		converted.New = payload.New
		converted.Updated = payload.Updated
		converted.Missing = payload.Missing
		converted.MatchedFiles = payload.MatchedFiles
		converted.RetriedItems = payload.RetriedItems
		converted.StillUnmatchedWarnings = payload.StillUnmatchedWarnings
	case evt.ChannelJobs:
		var job models.AdminJob
		if err := json.Unmarshal(env.Data, &job); err != nil {
			return Envelope{}, false
		}
		converted.Job = &job
	}
	return converted, true
}
