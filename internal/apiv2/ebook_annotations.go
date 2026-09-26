package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/danielgtaylor/huma/v2"
)

type EbookAnnotationService interface {
	ReaderAnnotationPage(context.Context, handlers.EbookAnnotationScope, *handlers.EbookAnnotationPosition, int) ([]handlers.EbookReaderAnnotation, error)
	CreateReaderAnnotation(context.Context, handlers.EbookAnnotationScope, string, handlers.EbookAnnotationCreate) (*handlers.EbookReaderAnnotation, bool, error)
	UpdateReaderAnnotation(context.Context, handlers.EbookAnnotationScope, string, handlers.EbookAnnotationPatch, handlers.EbookAnnotationGuard) (*handlers.EbookReaderAnnotation, error)
	DeleteReaderAnnotation(context.Context, handlers.EbookAnnotationScope, string, handlers.EbookAnnotationGuard) error
}

type AnnotationMetadata map[string]json.RawMessage

func (AnnotationMetadata) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: true, Extensions: map[string]any{extExtensionBag: "ebook-annotation-metadata"}, Description: "Client-owned annotation metadata; bounded by the request body limit."}
}

type EbookAnnotation struct {
	ID           ID                 `json:"id"`
	ContentID    string             `json:"content_id"`
	Kind         string             `json:"kind"`
	CFIRange     string             `json:"cfi_range,omitempty"`
	Location     string             `json:"location,omitempty"`
	SelectedText string             `json:"selected_text"`
	Note         string             `json:"note"`
	Style        string             `json:"style"`
	Color        string             `json:"color"`
	Metadata     AnnotationMetadata `json:"metadata"`
	CreatedAt    Instant            `json:"created_at"`
	UpdatedAt    Instant            `json:"updated_at"`
	ETag         string             `json:"etag" doc:"Current validator for this annotation; send it as If-Match when editing or deleting."`
}
type EbookAnnotationOutput struct {
	ETag string `header:"ETag"`
	Body EbookAnnotation
}
type EbookAnnotationCreatedOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   EbookAnnotation
}
type EbookAnnotationPageOutput struct{ Body Collection[EbookAnnotation] }
type EbookAnnotationListInput struct {
	ContentID string `path:"content_id" minLength:"1"`
	Limit     int    `query:"limit" default:"20" minimum:"1" maximum:"50" doc:"Bounded annotation page; at most 50 potentially large user-text records."`
	Cursor    string `query:"cursor"`
}
type EbookAnnotationCreateBody struct {
	ID           ID                 `json:"id" minLength:"1" maxLength:"128" doc:"Client-selected immutable identity; retain on retry. A UUID is recommended."`
	Kind         string             `json:"kind,omitempty" enum:"highlight,note,bookmark"`
	CFIRange     string             `json:"cfi_range,omitempty"`
	Location     string             `json:"location,omitempty"`
	SelectedText string             `json:"selected_text,omitempty"`
	Note         string             `json:"note,omitempty"`
	Style        string             `json:"style,omitempty"`
	Color        string             `json:"color,omitempty"`
	Metadata     AnnotationMetadata `json:"metadata,omitempty"`
}
type EbookAnnotationCreateInput struct {
	ContentID string `path:"content_id" minLength:"1"`
	Body      EbookAnnotationCreateBody
}
type EbookAnnotationPatchBody struct {
	Kind         Patch[string]             `json:"kind,omitzero"`
	CFIRange     Patch[string]             `json:"cfi_range,omitzero"`
	Location     Patch[string]             `json:"location,omitzero"`
	SelectedText Patch[string]             `json:"selected_text,omitzero"`
	Note         Patch[string]             `json:"note,omitzero"`
	Style        Patch[string]             `json:"style,omitzero"`
	Color        Patch[string]             `json:"color,omitzero"`
	Metadata     Patch[AnnotationMetadata] `json:"metadata,omitzero"`
}
type EbookAnnotationMutationInput struct {
	ContentID    string `path:"content_id" minLength:"1"`
	AnnotationID string `path:"annotation_id" minLength:"1" maxLength:"128"`
	IfMatch      string `header:"If-Match"`
	IfNoneMatch  string `header:"If-None-Match"`
}
type EbookAnnotationUpdateInput struct {
	ContentID    string `path:"content_id" minLength:"1"`
	AnnotationID string `path:"annotation_id" minLength:"1" maxLength:"128"`
	IfMatch      string `header:"If-Match"`
	IfNoneMatch  string `header:"If-None-Match"`
	Body         EbookAnnotationPatchBody
}

