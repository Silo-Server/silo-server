package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type AdminSubtitleDeleteService interface {
	DeleteAdminSubtitle(context.Context, int, *int64) error
}

func registerAdminSubtitleDelete(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodDelete, Prefix+"/admin/subtitles/{id}", "deleteAdminStoredSubtitle", "admin", "Delete stored subtitle metadata using its captured validator. Physical object cleanup is best effort; an uncertain response must not be automatically replayed."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNonRetryable}
	op.DefaultStatus = http.StatusNoContent
	Register(reg, op, func(ctx context.Context, in *AdminSubtitleMetadataInput) (*struct{}, error) {
		if reg.deps.AdminSubtitleDelete == nil {
			return nil, NewProblem(TypeDependencyUnavailable, "Subtitle deletion is unavailable.")
		}
		row, err := reg.adminSubtitleMetadataRow(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, adminSubtitleMetadataTag(ctx, row)); p != nil {
			return nil, p
		}
		revision := new(row.Revision)
		if strings.TrimSpace(in.IfMatch) == "*" {
			revision = nil
		}
		err = reg.deps.AdminSubtitleDelete.DeleteAdminSubtitle(ctx, row.ID, revision)
		if errors.Is(err, subtitles.ErrSubtitleGuardedDeletionUnavailable) {
			return nil, NewProblem(TypeDependencyUnavailable, "Subtitle deletion is unavailable.")
		}
		if err != nil {
			if conflict, ok := errors.AsType[*subtitles.SubtitleRevisionConflict](err); ok && conflict.Current != nil {
				return nil, NewProblem(TypePreconditionFailed, "Subtitle changed; review it before deleting.").WithHeader("ETag", adminSubtitleMetadataTag(ctx, conflict.Current).String())
			}
			if errors.Is(err, subtitles.ErrSubtitleNotFound) {
				return nil, NewProblem(TypeNotFound, "Stored subtitle not found.")
			}
			return nil, NewProblem(TypeInternalError, "Unable to confirm subtitle deletion. Reconcile the stored subtitle before another attempt.")
		}
		return nil, nil
	})
}
