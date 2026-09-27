package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type DownloadCreationService interface {
	Create(context.Context, int, downloads.CreateRequest, catalogpkg.AccessFilter) (*downloads.Download, error)
	CreateSeriesPage(context.Context, int, downloads.CreateRequest, *int, *catalogpkg.EpisodePagePosition, int, catalogpkg.AccessFilter) (downloads.CreatePage, error)
}
type DownloadEntryExpectation struct {
	ID       ID  `json:"id,omitempty"`
	Revision int `json:"revision" minimum:"0"`
}

type DownloadCreateBody struct {
	ContentID          string                              `json:"content_id" minLength:"1" maxLength:"128"`
	EpisodeID          string                              `json:"episode_id,omitempty" maxLength:"128"`
	MediaFileID        ID                                  `json:"media_file_id,omitempty"`
	Quality            string                              `json:"quality,omitempty" enum:"original,20mbps,10mbps,5mbps,2mbps,1mbps"`
	Series             bool                                `json:"series,omitzero"`
	SeasonNumber       *int                                `json:"season_number,omitempty" minimum:"0" maximum:"9999"`
	Caps               *playback.ClientCapabilities        `json:"caps,omitempty"`
	ExpectedRevision   *int                                `json:"expected_revision,omitempty" minimum:"0" doc:"Required for a single managed request: zero requires absence; a positive revision authorizes replacement. Retain after uncertainty."`
	ExpectedDownloadID ID                                  `json:"expected_download_id,omitempty" doc:"Required with a positive expected_revision; retain the exact registry entry ID."`
	BatchID            ID                                  `json:"batch_id,omitempty" doc:"Required stable client-selected batch identity for every page of a series request."`
	ExpectedEntries    map[string]DownloadEntryExpectation `json:"expected_entries,omitempty" doc:"At most 100 episode ID to exact registry ID/revision guards. Existing rows without a guard retain current bytes, status and batch."`
}
type DownloadCreateInput struct {
	DeviceID       string `header:"X-Silo-Device-Id" maxLength:"128"`
	DeviceName     string `header:"X-Silo-Device-Name" maxLength:"120"`
	DevicePlatform string `header:"X-Silo-Device-Platform" maxLength:"40"`
	Limit          int    `query:"limit" default:"50" minimum:"1" maximum:"100"`
	Cursor         string `query:"cursor"`
	Body           DownloadCreateBody
}
type DownloadCreated struct {
	Items   []DownloadEntry             `json:"items"`
	BatchID ID                          `json:"batch_id,omitempty"`
	Skipped []downloads.SkippedDownload `json:"skipped"`
	Page    PageInfo                    `json:"page"`
}
type DownloadCreateOutput struct{ Body DownloadCreated }