func registerEbookAnnotations(reg *Registry) {
	op := func(method, path, id, summary string) Operation {
		return Operation{Operation: humaOp(method, Prefix+path, id, "ebooks", summary), Class: ClassProfileScoped, ServiceBacked: true}
	}
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, op(http.MethodGet, "/ebooks/{content_id}/annotations", "listEbookAnnotations", "Page the acting profile's annotations by newest update and identity."), func(ctx context.Context, in *EbookAnnotationListInput) (*EbookAnnotationPageOutput, error) {
		return reg.listEbookAnnotations(ctx, cursors, in)
	})
	create := op(http.MethodPost, "/ebooks/{content_id}/annotations", "createEbookAnnotation", "Create an annotation using its client-selected identity; retry returns the existing scoped annotation without changing it.")
	create.DefaultStatus = 201
	create.MaxBodyBytes = 256 << 10
	create.RetrySafety = RetrySafetyUniqueConstraint
	create.Errors = []int{409}
	create.Responses = map[string]*huma.Response{"200": {Description: "The annotation identity already exists; returns its current state.", Content: map[string]*huma.MediaType{mediaTypeJSON: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[EbookAnnotation](), true, "")}}}}
	Register(reg, create, reg.createEbookAnnotation)
	update := op(http.MethodPatch, "/ebooks/{content_id}/annotations/{annotation_id}", "updateEbookAnnotation", "Apply an annotation patch under its current validator; null clears a field.")
	update.Guarded = true
	update.MaxBodyBytes = 256 << 10
	update.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, update, reg.updateEbookAnnotation)
	remove := op(http.MethodDelete, "/ebooks/{content_id}/annotations/{annotation_id}", "deleteEbookAnnotation", "Delete an annotation under its current validator.")
	remove.Guarded = true
	remove.DefaultStatus = 204
	remove.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, remove, reg.deleteEbookAnnotation)
}

