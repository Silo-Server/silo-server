package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/policy"
)

type MarkerService interface {
	GetMarkers(context.Context, catalogpkg.AccessFilter, handlers.MarkerTarget) (handlers.FileMarkersView, error)
	SetMarkers(context.Context, catalogpkg.AccessFilter, handlers.MarkerTarget, handlers.MarkerChanges) (handlers.FileMarkersView, error)
	ClearMarker(context.Context, catalogpkg.AccessFilter, int, string) (handlers.FileMarkersView, error)
}

type MarkerSegment struct {
	StartSeconds *float64 `json:"start_seconds,omitempty" doc:"Segment start in source seconds"`
	EndSeconds   *float64 `json:"end_seconds,omitempty" doc:"Segment end in source seconds"`
	Source       *string  `json:"source,omitempty"`
	Provider     *string  `json:"provider,omitempty"`
	Confidence   *float64 `json:"confidence,omitempty"`
	Algorithm    *string  `json:"algorithm,omitempty"`
	DetectedAt   *Instant `json:"detected_at,omitempty"`
}

type FileMarkers struct {
	FileID         ID                 `json:"file_id"`
	Intro          MarkerSegment      `json:"intro"`
	Credits        MarkerSegment      `json:"credits"`
	Recap          MarkerSegment      `json:"recap"`
	Preview        MarkerSegment      `json:"preview"`
	MarkerSegments []MarkerOccurrence `json:"marker_segments" doc:"All effective marker occurrences in source-time order; empty, never null"`
}

// MarkerOccurrence is one continuous range. A kind can occur more than once.
type MarkerOccurrence struct {
	Kind         string  `json:"kind" enum:"intro,credits,recap,preview"`
	StartSeconds float64 `json:"start_seconds" minimum:"0"`
	EndSeconds   float64 `json:"end_seconds" minimum:"0"`
}

func markerOccurrences(segments []models.MarkerSegment) []MarkerOccurrence {
	out := make([]MarkerOccurrence, 0, len(segments))
	for _, segment := range segments {
		out = append(out, MarkerOccurrence{Kind: segment.Kind, StartSeconds: segment.StartSeconds, EndSeconds: segment.EndSeconds})
	}
	return out
}

type MarkerSegmentSet struct {
	StartSeconds *float64 `json:"start_seconds,omitempty" nullable:"false" minimum:"0" doc:"Defaults to zero for intro and recap; required for credits and preview"`
	EndSeconds   *float64 `json:"end_seconds,omitempty" nullable:"false" minimum:"0" doc:"Required for intro and recap; defaults to file duration for credits and preview"`
}

type MarkerUpdate struct {
	Intro   Patch[MarkerSegmentSet] `json:"intro,omitzero" doc:"Omitted leaves unchanged; null clears the segment"`
	Credits Patch[MarkerSegmentSet] `json:"credits,omitzero" doc:"Omitted leaves unchanged; null clears the segment"`
	Recap   Patch[MarkerSegmentSet] `json:"recap,omitzero" doc:"Omitted leaves unchanged; null clears the segment"`
	Preview Patch[MarkerSegmentSet] `json:"preview,omitzero" doc:"Omitted leaves unchanged; null clears the segment"`
}

type FileMarkerInput struct {
	FileID ID `path:"file_id"`
}
type ItemMarkerInput struct {
	ItemID ID `path:"item_id" minLength:"1"`
}
type FileMarkerUpdateInput struct {
	FileMarkerInput
	Body      MarkerUpdate
	RawBody   []byte
	UserAgent string `header:"User-Agent"`
}
type ItemMarkerUpdateInput struct {
	ItemMarkerInput
	Body      MarkerUpdate
	RawBody   []byte
	UserAgent string `header:"User-Agent"`
}
type ClearMarkerInput struct {
	FileMarkerInput
	Segment   string `path:"segment" enum:"intro,credits,recap,preview"`
	UserAgent string `header:"User-Agent"`
}
type FileMarkersOutput struct{ Body FileMarkers }

