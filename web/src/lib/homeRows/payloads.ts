import {
  queryDefinitionToSectionConfig,
  type PageSectionConfig,
  type QueryDefinition,
  type SettingsSectionEntry,
} from "@/api/types";
import type { BulkCreateAdminSections } from "@/api/adminSections";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import {
  finalizeSectionLibraryFilter,
  LIBRARY_FILTER_SECTION_TYPES,
} from "@/lib/sectionLibraryFilter";
import { FILTER_SECTION_TYPES, sectionTypeLabel } from "@/lib/sectionTypes";
import { randomUUID } from "@/lib/uuid";

/** What the recipe gallery's config drawer hands its owner when the user adds a row. */
export interface AddPayload {
  section_type: string;
  title: string;
  item_limit: number;
  featured: boolean;
  enabled: boolean;
  config: Record<string, unknown>;
  apply_to_all_libraries?: boolean;
  library_ids?: number[];
}

interface BuildGalleryAddPayloadInput {
  sectionType: string;
  title: string;
  itemLimit: number;
  featured: boolean;
  enabled: boolean;
  config: Record<string, unknown>;
}

/**
 * The gallery drawer's add payload. With `bulkLibraryIds` it is the payload
 * the "Apply to all libraries" confirmation sends.
 */
export function buildGalleryAddPayload(
  { sectionType, title, itemLimit, featured, enabled, config }: BuildGalleryAddPayloadInput,
  bulkLibraryIds?: number[],
): AddPayload {
  const payload = {
    section_type: sectionType,
    title,
    item_limit: itemLimit,
    featured,
    enabled,
    config,
  };
  return bulkLibraryIds
    ? { ...payload, apply_to_all_libraries: true, library_ids: bulkLibraryIds }
    : { ...payload, apply_to_all_libraries: false };
}

/** The admin create request for one gallery add on the page the admin has open. */
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

/** The admin bulk create request for a gallery add applied to several library pages. */
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

/** A gallery add on Settings > Home Screen, as a new profile-owned row. */
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

function preserveGeneratedSectionMetadata(
  existingConfig: Record<string, unknown> | undefined,
  nextConfig: Record<string, unknown>,
): Record<string, unknown> {
  if (!existingConfig) {
    return nextConfig;
  }

  const merged = { ...nextConfig };
  if (typeof existingConfig.generated_source === "string" && existingConfig.generated_source) {
    merged.generated_source = existingConfig.generated_source;
  }
  if (
    typeof existingConfig.filter_library_id === "number" &&
    Number.isInteger(existingConfig.filter_library_id)
  ) {
    merged.filter_library_id = existingConfig.filter_library_id;
  }
  return merged;
}

export interface BuildProfileSectionSaveEntryInput {
  section: SettingsSectionEntry | null;
  sectionType: string;
  title: string;
  itemLimit: number;
  featured: boolean;
  queryDefinition: QueryDefinition;
  selectedCollectionId: string;
  recipeParams?: Record<string, unknown>;
  collections?: CollectionOption[];
}

export function buildProfileSectionSaveEntry({
  section,
  sectionType,
  title,
  itemLimit,
  featured,
  queryDefinition,
  selectedCollectionId,
  recipeParams,
  collections,
}: BuildProfileSectionSaveEntryInput): SettingsSectionEntry {
  let config: Record<string, unknown>;
  if (sectionType === "collection") {
    const selected = collections?.find((collection) => collection.id === selectedCollectionId);
    config =
      selected?.source === "user"
        ? { user_collection_id: selectedCollectionId }
        : { library_collection_id: selectedCollectionId };
  } else if (FILTER_SECTION_TYPES.has(sectionType)) {
    config = preserveGeneratedSectionMetadata(
      section?.config,
      queryDefinitionToSectionConfig(queryDefinition),
    );
  } else if (recipeParams && LIBRARY_FILTER_SECTION_TYPES.has(sectionType)) {
    // The params start from the section config and the library picker owns the
    // filter keys, so restoring the old filter_library_id would widen the selection.
    config = finalizeSectionLibraryFilter(recipeParams);
  } else {
    config = preserveGeneratedSectionMetadata(section?.config, recipeParams ?? {});
  }

  return {
    id: section?.id ?? randomUUID(),
    section_type: sectionType,
    title: title || sectionTypeLabel(sectionType),
    featured,
    item_limit: itemLimit,
    hidden: section?.hidden ?? false,
    is_custom: section?.is_custom ?? true,
    customized: section?.customized ?? false,
    position: section?.position ?? 0,
    config,
  };
}

export interface BuildAdminSectionPayloadInput {
  section: PageSectionConfig | null;
  scope: string;
  currentLibraryId: number | null;
  sectionType: string;
  title: string;
  itemLimit: number;
  featured: boolean;
  enabled: boolean;
  queryDefinition: QueryDefinition;
  selectedCollectionId: string;
  recipeParams?: Record<string, unknown>;
  collections?: CollectionOption[];
}

export function buildAdminSectionPayload({
  section,
  scope,
  currentLibraryId,
  sectionType,
  title,
  itemLimit,
  featured,
  enabled,
  queryDefinition,
  selectedCollectionId,
  recipeParams,
}: BuildAdminSectionPayloadInput): Partial<PageSectionConfig> & { id?: string } {
  const base = section?.section_type === sectionType ? { ...section.config } : {};
  let config: Record<string, unknown>;
  if (sectionType === "collection") {
    delete base.user_collection_id;
    config = { ...base, library_collection_id: selectedCollectionId };
  } else if (FILTER_SECTION_TYPES.has(sectionType)) {
    // The editor replaces query fields, while keeping recipe metadata it does not edit.
    delete base.filter_type;
    delete base.filter_library_id;
    delete base.filter_library_ids;
    delete base.order;
    config = { ...base, ...queryDefinitionToSectionConfig(queryDefinition) };
  } else if (recipeParams && LIBRARY_FILTER_SECTION_TYPES.has(sectionType)) {
    // The library picker owns the filter keys; keeping the old ones from base
    // would re-add a replaced filter_library_id.
    delete base.filter_library_id;
    delete base.filter_library_ids;
    delete base.library_ids;
    config = finalizeSectionLibraryFilter({ ...base, ...recipeParams });
  } else {
    config = { ...base, ...recipeParams };
  }

  const safeTitle = title.trim() || sectionTypeLabel(sectionType);

  return {
    ...(section ? { id: section.id } : {}),
    scope,
    ...(scope === "library" && currentLibraryId != null ? { library_id: currentLibraryId } : {}),
    title: safeTitle,
    section_type: sectionType,
    item_limit: itemLimit,
    featured,
    enabled,
    config,
  };
}
