import { v2 } from "../api/v2/request";

export type Category =
  | "library_staples"
  | "personalized"
  | "discovery"
  | "editorial"
  | "seasonal"
  | "mood"
  | "hand_picked"
  | "social"
  | "custom";

export interface GalleryPreset {
  key: string;
  display_name: string;
  icon: string;
  description_short: string;
  description_long?: string;
  default_params: Record<string, unknown>;
}

export interface RecipeDefinition {
  type: string;
  category: Category;
  presets: GalleryPreset[];
  avoid_duplicates: boolean;
  supports_rotation: boolean;
  admin_only: boolean;
}

export interface RecipeCatalogResponse {
  categories: Partial<Record<Category, RecipeDefinition[]>>;
}

// matchRecipePreset returns the gallery preset a section's config came from.
// Several presets can share one recipe type and differ only in their params
// (TMDB Trending Today vs This Week), so the type alone cannot name the
// section. The preset whose default params the config matches on the most
// keys wins; a config that matches none falls back to the first preset.
export function matchRecipePreset(
  def: RecipeDefinition,
  config: Record<string, unknown> | undefined,
): GalleryPreset | undefined {
  let best: GalleryPreset | undefined;
  let bestScore = -1;
  for (const preset of def.presets) {
    const entries = Object.entries(preset.default_params ?? {});
    const matches = entries.every(
      ([key, value]) => JSON.stringify(config?.[key]) === JSON.stringify(value),
    );
    if (matches && entries.length > bestScore) {
      best = preset;
      bestScore = entries.length;
    }
  }
  return best ?? def.presets[0];
}

export interface Candidate {
  value: string;
  display_name: string;
  subtitle?: string;
}

export interface PreviewRequest {
  section_type: string;
  config: Record<string, unknown>;
  item_limit?: number;
  library_id?: number;
  library_ids?: number[];
}

export interface PreviewResponse {
  items: Array<{ content_id: string; title?: string; poster_path?: string }>;
  total_count: number;
}

export async function fetchRecipeCatalog(): Promise<RecipeCatalogResponse> {
  const catalog = await v2("GET /api/v2/sections/recipes");
  const categories: Partial<Record<Category, RecipeDefinition[]>> = {};
  for (const group of catalog.categories) {
    categories[group.category as Category] = group.recipes.map((def) => ({
      type: def.type,
      category: def.category as Category,
      presets: def.presets.map((preset) => ({
        key: preset.key,
        display_name: preset.display_name,
        icon: preset.icon,
        description_short: preset.description_short,
        ...(preset.description_long ? { description_long: preset.description_long } : {}),
        default_params: preset.default_params,
      })),
      avoid_duplicates: def.avoid_duplicates,
      supports_rotation: def.supports_rotation,
      admin_only: def.admin_only,
    }));
  }
  return { categories };
}

export async function fetchCandidates(recipeType: string): Promise<Candidate[]> {
  const body = await v2("GET /api/v2/sections/recipes/{type}/candidates", {
    path: { type: recipeType },
  });
  return body.candidates.map((candidate) => ({
    value: candidate.value,
    display_name: candidate.display_name,
    ...(candidate.subtitle ? { subtitle: candidate.subtitle } : {}),
  }));
}

export async function previewSection(req: PreviewRequest): Promise<PreviewResponse> {
  const result = await v2("POST /api/v2/admin/sections/preview", {
    body: {
      ...req,
      library_id: req.library_id === undefined ? undefined : String(req.library_id),
      library_ids: req.library_ids?.map(String),
    },
  });
  return {
    items: result.items.map((item) => ({
      content_id: item.content_id,
      title: item.title,
      poster_path: item.poster_url,
    })),
    total_count: result.total_count,
  };
}