func registerMarkers(reg *Registry) {
	read := func(path, id string) Operation {
		return Operation{Operation: humaOp(http.MethodGet, Prefix+path, id, "playback", "Read the file's effective markers and provenance."), Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true}
	}
	write := func(method, path, id string) Operation {
		return Operation{Operation: humaOp(method, Prefix+path, id, "playback", "Update supplied manual marker segments atomically."), Class: ClassPermissionGated, Permission: policy.PermissionMarkerEdit, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	}
	Register(reg, read("/markers/files/{file_id}", "getFileMarkers"), func(ctx context.Context, in *FileMarkerInput) (*FileMarkersOutput, error) {
		id, p := in.FileID.positive("path.file_id")
		if p != nil {
			return nil, p
		}
		return reg.markerRead(ctx, handlers.MarkerTarget{FileID: id})
	})
	Register(reg, read("/markers/items/{item_id}", "getItemMarkers"), func(ctx context.Context, in *ItemMarkerInput) (*FileMarkersOutput, error) {
		return reg.markerRead(ctx, handlers.MarkerTarget{ItemID: string(in.ItemID)})
	})
	Register(reg, write(http.MethodPut, "/markers/files/{file_id}", "setFileMarkers"), func(ctx context.Context, in *FileMarkerUpdateInput) (*FileMarkersOutput, error) {
		id, p := in.FileID.positive("path.file_id")
		if p != nil {
			return nil, p
		}
		return reg.markerWrite(handlers.MarkerRequestAuditContext(ctx, in.UserAgent), handlers.MarkerTarget{FileID: id}, in.Body, in.RawBody)
	})
	Register(reg, write(http.MethodPut, "/markers/items/{item_id}", "setItemMarkers"), func(ctx context.Context, in *ItemMarkerUpdateInput) (*FileMarkersOutput, error) {
		return reg.markerWrite(handlers.MarkerRequestAuditContext(ctx, in.UserAgent), handlers.MarkerTarget{ItemID: string(in.ItemID)}, in.Body, in.RawBody)
	})
	Register(reg, write(http.MethodDelete, "/markers/files/{file_id}/{segment}", "clearFileMarkerSegment"), func(ctx context.Context, in *ClearMarkerInput) (*FileMarkersOutput, error) {
		id, p := in.FileID.positive("path.file_id")
		if p != nil {
			return nil, p
		}
		access, p := reg.markerAccess(ctx)
		if p != nil {
			return nil, p
		}
		view, err := reg.deps.Markers.ClearMarker(handlers.MarkerRequestAuditContext(ctx, in.UserAgent), access, id, in.Segment)
		return markerOutput(view, err)
	})
}

func (reg *Registry) markerAccess(ctx context.Context) (catalogpkg.AccessFilter, *Problem) {
	if reg.deps.Markers == nil || reg.deps.CatalogAccess == nil {
		return catalogpkg.AccessFilter{}, NewProblem(TypeDependencyUnavailable, "Marker access is not configured.")
	}
	access, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return catalogpkg.AccessFilter{}, catalogProblem(err, "body")
	}
	return access, nil
}
func (reg *Registry) markerRead(ctx context.Context, target handlers.MarkerTarget) (*FileMarkersOutput, error) {
	access, p := reg.markerAccess(ctx)
	if p != nil {
		return nil, p
	}
	view, err := reg.deps.Markers.GetMarkers(ctx, access, target)
	return markerOutput(view, err)
}
func (reg *Registry) markerWrite(ctx context.Context, target handlers.MarkerTarget, body MarkerUpdate, raw []byte) (*FileMarkersOutput, error) {
	access, p := reg.markerAccess(ctx)
	if p != nil {
		return nil, p
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, NewProblem(TypeValidationFailed, "Invalid marker document.")
	}
	changes := handlers.MarkerChanges{}
	for name, patch := range map[string]Patch[MarkerSegmentSet]{"intro": body.Intro, "credits": body.Credits, "recap": body.Recap, "preview": body.Preview} {
		if !patch.Present {
			continue
		}
		if patch.Null {
			changes[name] = nil
			continue
		}
		var segment map[string]json.RawMessage
		if err := json.Unmarshal(members[name], &segment); err != nil {
			return nil, NewProblem(TypeValidationFailed, "Invalid marker segment.")
		}
		for field, value := range segment {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, NewProblem(TypeValidationFailed, "Segment boundaries cannot be null.").WithErrors(ProblemError{Location: "body." + name + "." + field, Code: codeInvalid, Detail: "omit a boundary to use its default; clear the segment with null"})
			}
		}
		changes[name] = &handlers.MarkerSegmentInput{Start: patch.Value.StartSeconds, End: patch.Value.EndSeconds}
	}
	view, err := reg.deps.Markers.SetMarkers(ctx, access, target, changes)
	return markerOutput(view, err)
}
func markerOutput(view handlers.FileMarkersView, err error) (*FileMarkersOutput, error) {
	if err != nil {
		return nil, catalogProblem(err, "body")
	}
	return &FileMarkersOutput{Body: FileMarkers{FileID: IDFromInt(int64(view.FileID)), Intro: markerSegment(view.Intro), Credits: markerSegment(view.Credits), Recap: markerSegment(view.Recap), Preview: markerSegment(view.Preview), MarkerSegments: markerOccurrences(view.MarkerSegments)}}, nil
}
func markerSegment(view handlers.MarkerSegmentView) MarkerSegment {
	result := MarkerSegment{StartSeconds: view.Start, EndSeconds: view.End, Source: view.Source, Provider: view.Provider, Confidence: view.Confidence, Algorithm: view.Algorithm}
	if view.DetectedAt != nil {
		result.DetectedAt = new(NewInstant(*view.DetectedAt))
	}
	return result
}
