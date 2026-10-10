import type { BulkCreateAdminSections } from "@/api/adminSections";
import {
  queryDefinitionToSectionConfig,
  type PageSectionConfig,
  type QueryDefinition,
  type SettingsSectionEntry,
} from "@/api/types";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import {
  finalizeSectionLibraryFilter,
  LIBRARY_FILTER_SECTION_TYPES,
} from "@/lib/sectionLibraryFilter";
import { FILTER_SECTION_TYPES } from "@/lib/sectionTypes";
import { randomUUID } from "@/lib/uuid";
import { rowKindLabel } from "./catalog";
import type { RowDraft } from "./rowDraft";
import { stableJson } from "./stableJson";
import type { PageRef, Surface } from "./types";

/** A new row's fields, before they become an admin create or a profile row. */
export interface AddPayload {
  section_type: string;
  title: string;
  item_limit: number;
  featured: boolean;
  enabled: boolean;
  config: Record<string, unknown>;
}

interface BuildGalleryAddPayloadInput {
  sectionType: string;
  title: string;
  itemLimit: number;
  featured: boolean;
  enabled: boolean;
  config: Record<string, unknown>;
}

/** A new row from a preset or an Add row draft: the fields as given, config untouched. */
export function buildGalleryAddPayload({
  sectionType,
  title,
  itemLimit,
  featured,
  enabled,
  config,
}: BuildGalleryAddPayloadInput): AddPayload {
  return {
    section_type: sectionType,
    title,
    item_limit: itemLimit,
    featured,
    enabled,
    config,
  };
}

/** The admin create request for one new row on the page the admin has open. */
export function buildGalleryCreateRequest(
  payload: AddPayload,
  scope: string,
  activeLibraryId: number | null,
): Partial<PageSectionConfig> {
  return {
    scope,
    ...(scope === "library" && activeLibraryId != null ? { library_id: activeLibraryId } : {}),
    section_type: payload.section_type,
    title: payload.title,
    item_limit: payload.item_limit,
    featured: payload.featured,
    enabled: payload.enabled,
    config: payload.config,
  };
}

/** The admin bulk create request that adds one row to several library pages. */
export function buildGalleryBulkCreateRequest(
  payload: AddPayload,
  libraryIds: number[],
): BulkCreateAdminSections {
  return {
    scope: "library",
    library_ids: libraryIds,
    section_type: payload.section_type,
    title: payload.title,
    item_limit: payload.item_limit,
    featured: payload.featured,
    enabled: payload.enabled,
    config: payload.config,
  };
}

/** A new row on Settings > Home Screen, as a profile-owned row. */
export function buildProfileGallerySection(
  payload: AddPayload,
  position: number,
): SettingsSectionEntry {
  return {
    id: randomUUID(),
    section_type: payload.section_type,
    title: payload.title,
    featured: payload.featured,
    item_limit: payload.item_limit,
    hidden: false,
    is_custom: true,
    customized: true,
    position,
    config: payload.config,
  };
}

/**
 * The collection a collection row's config points at, or "" for none. Admin
 * rows read only `library_collection_id`: the admin endpoint rejects a row
 * without it, so a legacy `user_collection_id` there counts as no selection.
 */
export function collectionIdOf(
  config?: Record<string, unknown>,
  surface: Surface = "profile",
): string {
  const userValue = config?.user_collection_id;
  if (surface === "profile" && typeof userValue === "string" && userValue) return userValue;
  const libraryValue = config?.library_collection_id;
  return typeof libraryValue === "string" ? libraryValue : "";
}

/**
 * A collection row's config after an edit. An unchanged selection keeps the
 * stored config byte for byte, so a row whose collection isn't in the picker
 * list (still loading, or shared and since gone) keeps its id key. A newly
 * picked collection replaces both id keys with `key` and keeps the rest.
 * `stored` is the row's config only when the row already was a collection row;
 * `surface` decides which stored key counts as its selection (see collectionIdOf).
 */
function collectionRowConfig(
  stored: Record<string, unknown> | undefined,
  selectedCollectionId: string,
  key: "user_collection_id" | "library_collection_id",
  surface: Surface,
): Record<string, unknown> {
  if (stored && selectedCollectionId && collectionIdOf(stored, surface) === selectedCollectionId) {
    return { ...stored };
  }
  const rest = { ...stored };
  delete rest.user_collection_id;
  delete rest.library_collection_id;
  return { ...rest, [key]: selectedCollectionId };
}

/**
 * A collection row's config after the user picks `option`: the key comes
 * from the option's source, and every key other than the two id keys stays.
 * Picking the collection the row already shows keeps the config as it is.
 */
export function withPickedCollection(
  config: Record<string, unknown>,
  option: Pick<CollectionOption, "id" | "source">,
): Record<string, unknown> {
  if (collectionIdOf(config) === option.id) return config;
  return collectionRowConfig(
    config,
    option.id,
    option.source === "user" ? "user_collection_id" : "library_collection_id",
    "profile",
  );
}

