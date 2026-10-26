package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/trickplay"
)

// AdminTrickplayService answers administrators about seek-bar previews
// (*trickplay.Admin).
type AdminTrickplayService interface {
	ItemStatus(ctx context.Context, itemID string) ([]trickplay.FileStatus, error)
	Regenerate(ctx context.Context, itemID string) (int, error)
	LibraryStatuses(ctx context.Context) ([]trickplay.LibraryStatus, error)
}

// AdminItemTrickplayInput names the item.
type AdminItemTrickplayInput struct {
	ID string `path:"id" minLength:"1" maxLength:"512" doc:"A movie, an episode, or a series (every episode file)" example:"movie:heat-1995"`
}

// AdminTrickplayFile is one media file's seek-bar previews.
type AdminTrickplayFile struct {
	FileID         ID       `json:"file_id" example:"42"`
	State          string   `json:"state" enum:"off,pending,running,ready,unusable" doc:"off when the file's library does not generate previews; unusable when the file cannot yield them until it changes"`
	Servable       bool     `json:"servable" doc:"Players are served previews now; a regeneration keeps serving the previous ones until it publishes"`
	Failures       int      `json:"failures" doc:"Consecutive failures since the last success" example:"0"`
	LastError      string   `json:"last_error,omitempty" doc:"Why the last attempt failed"`
	GeneratedAt    *Instant `json:"generated_at,omitempty" doc:"When the served previews were published"`
	ThumbnailCount int      `json:"thumbnail_count,omitempty" example:"720"`
	ThumbnailWidth int      `json:"thumbnail_width,omitempty" doc:"Pixels" example:"300"`
	IntervalMS     int      `json:"interval_ms,omitempty" example:"10000"`
	SheetBytes     int64    `json:"sheet_bytes,omitempty" doc:"Storage the published sheets take" example:"2100000"`
}

// AdminItemTrickplay is an item's files and their previews.
type AdminItemTrickplay struct {
	Files []AdminTrickplayFile `json:"files" doc:"Every media file of the item; empty, never null"`
}

// AdminItemTrickplayOutput is the getAdminItemTrickplay response.
type AdminItemTrickplayOutput struct {
	Body AdminItemTrickplay
}

// AdminTrickplayRegeneration reports a regeneration.
type AdminTrickplayRegeneration struct {
	Requeued int `json:"requeued" doc:"Files queued ahead of the backlog; a file whose previews are being made is left to finish" example:"1"`
}

// AdminTrickplayRegenerationOutput is the regenerateAdminItemTrickplay
// response.
type AdminTrickplayRegenerationOutput struct {
	Body AdminTrickplayRegeneration
}

// AdminTrickplayLibrary is the previews of a library that generates them.
type AdminTrickplayLibrary struct {
	LibraryID  ID     `json:"library_id" example:"1"`
	Name       string `json:"name" example:"Movies"`
	Pending    int    `json:"pending" doc:"Files waiting for previews, including those backing off after a failure" example:"12"`
	Running    int    `json:"running" example:"1"`
	Ready      int    `json:"ready" example:"840"`
	Unusable   int    `json:"unusable" example:"2"`
	SheetBytes int64  `json:"sheet_bytes" doc:"Storage the library's sheets take" example:"1800000000"`
}

// AdminTrickplayLibraries lists the libraries that generate previews.
type AdminTrickplayLibraries struct {
	Items []AdminTrickplayLibrary `json:"items" doc:"Empty, never null"`
}

// AdminTrickplayLibrariesOutput is the listAdminTrickplayLibraries response.
type AdminTrickplayLibrariesOutput struct {
	Body AdminTrickplayLibraries
}

