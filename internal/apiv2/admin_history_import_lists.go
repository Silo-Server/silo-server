package apiv2

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

type AdminHistoryImportListInput struct {
	LimitParam
	Cursor   string `query:"cursor"`
	SourceID ID     `query:"source_id" pattern:"^[1-9][0-9]*$"`
}
type AdminHistoryImportSourceCollectionOutput struct {
	Body Collection[AdminHistoryImportSource]
}
type AdminHistoryImportMappingCollectionOutput struct {
	Body Collection[AdminHistoryImportMapping]
}
type AdminHistoryImportExternalUserCollectionOutput struct {
	Body Collection[historyimport.ExternalUser]
}
type AdminHistoryImportExternalUsersInput struct {
	AdminHistoryImportIDInput
	LimitParam
	Cursor string `query:"cursor"`
}

func adminHistoryCursorScope(ctx context.Context, op, filter string) CursorScope {
	return CursorScope{OperationID: op, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: filter, Sort: "id", Tiebreaker: "id"}
}

// Configuration lists are administrator-maintained sets. We read each set once,
// then expose a bounded keyset page rather than an unbounded response.
func adminHistoryConfigPage[T any](c *Cursors, scope CursorScope, cursor string, limit int, rows []T, key func(T) int) (Collection[T], error) {
	after := 0
	if cursor != "" {
		if p := c.Decode(scope, cursor, &after); p != nil {
			return Collection[T]{}, p
		}
		if after <= 0 {
			return Collection[T]{}, NewProblem(TypeInvalidCursor, "Invalid configuration cursor.")
		}
	}
	slices.SortFunc(rows, func(a, b T) int { return cmp.Compare(key(a), key(b)) })
	items := make([]T, 0, limit)
	next := ""
	for _, row := range rows {
		if key(row) <= after {
			continue
		}
		if len(items) == limit {
			var err error
			next, err = c.Encode(scope, key(items[len(items)-1]))
			if err != nil {
				return Collection[T]{}, NewProblem(TypeInternalError, "Unable to encode cursor.")
			}
			break
		}
		items = append(items, row)
	}
	return Paginated(items, next), nil
}
func registerAdminHistoryImportLists(reg *Registry, op func(string, string, string, bool) Operation) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, op(http.MethodGet, "/admin/history-import-sources", "listAdminHistoryImportSources", false), func(ctx context.Context, in *CursorListInput) (*AdminHistoryImportSourceCollectionOutput, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		rows, err := s.ListAdminSources(ctx)
		if err != nil {
			return nil, adminHistoryProblem(err)
		}
		page, err := adminHistoryConfigPage(cursors, adminHistoryCursorScope(ctx, "listAdminHistoryImportSources", ""), in.Cursor, in.Limit, rows, func(s historyimport.Source) int { return s.ID })
		if err != nil {
			return nil, err
		}
		items := make([]AdminHistoryImportSource, 0, len(page.Items))
		for i := range page.Items {
			items = append(items, adminHistorySourceOf(&page.Items[i]))
		}
		return &AdminHistoryImportSourceCollectionOutput{Body: Collection[AdminHistoryImportSource]{Items: items, Page: page.Page}}, nil
	})
	Register(reg, op(http.MethodGet, "/admin/history-imports/mappings", "listAdminHistoryImportMappings", false), func(ctx context.Context, in *AdminHistoryImportListInput) (*AdminHistoryImportMappingCollectionOutput, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		source, p := adminHistoryID(in.SourceID)
		if p != nil {
			return nil, p
		}
		rows, err := s.ListMappings(ctx, source)
		if err != nil {
			return nil, adminHistoryProblem(err)
		}
		page, err := adminHistoryConfigPage(cursors, adminHistoryCursorScope(ctx, "listAdminHistoryImportMappings", string(in.SourceID)), in.Cursor, in.Limit, rows, func(m historyimport.UserMapping) int { return m.ID })
		if err != nil {
			return nil, err
		}
		items := make([]AdminHistoryImportMapping, 0, len(page.Items))
		for i := range page.Items {
			items = append(items, adminHistoryMappingOf(&page.Items[i]))
		}
		return &AdminHistoryImportMappingCollectionOutput{Body: Collection[AdminHistoryImportMapping]{Items: items, Page: page.Page}}, nil
	})
	// A source that is disabled or has no stored admin token cannot be asked
	// for its users; adminHistoryDiscoveryProblem answers that with 409.
	discover := op(http.MethodGet, "/admin/history-imports/sources/{id}/users", "listAdminHistoryImportExternalUsers", false)
	discover.Errors = []int{http.StatusConflict}
	Register(reg, discover, func(ctx context.Context, in *AdminHistoryImportExternalUsersInput) (*AdminHistoryImportExternalUserCollectionOutput, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		id, p := adminHistoryID(in.ID)
		if p != nil {
			return nil, p
		}
		scope := adminHistoryCursorScope(ctx, "listAdminHistoryImportExternalUsers", string(in.ID))
		after := ""
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
		}
		rows, err := s.DiscoverExternalUsers(ctx, id)
		if err != nil {
			return nil, adminHistoryDiscoveryProblem(err)
		}
		slices.SortFunc(rows, func(a, b historyimport.ExternalUser) int { return cmp.Compare(a.ID, b.ID) })
		items := make([]historyimport.ExternalUser, 0, in.Limit)
		next := ""
		for _, row := range rows {
			if row.ID <= after {
				continue
			}
			if len(items) == in.Limit {
				next, err = cursors.Encode(scope, items[len(items)-1].ID)
				if err != nil {
					return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
				}
				break
			}
			items = append(items, row)
		}
		return &AdminHistoryImportExternalUserCollectionOutput{Body: Paginated(items, next)}, nil
	})
}
