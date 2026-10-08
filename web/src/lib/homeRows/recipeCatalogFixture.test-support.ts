import type { components } from "@/api/v2/schema";
import {
  recipeCatalogFromV2,
  type GalleryPreset,
  type RecipeCatalogResponse,
  type RecipeDefinition,
} from "@/lib/recipes";

// The real GET /api/v2/sections/recipes body. The Go test
// TestRecipeCatalogMatchesWebFixture keeps it in step with the recipe registry.
import body from "./__fixtures__/recipeCatalog.v2.json";

export const recipeCatalogFixture: RecipeCatalogResponse = recipeCatalogFromV2(
  body as components["schemas"]["RecipeCatalog"],
);

export interface CatalogPreset {
  def: RecipeDefinition;
  preset: GalleryPreset;
}

/** Every recipe definition in the fixture, in catalog order. */
export function everyRecipe(catalog = recipeCatalogFixture): RecipeDefinition[] {
  return Object.values(catalog.categories).flatMap((defs) => defs ?? []);
}

/** Every (recipe, preset) pair in the fixture, in catalog order. */
export function everyPreset(catalog = recipeCatalogFixture): CatalogPreset[] {
  return everyRecipe(catalog).flatMap((def) => def.presets.map((preset) => ({ def, preset })));
}
