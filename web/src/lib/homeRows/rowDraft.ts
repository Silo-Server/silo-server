import { queryDefinitionFromSectionConfig, type QueryDefinition } from "@/api/types";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import type { GalleryPreset, RecipeCatalogResponse, RecipeDefinition } from "@/lib/recipes";
import { FILTER_SECTION_TYPES } from "@/lib/sectionTypes";
import { rowKindLabel } from "./catalog";
import { collectionIdOf, withPickedCollection, withQueryDefinition } from "./payloads";
import { stableJson } from "./stableJson";
import type { HomeRow, Surface } from "./types";
import { applyVariant, variantFamily, variantOf } from "./variants";

/** What the Add row / Edit row form holds while the user edits it. */
export interface RowDraft {
  sectionType: string;
  title: string;
  /** The name is still the preset's, so picking another variant renames the row. */
  titleFollowsVariant: boolean;
  config: Record<string, unknown>;
  itemLimit: number;
  hero: boolean;
  /** Add row on a library page: the other library pages that get their own copy. */
  extraLibraryIds?: number[];
}

export const DEFAULT_ITEM_LIMIT = 20;

export function findRecipe(
  catalog: RecipeCatalogResponse | undefined,
  sectionType: string,
): RecipeDefinition | undefined {
  for (const defs of Object.values(catalog?.categories ?? {})) {
    const found = defs?.find((def) => def.type === sectionType);
    if (found) return found;
  }
  return undefined;
}

function presetName(def: RecipeDefinition | undefined, presetKey: string | null) {
  return presetKey
    ? def?.presets.find((preset) => preset.key === presetKey)?.display_name
    : undefined;
}

/**
 * A new row from a picked preset: exactly the preset's params, as the gallery
 * added them. A collection row is named after its collection once one is
 * picked; a rule row starts with no rules, in the shape the rule editor saves.
 */
export function draftForPreset(def: RecipeDefinition, preset: GalleryPreset | undefined): RowDraft {
  const params = { ...(preset?.default_params ?? {}) };
  const collection = def.type === "collection";
  return {
    sectionType: def.type,
    title: collection ? "" : (preset?.display_name ?? rowKindLabel(def.type)),
    titleFollowsVariant: collection || variantFamily(def.type) !== undefined,
    config: FILTER_SECTION_TYPES.has(def.type)
      ? withQueryDefinition(params, queryDefinitionFromSectionConfig(params))
      : params,
    itemLimit: DEFAULT_ITEM_LIMIT,
    hero: false,
  };
}

/**
 * Picks the collection a collection row shows. The name follows the pick
 * while it is blank, still following, or still the old collection's name.
 */
export function withCollection(
  draft: RowDraft,
  option: Pick<CollectionOption, "id" | "title" | "source">,
  currentTitle?: string,
): RowDraft {
  const follows =
    draft.titleFollowsVariant ||
    draft.title.trim() === "" ||
    (currentTitle !== undefined && draft.title === currentTitle);
  return {
    ...draft,
    config: withPickedCollection(draft.config, option),
    ...(follows ? { title: option.title, titleFollowsVariant: true } : {}),
  };
}

/** Replaces a rule row's rules; config keys the rules don't own stay. */
export function withRules(draft: RowDraft, query: QueryDefinition): RowDraft {
  return { ...draft, config: withQueryDefinition(draft.config, query) };
}

/** An existing row as the form starts it. Opening a row never changes its config. */
export function draftFromRow(row: HomeRow, catalog: RecipeCatalogResponse | undefined): RowDraft {
  const name = presetName(
    findRecipe(catalog, row.sectionType),
    variantOf(row.sectionType, row.config),
  );
  return {
    sectionType: row.sectionType,
    title: row.title,
    titleFollowsVariant: name !== undefined && name === row.title,
    config: { ...row.config },
    itemLimit: row.itemLimit,
    hero: row.hero,
  };
}

/** Picks another variant; the name follows it until the user typed their own. */
export function withVariant(
  draft: RowDraft,
  def: RecipeDefinition | undefined,
  presetKey: string,
): RowDraft {
  const name = presetName(def, presetKey);
  return {
    ...draft,
    config: applyVariant(draft.sectionType, draft.config, presetKey),
    title: draft.titleFollowsVariant && name ? name : draft.title,
  };
}

