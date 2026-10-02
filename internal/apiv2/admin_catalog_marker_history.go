package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
)

type AdminMarkerHistoryService interface {
	AdminMarkerHistory(context.Context, catalogsvc.AccessFilter, handlers.MarkerTarget, int) ([]handlers.MarkerEditAuditView, error)
}
type AdminMarkerHistoryInput struct {
	Limit int `query:"limit" default:"25" minimum:"1" maximum:"100"`
}
type AdminFileMarkerHistoryInput struct {
	AdminMarkerHistoryInput
	FileID ID `path:"fileId"`
}
type AdminItemMarkerHistoryInput struct {
	AdminMarkerHistoryInput
	ID string `path:"id" minLength:"1" maxLength:"512"`
}
type AdminMarkerEdit struct {
	ID                   ID             `json:"id"`
	MediaFileID          ID             `json:"media_file_id"`
	ItemID               *string        `json:"item_id,omitempty"`
	ItemType             *string        `json:"item_type,omitempty"`
	MediaTitle           *string        `json:"media_title,omitempty"`
	FilePath             *string        `json:"file_path,omitempty"`
	Segment              string         `json:"segment" enum:"intro,credits,recap,preview"`
	Action               string         `json:"action" enum:"set,clear"`
	Before               *MarkerSegment `json:"before,omitempty" doc:"Absent when no prior marker existed."`
	After                *MarkerSegment `json:"after,omitempty" doc:"Absent when the marker was cleared."`
	UserID               *ID            `json:"user_id,omitempty"`
	Username             *string        `json:"username,omitempty"`
	ImpersonatorUserID   *ID            `json:"impersonator_user_id,omitempty"`
	ImpersonatorUsername *string        `json:"impersonator_username,omitempty"`
	APIKeyID             *ID            `json:"api_key_id,omitempty"`
	RequestID            *string        `json:"request_id,omitempty"`
	ClientIP             *string        `json:"client_ip,omitempty"`
	UserAgent            *string        `json:"user_agent,omitempty"`
	CreatedAt            Instant        `json:"created_at"`
}
type AdminMarkerHistory struct {
	History []AdminMarkerEdit `json:"history" doc:"Bounded recent edits, newest first; empty, never null."`
}
type AdminMarkerHistoryOutput struct{ Body AdminMarkerHistory }

func registerAdminCatalogMarkerHistory(reg *Registry) {
	op := func(path, id string) Operation {
		return Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/markers"+path, id, "admin-catalog", "Read bounded recent marker edit history."), Class: ClassActingAdmin, ServiceBacked: true}
	}
	Register(reg, op("/history", "listAdminMarkerHistory"), func(ctx context.Context, in *AdminMarkerHistoryInput) (*AdminMarkerHistoryOutput, error) {
		return reg.adminMarkerHistory(ctx, handlers.MarkerTarget{}, in.Limit)
	})
	Register(reg, op("/files/{fileId}/history", "listAdminFileMarkerHistory"), func(ctx context.Context, in *AdminFileMarkerHistoryInput) (*AdminMarkerHistoryOutput, error) {
		id, p := in.FileID.positive("path.fileId")
		if p != nil {
			return nil, p
		}
		return reg.adminMarkerHistory(ctx, handlers.MarkerTarget{FileID: id}, in.Limit)
	})
	Register(reg, op("/items/{id}/history", "listAdminItemMarkerHistory"), func(ctx context.Context, in *AdminItemMarkerHistoryInput) (*AdminMarkerHistoryOutput, error) {
		return reg.adminMarkerHistory(ctx, handlers.MarkerTarget{ItemID: in.ID}, in.Limit)
	})
}
func (reg *Registry) adminMarkerHistory(ctx context.Context, target handlers.MarkerTarget, limit int) (*AdminMarkerHistoryOutput, error) {
	if reg.deps.AdminMarkerHistory == nil {
		return nil, unavailable("marker history")
	}
	var filter catalogsvc.AccessFilter
	if target.FileID > 0 {
		if reg.deps.CatalogAccess == nil {
			return nil, unavailable("catalog access")
		}
		var err error
		filter, err = reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
		if err != nil {
			return nil, collectionProblem(err)
		}
	}
	rows, err := reg.deps.AdminMarkerHistory.AdminMarkerHistory(ctx, filter, target, limit)
	if err != nil {
		return nil, collectionProblem(err)
	}
	out := &AdminMarkerHistoryOutput{Body: AdminMarkerHistory{History: make([]AdminMarkerEdit, 0, len(rows))}}
	for _, row := range rows {
		item := AdminMarkerEdit{ID: IDFromInt(row.ID), MediaFileID: IDFromInt(int64(row.MediaFileID)), ItemID: row.ItemID, ItemType: row.ItemType, MediaTitle: row.MediaTitle, FilePath: row.FilePath, Segment: row.Segment, Action: row.Action, Username: row.Username, ImpersonatorUsername: row.ImpersonatorUsername, RequestID: row.RequestID, ClientIP: row.ClientIP, UserAgent: row.UserAgent, CreatedAt: NewInstant(row.CreatedAt)}
		if row.Before != nil {
			item.Before = new(markerSegment(*row.Before))
		}
		if row.After != nil {
			item.After = new(markerSegment(*row.After))
		}
		if row.UserID != nil {
			item.UserID = new(IDFromInt(int64(*row.UserID)))
		}
		if row.ImpersonatorUserID != nil {
			item.ImpersonatorUserID = new(IDFromInt(int64(*row.ImpersonatorUserID)))
		}
		if row.APIKeyID != nil {
			item.APIKeyID = new(IDFromInt(*row.APIKeyID))
		}
		out.Body.History = append(out.Body.History, item)
	}
	return out, nil
}