/**
 * A rule row's config with new rules: the query keys are replaced (legacy
 * filter_type, filter_library_id(s) and order fold into today's shape) and
 * recipe metadata the rules don't touch stays.
 */
export function withQueryDefinition(
  config: Record<string, unknown>,
  query: QueryDefinition,
): Record<string, unknown> {
  const rest = { ...config };
  delete rest.filter_type;
  delete rest.filter_library_id;
  delete rest.filter_library_ids;
  delete rest.order;
  return { ...rest, ...queryDefinitionToSectionConfig(query) };
}

/** Where a new single row goes: after every row on the page (0 on an empty page). */
export function nextAppendPosition(positions: readonly number[]): number {
  return positions.length === 0 ? 0 : Math.max(...positions) + 1;
}

/**
 * The create request for a row added from the Add row dialog: the same body
 * `buildGalleryCreateRequest` builds for a preset, plus `position`. The server
 * stores the position a single create sends, so without it a new row would
 * land near the top of the page.
 */
export function buildRowCreateRequest(
  draft: RowDraft,
  title: string,
  page: PageRef,
  position: number,
): Partial<PageSectionConfig> {
  const payload = buildGalleryAddPayload({
    sectionType: draft.sectionType,
    title,
    itemLimit: draft.itemLimit,
    featured: draft.hero,
    enabled: true,
    config: draft.config,
  });
  return {
    ...buildGalleryCreateRequest(
      payload,
      page.kind,
      page.kind === "library" ? page.libraryId : null,
    ),
    position,
  };
}

/** What a library page copy is made from: a new row's draft or an existing row. */
export interface BulkCopySource {
  sectionType: string;
  title: string;
  itemLimit: number;
  config: Record<string, unknown>;
  enabled: boolean;
}

/**
 * The bulk create request that puts a row on each of `libraryIds`. Each page
 * gets its own copy at its bottom. Copies are never hero banners, so a copy
 * can't add a second hero to another page.
 */
export function buildBulkCopyPayload(
  { sectionType, title, itemLimit, config, enabled }: BulkCopySource,
  libraryIds: number[],
): BulkCreateAdminSections {
  return buildGalleryBulkCreateRequest(
    buildGalleryAddPayload({ sectionType, title, itemLimit, featured: false, enabled, config }),
    libraryIds,
  );
}

/** Kinds whose form edits the config itself: the picked collection's id key, the rules. */
const FORM_OWNED_CONFIG_TYPES: ReadonlySet<string> = new Set([
  "collection",
  ...FILTER_SECTION_TYPES,
]);

function rowUpdateConfig(draft: RowDraft): Record<string, unknown> {
  if (FORM_OWNED_CONFIG_TYPES.has(draft.sectionType)) return draft.config;
  return LIBRARY_FILTER_SECTION_TYPES.has(draft.sectionType)
    ? finalizeSectionLibraryFilter(draft.config)
    : { ...draft.config };
}

/**
 * The update request for a row saved from Edit row: the row editor's bytes.
 * The draft starts from the stored config; keys removed by a variant change
 * stay removed. Collection and rule rows send the draft's config as it is, so an
 * untouched one saves exactly as stored. `enabled` comes from the version
 * being saved over.
 */
export function buildRowUpdateRequest(
  section: PageSectionConfig,
  draft: RowDraft,
  title: string,
): Partial<PageSectionConfig> & { id?: string } {
  return {
    id: section.id,
    scope: section.scope,
    ...(section.scope === "library" && section.library_id != null
      ? { library_id: section.library_id }
      : {}),
    title: title.trim() || rowKindLabel(draft.sectionType),
    section_type: draft.sectionType,
    item_limit: draft.itemLimit,
    featured: draft.hero,
    enabled: section.enabled,
    config: rowUpdateConfig(draft),
  };
}

/** A row added on Settings > Home Screen, as this profile's own. */
export function buildProfileRowCreate(
  draft: RowDraft,
  title: string,
  position: number,
): SettingsSectionEntry {
  return buildProfileGallerySection(
    buildGalleryAddPayload({
      sectionType: draft.sectionType,
      title,
      itemLimit: draft.itemLimit,
      featured: draft.hero,
      enabled: true,
      config: draft.config,
    }),
    position,
  );
}

/**
 * A row saved from Edit row on Settings > Home Screen. A config the user
 * didn't change stays the stored object, so a rename never stores (and pins) the row's config; collection and rule
 * rows send the draft's config as it is.
 */
export function buildProfileRowUpdate(
  section: SettingsSectionEntry,
  draft: RowDraft,
  title: string,
): SettingsSectionEntry {
  const config =
    draft.sectionType === section.section_type &&
    stableJson(draft.config) === stableJson(section.config ?? {})
      ? section.config
      : rowUpdateConfig(draft);
  return {
    id: section.id,
    section_type: draft.sectionType,
    title: title || rowKindLabel(draft.sectionType),
    featured: draft.hero,
    item_limit: draft.itemLimit,
    hidden: section.hidden ?? false,
    is_custom: section.is_custom ?? true,
    customized: section.customized ?? false,
    position: section.position ?? 0,
    default_title: section.default_title,
    config,
  };
}