func registerAdminTrickplay(reg *Registry) {
	Register(reg, Operation{
		Operation:     humaOp(http.MethodGet, Prefix+"/admin/items/{id}/trickplay", "getAdminItemTrickplay", "admin-catalog", "Report the seek-bar previews of an item's files."),
		Class:         ClassActingAdmin,
		ServiceBacked: true,
	}, reg.getAdminItemTrickplay)

	regenerate := Operation{
		Operation: humaOp(http.MethodPost, Prefix+"/admin/items/{id}/trickplay/regenerate", "regenerateAdminItemTrickplay", "admin-catalog",
			"Queue an item's seek-bar previews to be made again ahead of the backlog. The previous previews keep serving until the new ones publish."),
		Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNonRetryable,
	}
	regenerate.DefaultStatus = http.StatusAccepted
	// The item's library does not generate previews.
	regenerate.Errors = append(regenerate.Errors, http.StatusConflict)
	Register(reg, regenerate, reg.regenerateAdminItemTrickplay)

	Register(reg, Operation{
		Operation:     humaOp(http.MethodGet, Prefix+"/admin/trickplay/libraries", "listAdminTrickplayLibraries", "admin-catalog", "List the libraries that generate seek-bar previews, with their files' progress and storage."),
		Class:         ClassActingAdmin,
		ServiceBacked: true,
	}, reg.listAdminTrickplayLibraries)
}

func (reg *Registry) getAdminItemTrickplay(ctx context.Context, in *AdminItemTrickplayInput) (*AdminItemTrickplayOutput, error) {
	if reg.deps.AdminTrickplay == nil {
		return nil, unavailable("trickplay")
	}
	files, err := reg.deps.AdminTrickplay.ItemStatus(ctx, in.ID)
	if err != nil {
		return nil, adminTrickplayProblem(err)
	}
	out := AdminItemTrickplay{Files: make([]AdminTrickplayFile, 0, len(files))}
	for _, f := range files {
		file := AdminTrickplayFile{
			FileID: idOfInt(f.FileID), State: f.State, Servable: f.Servable, Failures: f.Failures, LastError: f.LastError,
			ThumbnailCount: f.ThumbnailCount, ThumbnailWidth: f.Width, IntervalMS: f.IntervalMS, SheetBytes: f.SheetBytes,
		}
		if f.GeneratedAt != nil {
			generated := NewInstant(*f.GeneratedAt)
			file.GeneratedAt = &generated
		}
		out.Files = append(out.Files, file)
	}
	return &AdminItemTrickplayOutput{Body: out}, nil
}

func (reg *Registry) regenerateAdminItemTrickplay(ctx context.Context, in *AdminItemTrickplayInput) (*AdminTrickplayRegenerationOutput, error) {
	if reg.deps.AdminTrickplay == nil {
		return nil, unavailable("trickplay")
	}
	requeued, err := reg.deps.AdminTrickplay.Regenerate(ctx, in.ID)
	if err != nil {
		return nil, adminTrickplayProblem(err)
	}
	return &AdminTrickplayRegenerationOutput{Body: AdminTrickplayRegeneration{Requeued: requeued}}, nil
}

func (reg *Registry) listAdminTrickplayLibraries(ctx context.Context, _ *struct{}) (*AdminTrickplayLibrariesOutput, error) {
	if reg.deps.AdminTrickplay == nil {
		return nil, unavailable("trickplay")
	}
	libraries, err := reg.deps.AdminTrickplay.LibraryStatuses(ctx)
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	out := AdminTrickplayLibraries{Items: make([]AdminTrickplayLibrary, 0, len(libraries))}
	for _, l := range libraries {
		out.Items = append(out.Items, AdminTrickplayLibrary{
			LibraryID: idOfInt(l.LibraryID), Name: l.Name, Pending: l.Pending, Running: l.Running,
			Ready: l.Ready, Unusable: l.Unusable, SheetBytes: l.SheetBytes,
		})
	}
	return &AdminTrickplayLibrariesOutput{Body: out}, nil
}

func adminTrickplayProblem(err error) error {
	switch {
	case errors.Is(err, trickplay.ErrItemNotFound):
		return NewProblem(TypeNotFound, "The item has no media files.")
	case errors.Is(err, trickplay.ErrNotOptedIn):
		return NewProblem(TypeCapabilityDisabled, "Seek-bar previews are turned off for this item's library.")
	}
	return NewProblem(TypeInternalError, "An unexpected error occurred.")
}