func readerAnnotationScope(ctx context.Context, contentID string) (handlers.EbookAnnotationScope, *Problem) {
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return handlers.EbookAnnotationScope{}, p
	}
	return handlers.EbookAnnotationScope{UserID: userID, ProfileID: profileID, ContentID: contentID, Access: handlers.AccessFilterFromContext(ctx, "")}, nil
}
func ebookAnnotationTag(scope handlers.EbookAnnotationScope, row handlers.EbookReaderAnnotation) EntityTag {
	b, _ := json.Marshal(struct {
		UserID               int
		ProfileID, ContentID string
		Row                  handlers.EbookReaderAnnotation
	}{scope.UserID, scope.ProfileID, scope.ContentID, row})
	sum := sha256.Sum256(b)
	return EntityTag{Opaque: hex.EncodeToString(sum[:])}
}
func ebookAnnotationOf(scope handlers.EbookAnnotationScope, row handlers.EbookReaderAnnotation) (EbookAnnotation, error) {
	metadata := AnnotationMetadata{}
	if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
		return EbookAnnotation{}, err
	}
	return EbookAnnotation{ID: ID(row.ID), ContentID: row.ContentID, Kind: row.Kind, CFIRange: row.CFIRange, Location: row.Location, SelectedText: row.SelectedText, Note: row.Note, Style: row.Style, Color: row.Color, Metadata: metadata, CreatedAt: NewInstant(row.CreatedAt), UpdatedAt: NewInstant(row.UpdatedAt), ETag: ebookAnnotationTag(scope, row).String()}, nil
}
func (reg *Registry) listEbookAnnotations(ctx context.Context, cursors *Cursors, in *EbookAnnotationListInput) (*EbookAnnotationPageOutput, error) {
	if reg.deps.EbookAnnotations == nil {
		return nil, unavailable("ebook annotations")
	}
	scope, p := readerAnnotationScope(ctx, in.ContentID)
	if p != nil {
		return nil, p
	}
	cursorScope := CursorScope{OperationID: "listEbookAnnotations", Security: strconv.Itoa(scope.UserID) + "/" + scope.ProfileID + "/" + viewerScopeDigest(ctx), Filter: in.ContentID, Sort: "-updated_at,-id", Tiebreaker: "id"}
	var after *handlers.EbookAnnotationPosition
	if in.Cursor != "" {
		after = &handlers.EbookAnnotationPosition{}
		if p := cursors.Decode(cursorScope, in.Cursor, after); p != nil {
			return nil, p
		}
	}
	rows, err := reg.deps.EbookAnnotations.ReaderAnnotationPage(ctx, scope, after, in.Limit+1)
	if err != nil {
		return nil, ebookProblem(err)
	}
	next := ""
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		last := rows[len(rows)-1]
		next, err = cursors.Encode(cursorScope, handlers.EbookAnnotationPosition{UpdatedAt: last.UpdatedAt, ID: last.ID})
		if err != nil {
			return nil, serviceProblem(err)
		}
	}
	items := make([]EbookAnnotation, 0, len(rows))
	for _, row := range rows {
		item, err := ebookAnnotationOf(scope, row)
		if err != nil {
			return nil, serviceProblem(err)
		}
		items = append(items, item)
	}
	return &EbookAnnotationPageOutput{Body: Paginated(items, next)}, nil
}
func (reg *Registry) createEbookAnnotation(ctx context.Context, in *EbookAnnotationCreateInput) (*EbookAnnotationCreatedOutput, error) {
	if reg.deps.EbookAnnotations == nil {
		return nil, unavailable("ebook annotations")
	}
	scope, p := readerAnnotationScope(ctx, in.ContentID)
	if p != nil {
		return nil, p
	}
	var metadata json.RawMessage
	if in.Body.Metadata != nil {
		var err error
		metadata, err = json.Marshal(in.Body.Metadata)
		if err != nil {
			return nil, serviceProblem(err)
		}
	}
	req := handlers.EbookAnnotationCreate{Kind: in.Body.Kind, CFIRange: in.Body.CFIRange, Location: in.Body.Location, SelectedText: in.Body.SelectedText, Note: in.Body.Note, Style: in.Body.Style, Color: in.Body.Color, Metadata: metadata}
	row, created, err := reg.deps.EbookAnnotations.CreateReaderAnnotation(ctx, scope, string(in.Body.ID), req)
	if err != nil {
		return nil, ebookProblem(err)
	}
	out, err := ebookAnnotationOf(scope, *row)
	if err != nil {
		return nil, serviceProblem(err)
	}
	status := 200
	if created {
		status = 201
	}
	return &EbookAnnotationCreatedOutput{Status: status, ETag: out.ETag, Body: out}, nil
}
func annotationPatchString(value Patch[string]) *string {
	if !value.Present {
		return nil
	}
	return new(value.Value)
}
func (reg *Registry) updateEbookAnnotation(ctx context.Context, in *EbookAnnotationUpdateInput) (*EbookAnnotationOutput, error) {
	if reg.deps.EbookAnnotations == nil {
		return nil, unavailable("ebook annotations")
	}
	scope, p := readerAnnotationScope(ctx, in.ContentID)
	if p != nil {
		return nil, p
	}
	req := handlers.EbookAnnotationPatch{Kind: annotationPatchString(in.Body.Kind), CFIRange: annotationPatchString(in.Body.CFIRange), Location: annotationPatchString(in.Body.Location), SelectedText: annotationPatchString(in.Body.SelectedText), Note: annotationPatchString(in.Body.Note), Style: annotationPatchString(in.Body.Style), Color: annotationPatchString(in.Body.Color)}
	if in.Body.Metadata.Present {
		req.Metadata = json.RawMessage(`{}`)
		if !in.Body.Metadata.Null {
			var err error
			req.Metadata, err = json.Marshal(in.Body.Metadata.Value)
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
	}
	var condition *Problem
	row, err := reg.deps.EbookAnnotations.UpdateReaderAnnotation(ctx, scope, in.AnnotationID, req, func(row handlers.EbookReaderAnnotation) error {
		condition = EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, ebookAnnotationTag(scope, row))
		if condition != nil {
			return condition
		}
		return nil
	})
	if condition != nil {
		return nil, condition
	}
	if err != nil {
		return nil, ebookProblem(err)
	}
	out, err := ebookAnnotationOf(scope, *row)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &EbookAnnotationOutput{ETag: out.ETag, Body: out}, nil
}
func (reg *Registry) deleteEbookAnnotation(ctx context.Context, in *EbookAnnotationMutationInput) (*struct{}, error) {
	if reg.deps.EbookAnnotations == nil {
		return nil, unavailable("ebook annotations")
	}
	scope, p := readerAnnotationScope(ctx, in.ContentID)
	if p != nil {
		return nil, p
	}
	var condition *Problem
	err := reg.deps.EbookAnnotations.DeleteReaderAnnotation(ctx, scope, in.AnnotationID, func(row handlers.EbookReaderAnnotation) error {
		condition = EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, ebookAnnotationTag(scope, row))
		if condition != nil {
			return condition
		}
		return nil
	})
	if condition != nil {
		return nil, condition
	}
	if err != nil {
		return nil, ebookProblem(err)
	}
	return &struct{}{}, nil
}
