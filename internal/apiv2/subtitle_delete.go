package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type ViewerSubtitleDeleteService interface {
	GetViewerSubtitleForDeletion(context.Context, catalogpkg.AccessFilter, int) (*subtitles.DownloadedSubtitle, error)
	DeleteViewerSubtitle(context.Context, catalogpkg.AccessFilter, int, int64) error
}

type ViewerSubtitleMetadataInput struct {
	ID          ID     `path:"id" pattern:"^[1-9][0-9]*$"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}

type ViewerSubtitleMetadataOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   StoredSubtitle
}

func viewerSubtitleTag(ctx context.Context, row *subtitles.DownloadedSubtitle) EntityTag {
	return RenderETag("viewer-subtitle/"+strconv.Itoa(claimsFrom(ctx).UserID)+"/"+profileFrom(ctx), strconv.Itoa(row.ID), row.Revision)
}

func viewerSubtitleDeleteProblem(err error) *Problem {
	switch {
	case errors.Is(err, subtitles.ErrSubtitleNotFound):
		return NewProblem(TypeNotFound, "Stored subtitle not found.")
	case errors.Is(err, subtitles.ErrSubtitleGuardedDeletionUnavailable):
		return NewProblem(TypeDependencyUnavailable, "Subtitle deletion is unavailable.")
	default:
		if _, ok := errors.AsType[*subtitles.SubtitleRevisionConflict](err); ok {
			return NewProblem(TypePreconditionFailed, "Subtitle changed; read its current metadata before another attempt.")
		}
		if _, ok := errors.AsType[*handlers.APIError](err); ok {
			return serviceProblem(err)
		}
		return NewProblem(TypeInternalError, "Unable to confirm subtitle deletion. Reconcile stored metadata before another attempt; physical cleanup is not confirmed.")
	}
}

func (reg *Registry) viewerSubtitleDeletionRow(ctx context.Context, raw ID) (*subtitles.DownloadedSubtitle, catalogpkg.AccessFilter, error) {
	var access catalogpkg.AccessFilter
	id, p := raw.positive("path.id")
	if p != nil {
		return nil, access, p
	}
	if reg.deps.ViewerSubtitleDelete == nil || reg.deps.CatalogAccess == nil {
		return nil, access, NewProblem(TypeDependencyUnavailable, "Subtitle deletion is unavailable.")
	}
	access, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return nil, access, catalogProblem(err, "path")
	}
	row, err := reg.deps.ViewerSubtitleDelete.GetViewerSubtitleForDeletion(ctx, access, id)
	if err != nil {
		return nil, access, viewerSubtitleDeleteProblem(err)
	}
	if row == nil {
		return nil, access, NewProblem(TypeNotFound, "Stored subtitle not found.")
	}
	return row, access, nil
}

func registerViewerSubtitleDeletion(reg *Registry) {
	read := Operation{Operation: humaOp(http.MethodGet, Prefix+"/subtitles/stored/{id}/metadata", "getViewerSubtitleMetadata", "subtitles", "Read a deletable stored subtitle and its viewer-bound validator. Requires file access and downloading-account or effective administrator authority."), Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true, Conditional: true}
	Register(reg, read, func(ctx context.Context, in *ViewerSubtitleMetadataInput) (*ViewerSubtitleMetadataOutput, error) {
		row, _, err := reg.viewerSubtitleDeletionRow(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		tag := viewerSubtitleTag(ctx, row)
		out := &ViewerSubtitleMetadataOutput{ETag: tag.String(), Body: storedSubtitleView(*row)}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	del := Operation{Operation: humaOp(http.MethodDelete, Prefix+"/subtitles/stored/{id}", "deleteStoredSubtitle", "subtitles", "Delete the authorized stored subtitle using If-Match. Success confirms metadata deletion only; object cleanup is best effort. Never automatically retry an uncertain response."), Class: ClassProfileScoped, ProfileOptional: true, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNonRetryable}
	del.DefaultStatus = http.StatusNoContent
	Register(reg, del, func(ctx context.Context, in *ViewerSubtitleMetadataInput) (*struct{}, error) {
		row, access, err := reg.viewerSubtitleDeletionRow(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, viewerSubtitleTag(ctx, row)); p != nil {
			return nil, p
		}
		if err := reg.deps.ViewerSubtitleDelete.DeleteViewerSubtitle(ctx, access, row.ID, row.Revision); err != nil {
			return nil, viewerSubtitleDeleteProblem(err)
		}
		return nil, nil
	})
}
