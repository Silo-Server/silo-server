package apiv2

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/danielgtaylor/huma/v2"
)

type SubtitleUploadService interface {
	UploadStoredSubtitle(context.Context, catalogpkg.AccessFilter, subtitles.UploadRequest) (*subtitles.DownloadedSubtitle, error)
}

type SubtitleUploadForm struct {
	File             huma.FormFile `form:"file" contentType:"application/octet-stream"`
	MediaFileID      string        `form:"media_file_id" minLength:"1" maxLength:"128"`
	Language         string        `form:"language" required:"false" maxLength:"64"`
	LanguageOverride bool          `form:"language_override" required:"false"`
	ReleaseName      string        `form:"release_name" required:"false" maxLength:"4096"`
	HearingImpaired  bool          `form:"hearing_impaired" required:"false"`
}
type SubtitleUploadInput struct {
	RawBody huma.MultipartFormFiles[SubtitleUploadForm]
}
type SubtitleDetectForm struct {
	File     huma.FormFile `form:"file" contentType:"application/octet-stream"`
	Language string        `form:"language" required:"false" maxLength:"64"`
}
type SubtitleDetectInput struct {
	RawBody huma.MultipartFormFiles[SubtitleDetectForm]
}
type SubtitleDetection struct {
	Language string `json:"language"`
	Source   string `json:"source" enum:"filename,metadata,content,manual"`
}
type SubtitleDetectOutput struct{ Body SubtitleDetection }

func subtitleFormBytes(file huma.FormFile) ([]byte, error) {
	if !file.IsSet {
		return nil, NewProblem(TypeValidationFailed, "Supply a subtitle file.")
	}
	defer func() { _ = file.Close() }()
	if file.Size > subtitles.MaxUploadSize {
		return nil, NewProblem(TypePayloadTooLarge, "Subtitle file must be under 5 MB.")
	}
	data, err := io.ReadAll(io.LimitReader(file, subtitles.MaxUploadSize+1))
	if err != nil {
		return nil, NewProblem(TypeInternalError, "Failed to read subtitle upload.")
	}
	if len(data) > subtitles.MaxUploadSize {
		return nil, NewProblem(TypePayloadTooLarge, "Subtitle file must be under 5 MB.")
	}
	return data, nil
}

func registerSubtitleUploads(reg *Registry) {
	op := func(path, id string) Operation {
		return Operation{Operation: humaOp(http.MethodPost, Prefix+path, id, "subtitles", "Submit a bounded subtitle multipart form."), Class: ClassProfileScoped, ProfileOptional: true, MaxBodyBytes: subtitles.MaxUploadSize + (256 << 10)}
	}
	upload := op("/subtitles/upload", "uploadSubtitle")
	upload.ServiceBacked = true
	upload.DemoRestricted = true
	upload.RetrySafety = RetrySafetyNonRetryable
	upload.Description = "Store a user subtitle file. Send once: content deduplication does not provide durable replay across a later deletion or metadata edit."
	Register(reg, upload, func(ctx context.Context, in *SubtitleUploadInput) (*SubtitleDownloadOutput, error) {
		form := in.RawBody.Data()
		if form == nil {
			return nil, NewProblem(TypeValidationFailed, "Supply a subtitle form.")
		}
		id, p := ID(strings.TrimSpace(form.MediaFileID)).positive("body.media_file_id")
		if p != nil {
			return nil, p
		}
		if reg.deps.SubtitleUploads == nil || reg.deps.CatalogAccess == nil {
			return nil, NewProblem(TypeDependencyUnavailable, "Subtitle upload is not configured.")
		}
		access, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
		if err != nil {
			return nil, catalogProblem(err, "body")
		}
		data, err := subtitleFormBytes(form.File)
		if err != nil {
			return nil, err
		}
		row, err := reg.deps.SubtitleUploads.UploadStoredSubtitle(ctx, access, subtitles.UploadRequest{MediaFileID: id, Language: strings.TrimSpace(form.Language), PreferUserLanguage: form.LanguageOverride, Filename: form.File.Filename, ReleaseName: strings.TrimSpace(form.ReleaseName), HearingImpaired: form.HearingImpaired, Data: data})
		if err != nil {
			return nil, serviceProblem(err)
		}
		if row == nil {
			return nil, NewProblem(TypeInternalError, "Subtitle upload returned no result.")
		}
		return &SubtitleDownloadOutput{Body: SubtitleDownloadResult{Subtitle: storedSubtitleView(*row)}}, nil
	})
	detect := op("/subtitles/detect-language", "detectSubtitleLanguage")
	detect.RetrySafety = RetrySafetyNaturalIdempotent
	detect.Description = "Detect a subtitle language from filename, metadata or content with an optional manual fallback. Does not store the file."
	Register(reg, detect, func(_ context.Context, in *SubtitleDetectInput) (*SubtitleDetectOutput, error) {
		form := in.RawBody.Data()
		if form == nil {
			return nil, NewProblem(TypeValidationFailed, "Supply a subtitle form.")
		}
		data, err := subtitleFormBytes(form.File)
		if err != nil {
			return nil, err
		}
		format, err := subtitles.FormatFromFilename(form.File.Filename)
		if err != nil {
			return nil, NewProblem(TypeMalformedRequest, "Unsupported subtitle filename or format.")
		}
		detected, err := subtitles.ResolveUploadLanguage(form.File.Filename, format, data, strings.TrimSpace(form.Language), false)
		if err != nil {
			return nil, NewProblem(TypeMalformedRequest, "Could not detect subtitle language; supply a valid fallback language.")
		}
		return &SubtitleDetectOutput{Body: SubtitleDetection{Language: detected.Language, Source: string(detected.Source)}}, nil
	})
}
