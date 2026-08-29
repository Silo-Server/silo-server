package sections

import (
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// ErrNoFetcher is returned when InstallRecipeDelegate(nil) was called and a recipe is resolved.
var ErrNoFetcher = errors.New("sections: recipe delegate has no fetcher")

// InstallRecipeDelegate wires the recipes package back into package sections.
// Called once at server startup with the live *Fetcher.
func InstallRecipeDelegate(f *Fetcher) {
	recipes.SetDelegate(func(typ string, rc recipes.ResolverContext) (recipes.ResolvedItems, error) {
		if f == nil {
			return recipes.ResolvedItems{}, ErrNoFetcher
		}
		return dispatchRecipe(f, typ, rc)
	})
}

// dispatchRecipe builds a ResolvedSection from the recipe context, calls the existing FetchOne,
// and converts the result back to recipes.ResolvedItems. This is byte-for-byte equivalent to
// the previous direct dispatch in FetchOne for the listed types.
func dispatchRecipe(f *Fetcher, typ string, rc recipes.ResolverContext) (recipes.ResolvedItems, error) {
	resolved := ResolvedSection{
		ID:          "", // dispatcher fills in upstream
		SectionType: SectionType(typ),
		Title:       rc.Title,
		ItemLimit:   rc.ItemLimit,
		Config:      rc.Params,
	}

	libraryID := rc.Library.LibraryID
	libraryIDs := rc.Library.LibraryIDs

	withItems, err := f.FetchOne(rc.Ctx, resolved, libraryID, libraryIDs, rc.UserID, rc.ProfileID, rc.Filter)
	if err != nil {
		return recipes.ResolvedItems{}, fmt.Errorf("recipe %s: %w", typ, err)
	}

	out := recipes.ResolvedItems{
		Items:      withItems.Items,
		TotalCount: withItems.TotalCount,
		ItemMeta:   convertItemMeta(withItems.ItemMeta),
	}
	return out, nil
}

func convertItemMeta(in map[string]SectionItemMeta) map[string]recipes.SectionItemMeta {
	if in == nil {
		return nil
	}
	out := make(map[string]recipes.SectionItemMeta, len(in))
	for k, v := range in {
		out[k] = recipes.SectionItemMeta{
			SeriesID:          v.SeriesID,
			SeriesTitle:       v.SeriesTitle,
			SeasonNumber:      v.SeasonNumber,
			EpisodeNumber:     v.EpisodeNumber,
			PosterOwner:       convertArtworkOwner(v.PosterOwner),
			BackdropOwner:     convertArtworkOwner(v.BackdropOwner),
			LogoOwner:         convertArtworkOwner(v.LogoOwner),
			Badges:            v.Badges,
			PositionSeconds:   v.PositionSeconds,
			DurationSeconds:   v.DurationSeconds,
			ProgressUpdatedAt: v.ProgressUpdatedAt,
			ItemSource:        v.ItemSource,
			SortTimestamp:     v.SortTimestamp,
		}
	}
	return out
}

func convertArtworkOwner(owner SectionArtworkOwner) recipes.SectionArtworkOwner {
	return recipes.SectionArtworkOwner{Kind: string(owner.Kind), ContentID: owner.ContentID}
}

// AccessFilterFromContext is a convenience for tests/callers that need to build a context.
func AccessFilterFromContext(rc recipes.ResolverContext) catalog.AccessFilter {
	return rc.Filter
}
