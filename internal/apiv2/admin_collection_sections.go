package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// AdminCollectionSectionService reports the administrator Home and library
// page rows that show server collections. It reads page_sections only, so
// rows profiles added to their own Home are neither listed nor counted.
type AdminCollectionSectionService interface {
	AdminCollectionSections(context.Context, string) ([]handlers.AdminCollectionSection, error)
	AdminCollectionRowCounts(context.Context, []string) (map[string]handlers.AdminCollectionRowCount, error)
}

type AdminCollectionSection struct {
	ID           ID     `json:"id"`
	Scope        string `json:"scope" enum:"home,library"`
	LibraryID    *ID    `json:"library_id" nullable:"true" doc:"The library whose page holds the row; null for a Home row."`
	SectionType  string `json:"section_type"`
	Title        string `json:"title"`
	Featured     bool   `json:"featured" doc:"The row is its page's hero banner."`
	Enabled      bool   `json:"enabled"`
	Position     int    `json:"position"`
	PageRowCount int    `json:"page_row_count" doc:"Rows on the page that holds this row, turned-off rows included."`
}
type AdminCollectionSectionsOutput struct {
	Body Collection[AdminCollectionSection]
}

func registerAdminCollectionSections(reg *Registry) {
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/collections/{id}/sections", "listAdminCollectionSections", "List the administrator Home and library page rows that show a collection. Rows profiles added themselves are not listed.", false), reg.listAdminCollectionSections)
}

// adminCollectionSections returns the row-reference service, or nil when the
// configured admin collection service does not report rows.
func (reg *Registry) adminCollectionSections() AdminCollectionSectionService {
	s, _ := reg.deps.AdminCollections.(AdminCollectionSectionService)
	return s
}

func (reg *Registry) listAdminCollectionSections(ctx context.Context, in *AdminCollectionIDInput) (*AdminCollectionSectionsOutput, error) {
	s := reg.adminCollectionSections()
	if s == nil {
		return nil, unavailable("admin collection sections")
	}
	refs, err := s.AdminCollectionSections(ctx, string(in.ID))
	if err != nil {
		return nil, adminCollectionError(err)
	}
	items := make([]AdminCollectionSection, 0, len(refs))
	for _, r := range refs {
		item := AdminCollectionSection{ID: ID(r.ID), Scope: r.Scope, SectionType: string(r.SectionType), Title: r.Title, Featured: r.Featured, Enabled: r.Enabled, Position: r.Position, PageRowCount: r.PageRowCount}
		if r.LibraryID != nil {
			item.LibraryID = new(ID(strconv.Itoa(*r.LibraryID)))
		}
		items = append(items, item)
	}
	return &AdminCollectionSectionsOutput{Body: NewCollection(items)}, nil
}

// withAdminCollectionRowCounts sets home_row_count and row_count on every
// listed collection from one grouped count. The counts stay absent when the
// service does not report rows.
func (reg *Registry) withAdminCollectionRowCounts(ctx context.Context, items []AdminCollection) error {
	s := reg.adminCollectionSections()
	if s == nil || len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, c := range items {
		ids = append(ids, string(c.ID))
	}
	counts, err := s.AdminCollectionRowCounts(ctx, ids)
	if err != nil || counts == nil {
		return err
	}
	for i := range items {
		count := counts[string(items[i].ID)]
		items[i].HomeRowCount, items[i].RowCount = new(count.Home), new(count.Total)
	}
	return nil
}
