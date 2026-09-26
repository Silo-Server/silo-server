package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type AdminSubtitleMetadataService interface {
	GetAdminSubtitleMetadata(context.Context, int) (*subtitles.DownloadedSubtitle, error)
	UpdateAdminSubtitleMetadata(context.Context, int, subtitles.SubtitleMetadataPatch, *int64) (*subtitles.DownloadedSubtitle, error)
}

type AdminSubtitleMetadata struct {
	ID              ID      `json:"id"`
	MediaFileID     ID      `json:"media_file_id"`
	Provider        string  `json:"provider"`
	Language        string  `json:"language"`
	Format          string  `json:"format"`
	ReleaseName     string  `json:"release_name"`
	Score           float64 `json:"score"`
	HearingImpaired bool    `json:"hearing_impaired"`
	CreatedAt       Instant `json:"created_at"`
	DownloadedBy    *ID     `json:"downloaded_by,omitempty"`
}
type AdminSubtitleMetadataOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   AdminSubtitleMetadata
}
type AdminSubtitleMetadataInput struct {
	ID          ID     `path:"id" pattern:"^[1-9][0-9]*$"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminSubtitleMetadataPatch struct {
	Language        *string `json:"language,omitempty" nullable:"false" minLength:"1" maxLength:"128"`
	ReleaseName     *string `json:"release_name,omitempty" nullable:"false" maxLength:"4096"`
	HearingImpaired *bool   `json:"hearing_impaired,omitempty" nullable:"false"`
}
type AdminSubtitleMetadataPatchInput struct {
	ID          ID     `path:"id" pattern:"^[1-9][0-9]*$"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	RawBody     []byte
	Body        AdminSubtitleMetadataPatch
}

func adminSubtitleMetadataTag(ctx context.Context, row *subtitles.DownloadedSubtitle) EntityTag {
	language := subtitles.NormalizeProviderLanguage(row.Provider, row.Language)
	return RenderETag("admin-subtitle-metadata/"+strconv.Itoa(claimsFrom(ctx).UserID)+"/"+profileFrom(ctx), strconv.Itoa(row.ID)+"/"+language, row.Revision)
}
func adminSubtitleMetadataProjection(row *subtitles.DownloadedSubtitle) AdminSubtitleMetadata {
	item := AdminSubtitleMetadata{ID: IDFromInt(int64(row.ID)), MediaFileID: IDFromInt(int64(row.MediaFileID)), Provider: row.Provider, Language: subtitles.NormalizeProviderLanguage(row.Provider, row.Language), Format: string(row.Format), ReleaseName: row.ReleaseName, Score: row.Score, HearingImpaired: row.HearingImpaired, CreatedAt: NewInstant(row.CreatedAt)}
	if row.DownloadedBy != nil {
		item.DownloadedBy = new(IDFromInt(int64(*row.DownloadedBy)))
	}
	return item
}
func adminSubtitleMetadataProblem(ctx context.Context, err error) *Problem {
	if conflict, ok := errors.AsType[*subtitles.SubtitleRevisionConflict](err); ok && conflict.Current != nil {
		return NewProblem(TypePreconditionFailed, "Subtitle metadata changed; review the current value before saving.").WithHeader("ETag", adminSubtitleMetadataTag(ctx, conflict.Current).String())
	}
	switch {
	case errors.Is(err, handlers.ErrAdminSubtitleMetadataUnavailable):
		return NewProblem(TypeDependencyUnavailable, "Subtitle administration is unavailable.")
	case errors.Is(err, subtitles.ErrSubtitleNotFound):
		return NewProblem(TypeNotFound, "Stored subtitle not found.")
	case errors.Is(err, subtitles.ErrSubtitleLanguageConflict):
		return NewProblem(TypeConflict, "Identical subtitle content already exists for this file and language.")
	default:
		return NewProblem(TypeInternalError, "Unable to manage stored subtitle metadata.")
	}
}
func (reg *Registry) adminSubtitleMetadataRow(ctx context.Context, raw ID) (*subtitles.DownloadedSubtitle, error) {
	if reg.deps.AdminSubtitleMetadata == nil {
		return nil, NewProblem(TypeDependencyUnavailable, "Subtitle administration is unavailable.")
	}
	id, err := intOfID(raw)
	if err != nil || id <= 0 {
		return nil, NewProblem(TypeValidationFailed, "Invalid subtitle ID.")
	}
	row, err := reg.deps.AdminSubtitleMetadata.GetAdminSubtitleMetadata(ctx, id)
	if err != nil {
		return nil, adminSubtitleMetadataProblem(ctx, err)
	}
	if row == nil {
		return nil, NewProblem(TypeNotFound, "Stored subtitle not found.")
	}
	if _, ok := subtitles.CanonicalProviderLanguage(row.Provider, row.Language); !ok {
		return nil, NewProblem(TypeInternalError, "Stored subtitle has an invalid language value.")
	}
	return row, nil
}
func registerAdminSubtitleMetadata(reg *Registry) {
	read := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/subtitles/{id}", "getAdminSubtitleMetadata", "admin", "Read canonical stored subtitle metadata and its edit validator."), Class: ClassActingAdmin, ServiceBacked: true, Conditional: true}
	Register(reg, read, func(ctx context.Context, in *AdminSubtitleMetadataInput) (*AdminSubtitleMetadataOutput, error) {
		row, err := reg.adminSubtitleMetadataRow(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		tag := adminSubtitleMetadataTag(ctx, row)
		out := &AdminSubtitleMetadataOutput{ETag: tag.String(), Body: adminSubtitleMetadataProjection(row)}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	update := Operation{Operation: humaOp(http.MethodPatch, Prefix+"/admin/subtitles/{id}", "updateAdminSubtitleMetadata", "admin", "Merge supplied metadata using the captured strong validator. Content remains immutable. Uncertain saves require explicit reconciliation; never automatically rebase or replay."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNonRetryable}
	update.Errors = append(update.Errors, http.StatusConflict)
	Register(reg, update, func(ctx context.Context, in *AdminSubtitleMetadataPatchInput) (*AdminSubtitleMetadataOutput, error) {
		row, err := reg.adminSubtitleMetadataRow(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, adminSubtitleMetadataTag(ctx, row)); p != nil {
			return nil, p
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		patch := subtitles.SubtitleMetadataPatch{Language: in.Body.Language, ReleaseName: in.Body.ReleaseName, HearingImpaired: in.Body.HearingImpaired}
		if patch.Language == nil && patch.ReleaseName == nil && patch.HearingImpaired == nil {
			return nil, NewProblem(TypeValidationFailed, "Supply at least one metadata field.")
		}
		if patch.Language != nil {
			if _, err := subtitles.NormalizeLanguageCode(*patch.Language); err != nil {
				return nil, NewProblem(TypeValidationFailed, "Invalid subtitle language.")
			}
		}
		revision := new(row.Revision)
		if strings.TrimSpace(in.IfMatch) == "*" {
			revision = nil
		}
		row, err = reg.deps.AdminSubtitleMetadata.UpdateAdminSubtitleMetadata(ctx, row.ID, patch, revision)
		if err != nil {
			return nil, adminSubtitleMetadataProblem(ctx, err)
		}
		if row == nil {
			return nil, NewProblem(TypeNotFound, "Stored subtitle not found.")
		}
		return &AdminSubtitleMetadataOutput{ETag: adminSubtitleMetadataTag(ctx, row).String(), Body: adminSubtitleMetadataProjection(row)}, nil
	})
}
