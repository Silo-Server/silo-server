package apiv2

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

const opListCatalogImportSources = "listCatalogImportSources"
const opListLocalCatalogImportSources = "listLocalCatalogImportSources"

type AdminCatalogSourcesService interface {
	ListCatalogImportSourcesPage(context.Context, string, int) ([]handlers.CatalogImportSource, string, error)
	ListLocalCatalogImportSourcesPage(context.Context, string, int) ([]handlers.CatalogImportSource, error)
}
type AdminFilesystemService interface {
	BrowseDirectoryPage(context.Context, string, string, string, int) (handlers.FilesystemDirectoryPage, error)
}
type AdminCatalogSource struct {
	Key          string   `json:"key"`
	SizeBytes    int64    `json:"size_bytes"`
	LastModified *Instant `json:"last_modified,omitempty"`
}
type AdminCatalogSourcesInput struct {
	Limit  int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Cursor string `query:"cursor"`
}
type AdminCatalogSourcesOutput struct {
	Body Collection[AdminCatalogSource]
}
type AdminFilesystemInput struct {
	AdminCatalogSourcesInput
	Path       string `query:"path"`
	NamePrefix string `query:"name_prefix"`
}
type AdminFilesystemEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type AdminFilesystemPage struct {
	Collection[AdminFilesystemEntry]
	Path   string `json:"path"`
	Parent string `json:"parent"`
}
type AdminFilesystemOutput struct{ Body AdminFilesystemPage }
type adminCatalogSourcePosition struct {
	After string `json:"after"`
}

func registerAdminCatalogSources(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	for _, local := range []bool{false, true} {
		path, op := "/admin/catalog/import-sources", opListCatalogImportSources
		if local {
			path, op = "/admin/catalog/local-import-sources", opListLocalCatalogImportSources
		}
		Register(reg, Operation{Operation: humaOp("GET", Prefix+path, op, "admin-catalog", "List a bounded page of catalog import sources. Local sources use path order; storage sources use the storage continuation order."), Class: ClassActingAdmin, ServiceBacked: true}, func(ctx context.Context, in *AdminCatalogSourcesInput) (*AdminCatalogSourcesOutput, error) {
			if reg.deps.AdminCatalogSources == nil {
				return nil, unavailable("catalog sources")
			}
			scope := adminCatalogSourceScope(ctx, op, "")
			var pos adminCatalogSourcePosition
			if in.Cursor != "" {
				if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
					return nil, p
				}
			}
			var rows []handlers.CatalogImportSource
			var next string
			var err error
			if local {
				rows, err = reg.deps.AdminCatalogSources.ListLocalCatalogImportSourcesPage(ctx, pos.After, in.Limit+1)
				if len(rows) > in.Limit {
					next = rows[in.Limit-1].Key
					rows = rows[:in.Limit]
				}
			} else {
				rows, next, err = reg.deps.AdminCatalogSources.ListCatalogImportSourcesPage(ctx, pos.After, in.Limit)
			}
			if err != nil {
				return nil, serviceProblem(err)
			}
			if next != "" {
				next, err = cursors.Encode(scope, adminCatalogSourcePosition{After: next})
				if err != nil {
					return nil, serviceProblem(err)
				}
			}
			items := make([]AdminCatalogSource, 0, len(rows))
			for _, row := range rows {
				items = append(items, AdminCatalogSource{Key: row.Key, SizeBytes: row.SizeBytes, LastModified: instantPtr(row.LastModified)})
			}
			return &AdminCatalogSourcesOutput{Body: Paginated(items, next)}, nil
		})
	}
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/filesystem/browse", "browseAdminFilesystem", "admin-catalog", "Browse a bounded page of directories in path order. Results reflect this server's filesystem and are not a snapshot."), Class: ClassActingAdmin, ServiceBacked: true}, func(ctx context.Context, in *AdminFilesystemInput) (*AdminFilesystemOutput, error) {
		if reg.deps.AdminFilesystem == nil {
			return nil, unavailable("filesystem")
		}
		filter, _ := json.Marshal([]string{in.Path, in.NamePrefix})
		scope := adminCatalogSourceScope(ctx, "browseAdminFilesystem", string(filter))
		var pos adminCatalogSourcePosition
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
				return nil, p
			}
		}
		page, err := reg.deps.AdminFilesystem.BrowseDirectoryPage(ctx, in.Path, in.NamePrefix, pos.After, in.Limit+1)
		if err != nil {
			return nil, serviceProblem(err)
		}
		next := ""
		if len(page.Entries) > in.Limit {
			next, err = cursors.Encode(scope, adminCatalogSourcePosition{After: page.Entries[in.Limit-1].Key})
			if err != nil {
				return nil, serviceProblem(err)
			}
			page.Entries = page.Entries[:in.Limit]
		}
		items := make([]AdminFilesystemEntry, 0, len(page.Entries))
		for _, entry := range page.Entries {
			items = append(items, AdminFilesystemEntry{Name: filepath.Base(entry.Key), Path: entry.Key})
		}
		return &AdminFilesystemOutput{Body: AdminFilesystemPage{Collection: Paginated(items, next), Path: page.Path, Parent: page.Parent}}, nil
	})
}
func adminCatalogSourceScope(ctx context.Context, op, filter string) CursorScope {
	return CursorScope{OperationID: op, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: filter, Sort: "source", Tiebreaker: "key"}
}
