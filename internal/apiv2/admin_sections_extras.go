package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

func (reg *Registry) getAdminSectionCapabilities(ctx context.Context, _ *CapabilityInput) (*AdminSectionCapabilitiesOutput, error) {
	out := &AdminSectionCapabilitiesOutput{}
	if reg.deps.AdminSections == nil {
		return out, nil
	}
	caps := reg.deps.AdminSections.AdminSectionCapabilities(ctx)
	out.Body = AdminSectionCapabilities{Available: true, ResetProfiles: caps.ResetProfiles, Preview: caps.Preview}
	return out, nil
}

// adminSectionLibraryIDs converts the wire ids to service ids. It returns nil
// for an absent or empty list: the section fetcher reads a non-nil empty slice
// as "scoped to zero libraries" and answers with no items, while nil means
// unscoped.
func adminSectionLibraryIDs(ids []ID) ([]int, *Problem) {
	if len(ids) == 0 {
		return nil, nil
	}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		n, p := libraryID(id)
		if p != nil {
			return nil, p
		}
		out = append(out, n)
	}
	return out, nil
}
func (reg *Registry) bulkCreateAdminSections(ctx context.Context, in *AdminSectionBulkInput) (*AdminSectionBulkOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	if reg.deps.AdminSections == nil {
		return nil, unavailable("admin sections")
	}
	if in.Body.Scope == scopeHome && len(in.Body.LibraryIDs) > 0 {
		return nil, NewProblem(TypeValidationFailed, "library_ids must be omitted for home sections.")
	}
	if in.Body.Scope == scopeLibrary && len(in.Body.LibraryIDs) == 0 {
		return nil, NewProblem(TypeValidationFailed, "library_ids must not be empty for library sections.")
	}
	ids, p := adminSectionLibraryIDs(in.Body.LibraryIDs)
	if p != nil {
		return nil, p
	}
	value, err := reg.deps.AdminSections.BulkCreateAdminSections(ctx, handlers.AdminSectionBulkCreate{Scope: in.Body.Scope, LibraryIDs: ids, SectionType: in.Body.SectionType, Title: in.Body.Title, Featured: in.Body.Featured, ItemLimit: in.Body.ItemLimit, Config: adminSectionConfig(in.Body.Config), Enabled: in.Body.Enabled})
	if err != nil {
		return nil, adminSectionError(err)
	}
	return &AdminSectionBulkOutput{Body: AdminSectionBulkResult{Created: value.Created}}, nil
}
func (reg *Registry) previewAdminSection(ctx context.Context, in *AdminSectionPreviewInput) (*AdminSectionPreviewOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	if reg.deps.AdminSections == nil {
		return nil, unavailable("admin sections")
	}
	if !reg.deps.AdminSections.AdminSectionCapabilities(ctx).Preview {
		return nil, NewProblem(TypeCapabilityUnsupported, "Section preview is not supported by this service.")
	}
	ids, p := adminSectionLibraryIDs(in.Body.LibraryIDs)
	if p != nil {
		return nil, p
	}
	var library *int
	if in.Body.LibraryID != nil {
		n, p := libraryID(*in.Body.LibraryID)
		if p != nil {
			return nil, p
		}
		library = &n
	}
	value, err := reg.deps.AdminSections.PreviewAdminSection(ctx, handlers.AdminSectionPreviewRequest{SectionType: in.Body.SectionType, Config: adminSectionConfig(in.Body.Config), ItemLimit: in.Body.ItemLimit, LibraryID: library, LibraryIDs: ids})
	if err != nil {
		return nil, adminSectionError(err)
	}
	out := AdminSectionPreviewResult{Collection: NewCollection([]CatalogItem{}), TotalCount: value.TotalCount}
	for _, item := range value.Items {
		if item == nil {
			continue
		}
		// Preview models carry internal artwork storage keys, not public URLs.
		// Expose the stable catalog card fields, without serializing storage state.
		out.Items = append(out.Items, CatalogItem{ContentID: item.ContentID, Type: item.Type, Title: item.Title, PlayContentID: item.PlayContentID, Year: item.Year, Runtime: item.Runtime, Genres: NonNil(item.Genres), Keywords: NonNil(item.Keywords), Studios: item.Studios, Networks: item.Networks, ContentRating: item.ContentRating, AdvisoryAge: item.AdvisoryAge, AdvisorySource: item.AdvisorySource, Status: item.Status, ShowStatus: item.ShowStatus, RatingIMDB: item.RatingIMDB, RatingTMDB: item.RatingTMDB, RatingRTCritic: item.RatingRTCritic, RatingRTAudience: item.RatingRTAudience, OriginalLanguage: item.OriginalLanguage, Overview: item.Overview, ReleaseDate: item.ReleaseDate, LastAirDate: item.LastAirDate, PosterThumbhash: item.PosterThumbhash, BackdropThumbhash: item.BackdropThumbhash})
	}
	return &AdminSectionPreviewOutput{Body: out}, nil
}