func registerDownloadCreation(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/downloads", "createDownloads", "downloads", "Create one download or one bounded series/season page using shared preparation and registration. Do not automatically replay uncertain creation; reconcile the registry first."), Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNonRetryable}
	op.DefaultStatus = http.StatusAccepted
	op.MaxBodyBytes = 256 << 10
	op.Errors = []int{409, 429, 501}
	Register(reg, op, func(ctx context.Context, in *DownloadCreateInput) (*DownloadCreateOutput, error) {
		return reg.createDownloads(ctx, cursors, in)
	})
}
func downloadCreationProblem(err error) *Problem {
	switch {
	case errors.Is(err, downloads.ErrStatusConflict):
		return NewProblem(TypeConflict, "The managed entry changed; reconcile the registry before a new creation request.")
	case errors.Is(err, downloads.ErrSubscriptionsUnavailable):
		return NewProblem(TypeDependencyUnavailable, "Bounded episode selection is not configured.")
	case errors.Is(err, downloads.ErrConcurrentLimitReached), errors.Is(err, downloads.ErrPeriodLimitReached):
		return NewProblem(TypeRateLimited, err.Error())
	case errors.Is(err, downloads.ErrTranscodeDisabled):
		return NewProblem(TypePermissionDenied, err.Error())
	case errors.Is(err, downloads.ErrBulkQualityUnavailable), errors.Is(err, downloads.ErrQualityUnavailable), errors.Is(err, downloads.ErrFormatUnavailable):
		return NewProblem(TypeCapabilityUnsupported, err.Error())
	case errors.Is(err, downloads.ErrCapacityUnavailable), errors.Is(err, downloads.ErrCapabilityUnavailable):
		return NewProblem(TypeDependencyUnavailable, err.Error())
	case errors.Is(err, downloads.ErrInvalidQuality), errors.Is(err, downloads.ErrNotSeries):
		return NewProblem(TypeMalformedRequest, err.Error())
	case errors.Is(err, downloads.ErrNoDownloadableEpisodes):
		return NewProblem(TypeNotFound, err.Error())
	default:
		return downloadProblem(err)
	}
}
func (reg *Registry) createDownloads(ctx context.Context, cursors *Cursors, in *DownloadCreateInput) (*DownloadCreateOutput, error) {
	if reg.deps.DownloadCreation == nil {
		return nil, unavailable("download creation")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	body := in.Body
	req := downloads.CreateRequest{StrictIdentity: true, ContentID: body.ContentID, EpisodeID: body.EpisodeID, Quality: body.Quality, ProfileID: profile, DeviceID: in.DeviceID, DeviceName: in.DeviceName, DevicePlatform: in.DevicePlatform, ExpectedRevision: body.ExpectedRevision, BatchID: string(body.BatchID), ExpectedDownloadID: string(body.ExpectedDownloadID)}
	if body.MediaFileID != "" {
		id, err := strconv.Atoi(string(body.MediaFileID))
		if err != nil || id < 1 || strconv.Itoa(id) != string(body.MediaFileID) {
			return nil, NewProblem(TypeMalformedRequest, "media_file_id must be a canonical positive integer string.")
		}
		req.FileID = id
	}
	if body.Caps != nil {
		req.Caps = *body.Caps
		if err := req.Caps.NormalizeAndValidateVideoDecode(); err != nil {
			return nil, NewProblem(TypeMalformedRequest, err.Error())
		}
	}
	if len(body.ExpectedEntries) > 100 {
		return nil, NewProblem(TypeMalformedRequest, "At most 100 episode revision guards are allowed.")
	}
	if body.ExpectedEntries != nil {
		req.ExpectedEntries = map[string]downloads.ManagedCreateExpectation{}
	}
	for key, entry := range body.ExpectedEntries {
		if key == "" || len(key) > 128 || entry.Revision < 0 || (entry.Revision == 0 && entry.ID != "") || (entry.Revision > 0 && (entry.ID == "" || len(entry.ID) > 128)) {
			return nil, NewProblem(TypeMalformedRequest, "Invalid episode entry guard.")
		}
		req.ExpectedEntries[key] = downloads.ManagedCreateExpectation{ID: string(entry.ID), Revision: entry.Revision}
	}
	if body.ExpectedRevision != nil && ((*body.ExpectedRevision == 0 && body.ExpectedDownloadID != "") || (*body.ExpectedRevision > 0 && (body.ExpectedDownloadID == "" || len(body.ExpectedDownloadID) > 128))) {
		return nil, NewProblem(TypeMalformedRequest, "A replacement requires its exact expected_download_id and positive expected_revision; absence requires revision zero without an ID.")
	}
	if body.ExpectedRevision == nil && body.ExpectedDownloadID != "" {
		return nil, NewProblem(TypeMalformedRequest, "expected_download_id requires expected_revision.")
	}

	if in.DeviceID == "" && (body.ExpectedRevision != nil || body.ExpectedEntries != nil) {
		return nil, NewProblem(TypeMalformedRequest, "Revision guards require a managed device identity.")
	}
	out := DownloadCreated{Items: []DownloadEntry{}, Skipped: []downloads.SkippedDownload{}}
	if !body.Series {
		if body.SeasonNumber != nil || body.BatchID != "" || body.ExpectedEntries != nil || in.Cursor != "" {
			return nil, NewProblem(TypeMalformedRequest, "Batch fields require series=true.")
		}
		if in.DeviceID != "" && body.ExpectedRevision == nil {
			return nil, NewProblem(TypeMalformedRequest, "A managed single download requires expected_revision.")
		}
		row, err := reg.deps.DownloadCreation.Create(ctx, user, req, handlers.AccessFilterFromContext(ctx, ""))
		if err != nil {
			return nil, downloadCreationProblem(err)
		}
		out.Items = append(out.Items, downloadEntryOf(row))
		return &DownloadCreateOutput{Body: out}, nil
	}
	if body.BatchID == "" || len(body.BatchID) > 128 || body.EpisodeID != "" || body.MediaFileID != "" || body.ExpectedRevision != nil {
		return nil, NewProblem(TypeMalformedRequest, "Series pages require batch_id and cannot select one file, episode or revision.")
	}
	filter, _ := json.Marshal(struct {
		Device, Batch, Series, Quality string
		Season                         *int
	}{in.DeviceID, string(body.BatchID), body.ContentID, body.Quality, body.SeasonNumber})
	scope := CursorScope{OperationID: "createDownloads", Security: strconv.Itoa(user) + "/" + profile + "/" + viewerScopeDigest(ctx), Filter: string(filter), Sort: "season,episode,id", Tiebreaker: "id"}
	var after *catalogpkg.EpisodePagePosition
	if in.Cursor != "" {
		after = &catalogpkg.EpisodePagePosition{}
		if p := cursors.Decode(scope, in.Cursor, after); p != nil {
			return nil, p
		}
	}
	page, err := reg.deps.DownloadCreation.CreateSeriesPage(ctx, user, req, body.SeasonNumber, after, in.Limit, handlers.AccessFilterFromContext(ctx, ""))
	if err != nil {
		return nil, downloadCreationProblem(err)
	}
	for _, row := range page.Items {
		out.Items = append(out.Items, downloadEntryOf(row))
	}
	out.BatchID = ID(page.BatchID)
	out.Skipped = append(out.Skipped, page.Skipped...)
	if page.Next != nil {
		next, err := cursors.Encode(scope, *page.Next)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out.Page = PageInfo{HasMore: true, NextCursor: next}
	}
	return &DownloadCreateOutput{Body: out}, nil
}
