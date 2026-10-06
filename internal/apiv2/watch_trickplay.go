package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/trickplay"
)

// TrickplayService reads published seek-bar previews (*trickplay.Reader).
type TrickplayService interface {
	SignedManifest(ctx context.Context, fileID int) (trickplay.SignedManifest, bool, error)
}

// WatchTrickplayInput names the file whose seek-bar previews to read.
type WatchTrickplayInput struct {
	DeviceID string `header:"X-Silo-Device-Id" maxLength:"128" doc:"The stable device identifier used to resolve playback preferences" example:"tv-1"`
	ID       ID     `path:"id" doc:"A movie or episode" example:"movie:heat-1995"`
	FileID   ID     `query:"file_id" required:"true" doc:"The file being played, one of the item's versions" example:"42"`
}

// WatchTrickplay is a file's seek-bar previews: sprite sheets of
// thumbnails, and how to cut them.
type WatchTrickplay struct {
	FileID          ID                    `json:"file_id" example:"42"`
	IntervalMS      int                   `json:"interval_ms" doc:"Media time each thumbnail covers: thumbnail i shows what plays from i*interval_ms to (i+1)*interval_ms" example:"10000"`
	ThumbnailWidth  int                   `json:"thumbnail_width" doc:"Pixels" example:"300"`
	ThumbnailHeight int                   `json:"thumbnail_height" doc:"Pixels" example:"126"`
	TileColumns     int                   `json:"tile_columns" doc:"Thumbnails across a sheet" example:"10"`
	TileRows        int                   `json:"tile_rows" doc:"Thumbnails down a sheet" example:"10"`
	ThumbnailCount  int                   `json:"thumbnail_count" example:"720"`
	Sheets          []WatchTrickplaySheet `json:"sheets" doc:"Every sheet, in order. Sheet s holds thumbnails s*tile_columns*tile_rows onward, left to right and top to bottom; every sheet has the full grid, black after the last thumbnail"`
	ExpiresAt       Instant               `json:"expires_at" doc:"When the sheet URLs stop working; read the previews again for fresh ones" example:"2026-01-02T03:04:05.000Z"`
}

// WatchTrickplaySheet is one sprite sheet.
type WatchTrickplaySheet struct {
	Index int    `json:"index" example:"0"`
	URL   string `json:"url" doc:"A JPEG image; the URL carries its own authorization"`
}

// WatchTrickplayOutput is the getWatchTrickplay response.
type WatchTrickplayOutput struct {
	Body WatchTrickplay
}

func registerWatchTrickplay(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/watch/{id}/trickplay", "getWatchTrickplay", "watch",
			"Get the seek-bar previews of one of an item's files. Versions whose trickplay_available is true have them."),
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
	}, reg.getWatchTrickplay)
}

func (reg *Registry) getWatchTrickplay(ctx context.Context, in *WatchTrickplayInput) (*WatchTrickplayOutput, error) {
	if reg.deps.Watch == nil || reg.deps.Trickplay == nil {
		return nil, unavailable("trickplay")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	fileID, err := intOfID(in.FileID)
	if err != nil || fileID <= 0 {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationQueryFile, Code: codeInvalid, Detail: "file_id must name a file."})
	}
	// The watch detail decides what the account may play; the previews of a
	// file it does not list are not served.
	filter, err := reg.deps.Watch.ContextAccessFilter(ctx, handlers.AccessFilterOptions{
		DeviceID:       handlers.NewDeviceMetadata(in.DeviceID, "", "").DeviceID,
		SelectedFileID: fileID,
	})
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	detail, err := reg.deps.Watch.WatchDetail(ctx, claims.UserID, profileFrom(ctx), string(in.ID), filter)
	if err != nil {
		return nil, watchDetailProblem(err)
	}
	if !watchDetailHasFile(detail, fileID) {
		return nil, NewProblem(TypeNotFound, "The item has no such file.")
	}
	manifest, ok, err := reg.deps.Trickplay.SignedManifest(ctx, fileID)
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	if !ok {
		return nil, NewProblem(TypeNotFound, "This file has no seek-bar previews.")
	}
	sheets := make([]WatchTrickplaySheet, len(manifest.SheetURLs))
	for i, url := range manifest.SheetURLs {
		sheets[i] = WatchTrickplaySheet{Index: i, URL: url}
	}
	return &WatchTrickplayOutput{Body: WatchTrickplay{
		FileID:          idOfInt(manifest.FileID),
		IntervalMS:      manifest.IntervalMS,
		ThumbnailWidth:  manifest.Width,
		ThumbnailHeight: manifest.Height,
		TileColumns:     manifest.TileColumns,
		TileRows:        manifest.TileRows,
		ThumbnailCount:  manifest.ThumbnailCount,
		Sheets:          sheets,
		ExpiresAt:       NewInstant(manifest.ExpiresAt),
	}}, nil
}

// watchDetailHasFile reports whether fileID is one of detail's versions or
// playback variant parts.
func watchDetailHasFile(detail *catalogpkg.WatchDetail, fileID int) bool {
	if detail == nil {
		return false
	}
	for _, v := range detail.Versions {
		if v.FileID == fileID {
			return true
		}
	}
	for _, variant := range detail.PlaybackVariants {
		for _, part := range variant.Parts {
			for _, v := range part.Versions {
				if v.FileID == fileID {
					return true
				}
			}
		}
	}
	return false
}
