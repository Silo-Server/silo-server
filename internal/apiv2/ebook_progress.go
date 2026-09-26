package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

// EbookProgressService is shared with the legacy reader transport.
type EbookProgressService interface {
	ReaderCapability(context.Context) handlers.EbookReaderCapability
	ReaderProgress(context.Context, int, string, string, catalogpkg.AccessFilter) (*handlers.EbookReaderProgress, error)
	SaveReaderProgress(context.Context, handlers.EbookReaderProgress, catalogpkg.AccessFilter) (*handlers.EbookReaderProgress, error)
}

type EbookProgressInput struct {
	ContentID string `path:"content_id" minLength:"1"`
}

type SaveEbookProgressInput struct {
	EbookProgressInput
	Body struct {
		FileID    ID      `json:"file_id"`
		Location  string  `json:"location" minLength:"1" maxLength:"8192"`
		Progress  float64 `json:"progress" minimum:"0" maximum:"1"`
		UpdatedAt Instant `json:"updated_at" doc:"Client event time; preserve exactly on retry. Future times are rejected. Older and equal events retain the stored position."`
	}
}

type EbookProgress struct {
	ContentID string  `json:"content_id"`
	FileID    ID      `json:"file_id"`
	Location  string  `json:"location"`
	Progress  float64 `json:"progress"`
	UpdatedAt Instant `json:"updated_at"`
}

type EbookProgressOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		Progress *EbookProgress `json:"progress,omitempty" doc:"Absent when this profile has no saved position."`
	}
}

func registerEbookProgress(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/capabilities/ebooks", "getEbookCapability", "ebooks", "Discover ordered reader progress and Kindle conversion support."),
		Class:     ClassProfileScoped,
	}, reg.getEbookCapability)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/ebooks/{content_id}/progress", "getEbookProgress", "ebooks", "Read the acting profile's saved ebook position."),
		Class:     ClassProfileScoped, ServiceBacked: true,
	}, reg.getEbookProgress)
	Register(reg, Operation{
		Operation: humaOp(http.MethodPut, Prefix+"/ebooks/{content_id}/progress", "saveEbookProgress", "ebooks", "Save an ebook position only when its client event time is newer; return the current position."),
		Class:     ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyDomainIdentity,
		MaxBodyBytes: 16 << 10,
	}, reg.saveEbookProgress)
}

func (reg *Registry) getEbookProgress(ctx context.Context, in *EbookProgressInput) (*EbookProgressOutput, error) {
	if reg.deps.EbookProgress == nil {
		return nil, unavailable("ebook progress")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	progress, err := reg.deps.EbookProgress.ReaderProgress(ctx, userID, profileID, in.ContentID, handlers.AccessFilterFromContext(ctx, ""))
	if err != nil {
		return nil, ebookProblem(err)
	}
	return ebookProgressOutput(progress), nil
}

func (reg *Registry) saveEbookProgress(ctx context.Context, in *SaveEbookProgressInput) (*EbookProgressOutput, error) {
	if reg.deps.EbookProgress == nil {
		return nil, unavailable("ebook progress")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	fileID, p := in.Body.FileID.positive("body.file_id")
	if p != nil {
		return nil, p
	}
	progress, err := reg.deps.EbookProgress.SaveReaderProgress(ctx, handlers.EbookReaderProgress{
		UserID: userID, ProfileID: profileID, ContentID: in.ContentID, FileID: fileID,
		Location: in.Body.Location, Progress: in.Body.Progress, UpdatedAt: in.Body.UpdatedAt.Time,
	}, handlers.AccessFilterFromContext(ctx, ""))
	if err != nil {
		return nil, ebookProblem(err)
	}
	return ebookProgressOutput(progress), nil
}

func ebookProgressOutput(progress *handlers.EbookReaderProgress) *EbookProgressOutput {
	out := &EbookProgressOutput{CacheControl: "private, no-store"}
	if progress != nil {
		out.Body.Progress = &EbookProgress{ContentID: progress.ContentID, FileID: IDFromInt(int64(progress.FileID)),
			Location: progress.Location, Progress: progress.Progress, UpdatedAt: NewInstant(progress.UpdatedAt)}
	}
	return out
}

type EbookCapability struct {
	Capability
	GuardedAnnotations bool     `json:"guarded_annotations"`
	ReaderFiles        bool     `json:"reader_files"`
	GuardedConfig      bool     `json:"guarded_config"`
	OrderedProgress    bool     `json:"ordered_progress" doc:"Reader progress accepts client event times and refuses older or equal writes."`
	KindleConversion   bool     `json:"kindle_conversion"`
	SourceFormats      []string `json:"source_formats"`
	ServedFormat       string   `json:"served_format"`
	Header             string   `json:"header"`
	HeaderFailedValue  string   `json:"header_failed_value"`
}
type EbookCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         EbookCapability
}

func (reg *Registry) getEbookCapability(ctx context.Context, _ *CapabilityInput) (*EbookCapabilityOutput, error) {
	view := handlers.EbookReaderCapability{}
	if reg.deps.EbookProgress != nil {
		view = reg.deps.EbookProgress.ReaderCapability(ctx)
	}
	state := StateNotConfigured
	if view.Progress || view.Config || view.Files || view.Annotations {
		state = StateAvailable
	}
	return &EbookCapabilityOutput{CacheControl: "private, no-cache", Body: EbookCapability{
		Capability:         Capability{State: state},
		GuardedAnnotations: view.Annotations, ReaderFiles: view.Files, GuardedConfig: view.Config, OrderedProgress: view.Progress, KindleConversion: view.KindleConversion,
		SourceFormats: []string{"mobi", "azw", "azw3"}, ServedFormat: "epub",
		Header: handlers.ConversionHeader, HeaderFailedValue: "failed",
	}}, nil
}
