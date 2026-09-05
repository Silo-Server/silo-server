import { api } from "../api/client";
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
  return api<PreviewResponse>("/admin/sections/preview", {
    method: "POST",
    body: JSON.stringify(req),
  });
}
