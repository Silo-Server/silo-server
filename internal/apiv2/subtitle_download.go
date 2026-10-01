package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type SubtitleDownloadService interface {
	DownloadStoredSubtitle(context.Context, catalogpkg.AccessFilter, subtitles.DownloadRequest) (*subtitles.DownloadedSubtitle, error)
}

type SubtitleDownloadBody struct {
	MediaFileID     ID      `json:"media_file_id"`
	Provider        string  `json:"provider" minLength:"1" maxLength:"128"`
	SubtitleID      ID      `json:"subtitle_id"`
	Language        string  `json:"language" minLength:"1" maxLength:"64"`
	ReleaseName     string  `json:"release_name" maxLength:"4096"`
	Score           float64 `json:"score"`
	HearingImpaired bool    `json:"hearing_impaired"`
}
type SubtitleDownloadInput struct{ Body SubtitleDownloadBody }
type SubtitleDownloadResult struct {
	Subtitle StoredSubtitle `json:"subtitle"`
}
type SubtitleDownloadOutput struct{ Body SubtitleDownloadResult }

func registerSubtitleDownloads(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/subtitles/download", "downloadSubtitle", "subtitles", "Download the selected provider result for an accessible file. Send once: content deduplication does not replay an upstream provider request or recover an uncertain response."), Class: ClassProfileScoped, ProfileOptional: true, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, func(ctx context.Context, in *SubtitleDownloadInput) (*SubtitleDownloadOutput, error) {
		id, p := in.Body.MediaFileID.positive("body.media_file_id")
		if p != nil {
			return nil, p
		}
		if reg.deps.SubtitleDownloads == nil || reg.deps.CatalogAccess == nil {
			return nil, NewProblem(TypeDependencyUnavailable, "Subtitle download is not configured.")
		}
		access, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
		if err != nil {
			return nil, catalogProblem(err, "body")
		}
		row, err := reg.deps.SubtitleDownloads.DownloadStoredSubtitle(ctx, access, subtitles.DownloadRequest{MediaFileID: id, ProviderName: in.Body.Provider, SubtitleID: string(in.Body.SubtitleID), Language: in.Body.Language, ReleaseName: in.Body.ReleaseName, Score: in.Body.Score, HearingImpaired: in.Body.HearingImpaired})
		if err != nil {
			return nil, serviceProblem(err)
		}
		if row == nil {
			return nil, NewProblem(TypeInternalError, "Subtitle download returned no result.")
		}
		return &SubtitleDownloadOutput{Body: SubtitleDownloadResult{Subtitle: storedSubtitleView(*row)}}, nil
	})
}
