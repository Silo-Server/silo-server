package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

type SubtitleAICreateService interface {
	CreateSubtitleAIJob(context.Context, catalogpkg.AccessFilter, handlers.SubtitleAICreateCommand) (handlers.SubtitleAICreateResult, error)
}
type SubtitleAICreateBody struct {
	MediaFileID    ID      `json:"media_file_id"`
	Kind           string  `json:"kind" enum:"translate,transcribe,transcribe_translate"`
	SourceIndex    int     `json:"source_index" minimum:"-1"`
	SourceLanguage string  `json:"source_language" maxLength:"64"`
	TargetLanguage string  `json:"target_language" maxLength:"64"`
	SessionID      string  `json:"session_id,omitempty" maxLength:"128"`
	StartPosition  float64 `json:"start_position" minimum:"0"`
}
type SubtitleAICreateInput struct{ Body SubtitleAICreateBody }
type SubtitleAICreateOutput struct {
	Body struct {
		Job                  SubtitleAIJob `json:"job"`
		LiveDeliveryAttached bool          `json:"live_delivery_attached"`
	}
}

func registerSubtitleAICreate(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/subtitles/ai/translate", "createSubtitleAIJob", "subtitles", "Start subtitle translation or transcription for an accessible file."), Class: ClassProfileScoped, ProfileOptional: true, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	op.DefaultStatus = http.StatusAccepted
	op.Description = "Send once. Active-job deduplication does not provide durable request replay or worker recovery. Optional live delivery requires the caller's local playback session and exact file; it is best effort and is not attached to an existing deduplicated job. Poll the returned job for persisted outcome."
	Register(reg, op, func(ctx context.Context, in *SubtitleAICreateInput) (*SubtitleAICreateOutput, error) {
		id, p := in.Body.MediaFileID.positive("body.media_file_id")
		if p != nil {
			return nil, p
		}
		if reg.deps.SubtitleAICreate == nil || reg.deps.CatalogAccess == nil {
			return nil, NewProblem(TypeDependencyUnavailable, "Subtitle AI is unavailable.")
		}
		filter, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
		if err != nil {
			return nil, catalogProblem(err, "body")
		}
		result, err := reg.deps.SubtitleAICreate.CreateSubtitleAIJob(ctx, filter, handlers.SubtitleAICreateCommand{MediaFileID: id, Kind: ai.JobKind(in.Body.Kind), SourceIndex: in.Body.SourceIndex, SourceLanguage: in.Body.SourceLanguage, TargetLanguage: in.Body.TargetLanguage, SessionID: in.Body.SessionID, StartPosition: in.Body.StartPosition})
		if err != nil {
			return nil, serviceProblem(err)
		}
		if result.Job == nil {
			return nil, NewProblem(TypeInternalError, "Subtitle processing returned no job.")
		}
		out := new(SubtitleAICreateOutput)
		out.Body.Job = projectSubtitleAIJob(result.Job)
		out.Body.LiveDeliveryAttached = result.LiveDeliveryAttached
		return out, nil
	})
}
