package apiv2

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"strconv"
)

type AdminUnmatchedFilesService interface {
	ListAdminUnmatchedFiles(context.Context, int, int) ([]handlers.AdminUnmatchedFileView, bool, error)
}
type AdminUnmatchedFilesInput struct {
	LimitParam
	Cursor string `query:"cursor"`
}
type AdminUnmatchedFile struct {
	ID            ID     `json:"id"`
	MediaFolderID ID     `json:"media_folder_id"`
	FilePath      string `json:"file_path"`
	FileSize      int64  `json:"file_size"`
	Container     string `json:"container"`
}
type AdminUnmatchedFilesOutput struct {
	Body Collection[AdminUnmatchedFile]
}

func registerAdminCatalogUnmatched(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/unmatched", "listAdminUnmatchedFiles", "admin-catalog", "Read a bounded live page of unmatched files, excluding extras."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminUnmatchedFilesInput) (*AdminUnmatchedFilesOutput, error) {
		if reg.deps.AdminUnmatchedFiles == nil {
			return nil, unavailable("unmatched files")
		}
		scope := adminPolicyListScope(ctx, "listAdminUnmatchedFiles", strconv.Itoa(in.Limit), "id", "id")
		after := 0
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
			if after <= 0 {
				return nil, NewProblem(TypeInvalidCursor, "Invalid file cursor")
			}
		}
		rows, more, err := reg.deps.AdminUnmatchedFiles.ListAdminUnmatchedFiles(ctx, in.Limit, after)
		if err != nil {
			return nil, collectionProblem(err)
		}
		items := make([]AdminUnmatchedFile, 0, len(rows))
		for _, f := range rows {
			items = append(items, AdminUnmatchedFile{ID: IDFromInt(int64(f.ID)), MediaFolderID: IDFromInt(int64(f.MediaFolderID)), FilePath: f.FilePath, FileSize: f.FileSize, Container: f.Container})
		}
		next := ""
		if more && len(rows) > 0 {
			next, err = cursors.Encode(scope, rows[len(rows)-1].ID)
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &AdminUnmatchedFilesOutput{Body: Paginated(items, next)}, nil
	})
}