export function withTitle(draft: RowDraft, title: string): RowDraft {
  return { ...draft, title, titleFollowsVariant: false };
}

/**
 * The name a save sends. A blank name falls back to `fallback` (a collection
 * row's collection), the variant's preset name, then the kind's plain name,
 * never a raw type key.
 */
export function savedTitle(
  draft: RowDraft,
  catalog: RecipeCatalogResponse | undefined,
  fallback?: string,
): string {
  const typed = draft.title.trim() || fallback?.trim();
  if (typed) return typed;
  const def = findRecipe(catalog, draft.sectionType);
  const variant = variantOf(draft.sectionType, draft.config);
  return (
    presetName(def, variant) ??
    (def?.presets.length === 1 ? def.presets[0]?.display_name : undefined) ??
    rowKindLabel(draft.sectionType)
  );
}

/**
 * Whether the server would accept the draft; the form says what is missing.
 * `surface` decides which collection key counts as a selection (see collectionIdOf).
 */
export function canSaveDraft(draft: RowDraft, surface: Surface = "profile"): boolean {
  const { config } = draft;
  switch (draft.sectionType) {
    case "seasonal_themed":
      // An empty holiday list with no legacy theme.
      return !(Array.isArray(config.enabled_themes) && config.enabled_themes.length === 0);
    case "collection":
      return collectionIdOf(config, surface) !== "";
    case "admin_curated_list":
      return Array.isArray(config.item_ids) && config.item_ids.length > 0;
    default:
      return true;
  }
}

/**
 * Why a draft has nothing to preview yet, or null: a collection row before a
 * collection is picked (the server refuses that preview), or an Editor's
 * Picks row with no titles.
 */
export function previewWaitText(draft: RowDraft, surface: Surface = "profile"): string | null {
  if (draft.sectionType === "collection" && collectionIdOf(draft.config, surface) === "")
    return "Pick a collection to see its titles here.";
  if (draft.sectionType === "admin_curated_list" && !canSaveDraft(draft))
    return "Add titles to see them here.";
  return null;
}

export type DraftField = "title" | "shows" | "itemLimit" | "hero";

export const DRAFT_FIELD_LABELS: Record<DraftField, string> = {
  title: "Row name",
  shows: "What it shows",
  itemLimit: "Number of titles",
  hero: "Hero banner",
};

const DRAFT_FIELDS: readonly DraftField[] = ["title", "shows", "itemLimit", "hero"];

function fieldValue(draft: RowDraft, field: DraftField): string {
  switch (field) {
    case "title":
      return draft.title;
    case "shows":
      return `${draft.sectionType}\n${stableJson(draft.config)}`;
    case "itemLimit":
      return String(draft.itemLimit);
    case "hero":
      return String(draft.hero);
  }
}

function changedFields(a: RowDraft, b: RowDraft): DraftField[] {
  return DRAFT_FIELDS.filter((field) => fieldValue(a, field) !== fieldValue(b, field));
}

function copyField(target: RowDraft, source: RowDraft, field: DraftField): RowDraft {
  switch (field) {
    case "title":
      return { ...target, title: source.title, titleFollowsVariant: source.titleFollowsVariant };
    case "shows":
      return { ...target, sectionType: source.sectionType, config: source.config };
    case "itemLimit":
      return { ...target, itemLimit: source.itemLimit };
    case "hero":
      return { ...target, hero: source.hero };
  }
}

/**
 * After a save is refused because the row changed elsewhere: every field the
 * user changed keeps their value, every other field takes the row's new
 * value. `changedUpstream` names what changed elsewhere, including fields
 * where the user's value won.
 */
export function mergeReloadedDraft(
  original: RowDraft,
  edited: RowDraft,
  upstream: RowDraft,
): { draft: RowDraft; changedUpstream: DraftField[] } {
  const userChanged = new Set(changedFields(original, edited));
  const draft = DRAFT_FIELDS.reduce(
    (next, field) => (userChanged.has(field) ? next : copyField(next, upstream, field)),
    edited,
  );
  return { draft, changedUpstream: changedFields(original, upstream) };
}
