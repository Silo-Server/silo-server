/**
 * The override set a profile saves for one page (`PUT /api/v2/profile/sections`)
 * and the page state it is built from. Pure, so the Home Screen hook and every
 * test build it the same way.
 */
import type { SectionOverride, SettingsSectionEntry } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import { isTraktConfig } from "@/lib/sectionTypes";
import { randomUUID } from "@/lib/uuid";
import { stableJson } from "./stableJson";

export interface RemovedSystemOverride {
  id: string;
}

export interface SectionOverrideIds {
  /** The profile's saved overrides for the page. */
  savedOverrides?: SectionOverride[];
  /** An ID for an admin section the profile has no saved override for. */
  newId?: (sectionId: string) => string;
  /** The section the change being saved is to, if it is to one section. */
  changedSectionId?: string;
  /** Every section the changes being saved are to, when several are saved at once. */
  changedSectionIds?: ReadonlySet<string>;
  /**
   * The page as it was read. With it, a save stores only what the profile
   * changed (see `buildSectionOverrides`); without it every field of every
   * row counts as changed.
   */
  baseline?: SettingsSectionEntry[];
}

type ServerRowFields = Pick<SectionOverride, "title" | "featured" | "item_limit" | "config">;

/**
 * What an override stores for a server row: each field the profile changed
 * since `base` was read, else what its saved override already stores (an
 * earlier save may have pinned it), else nothing, so the row keeps following
 * the server. A name changed back to the server's own clears the rename.
 */
function serverRowFields(
  s: SettingsSectionEntry,
  base: SettingsSectionEntry | undefined,
  saved: SectionOverride | undefined,
): ServerRowFields {
  const fields: ServerRowFields = {};
  if (!base || s.title !== base.title) {
    if (s.title !== s.default_title) fields.title = s.title;
  } else if (saved?.title) {
    fields.title = saved.title;
  }
  const featured = !base || s.featured !== base.featured ? s.featured : saved?.featured;
  if (featured !== undefined) fields.featured = featured;
  const itemLimit = !base || s.item_limit !== base.item_limit ? s.item_limit : saved?.item_limit;
  if (itemLimit !== undefined) fields.item_limit = itemLimit;
  const config =
    !base || stableJson(s.config) !== stableJson(base.config) ? s.config : saved?.config;
  if (config !== undefined) fields.config = config;
  return fields;
}

/** The ids of `a` in order, keeping only the ids `b` also has. */
function sharedOrder(a: SettingsSectionEntry[], b: SettingsSectionEntry[]): string[] {
  const ids = new Set(b.map((s) => s.id));
  return a.filter((s) => ids.has(s.id)).map((s) => s.id);
}

/**
 * Whether a row added since `baseline` was read has moved: rows are added at
 * the bottom with rising positions, so any other placement is the profile's.
 */
function movedNewRow(sections: SettingsSectionEntry[], baseline: SettingsSectionEntry[]): boolean {
  const known = new Set(baseline.map((s) => s.id));
  const firstNew = sections.findIndex((s) => !known.has(s.id));
  if (firstNew === -1) return false;
  const tail = sections.slice(firstNew);
  return tail.some(
    (s, index) => known.has(s.id) || (index > 0 && s.position < tail[index - 1]!.position),
  );
}

/**
 * The override set to save for one page. A change to an admin section keeps
 * the ID of the profile's saved override for that section, or gets one from
 * `newId`: the server's section source policy refuses a legacy Trakt admin
 * section's override without an ID. It also refuses a new override that
 * leaves such a section showing, so a shown one without a saved override ID is
 * left out and keeps its admin position, unless a change being saved is to
 * that section; the refusal then reaches the user instead of the change
 * silently not saving. Positions are only as close to the list order as that
 * held position allows.
 *
 * With a `baseline`, an admin section's override stores only what the
 * profile changed: whether it is hidden, the fields it edited (or an earlier
 * save stored), and positions only once the profile has ordered the page
 * itself. Admin sections it left alone get no override, so an admin's later
 * edit still reaches them. Rows the profile added always store every field.
 */
export function buildSectionOverrides(
  sections: SettingsSectionEntry[],
  removedSystemSections: RemovedSystemOverride[] = [],
  {
    savedOverrides = [],
    newId = () => randomUUID(),
    changedSectionId,
    changedSectionIds,
    baseline,
  }: SectionOverrideIds = {},
): SectionOverride[] {
  // The server resolves the last saved override for a section. One saved
  // without an ID still holds the profile's changes; it gets a new ID below.
  const saved = new Map<string, SectionOverride>();
  for (const override of savedOverrides) {
    if (override.section_id) saved.set(override.section_id, override);
  }
  const savedId = (sectionId: string) => saved.get(sectionId)?.id || newId(sectionId);
  const baseById = new Map(baseline?.map((s) => [s.id, s]));
  const changed = (id: string) => id === changedSectionId || Boolean(changedSectionIds?.has(id));
  const leftOut = (s: SettingsSectionEntry) =>
    !s.is_custom && !s.hidden && !saved.get(s.id)?.id && !changed(s.id) && isTraktConfig(s.config);
  // Positions follow the list once the profile orders the page; until then
  // admin sections keep the admin order and only added rows store a position.
  const ordered =
    !baseline ||
    savedOverrides.some((o) => o.section_id && o.position !== undefined) ||
    stableJson(sharedOrder(sections, baseline)) !== stableJson(sharedOrder(baseline, sections)) ||
    movedNewRow(sections, baseline);
  // A section left out keeps its admin position, so the others are numbered
  // in list order around it and never on it: the server orders sections with
  // equal positions arbitrarily.
  const heldPositions = new Set(sections.filter(leftOut).map((s) => s.position));
  const overrides: SectionOverride[] = [];
  let position = 0;
  for (const s of sections) {
    if (leftOut(s)) {
      position = Math.max(position, s.position + 1);
      continue;
    }
    while (heldPositions.has(position)) position += 1;
    const listPosition = position++;
    if (s.is_custom) {
      overrides.push({
        section_id: undefined,
        id: s.id,
        position: ordered ? listPosition : s.position,
        hidden: s.hidden,
        title: s.title,
        featured: s.featured,
        item_limit: s.item_limit,
        section_type: s.section_type,
        config: s.config,
      });
      continue;
    }
    const savedOverride = saved.get(s.id);
    const id = savedId(s.id);
    if (!baseline) {
      overrides.push({
        section_id: s.id,
        id,
        position: listPosition,
        hidden: s.hidden,
        title: s.title,
        featured: s.featured,
        item_limit: s.item_limit,
        section_type: undefined,
        config: s.config,
      });
      continue;
    }
    const fields = serverRowFields(s, baseById.get(s.id), savedOverride);
    if (!ordered && !savedOverride && !s.hidden && Object.keys(fields).length === 0) continue;
    overrides.push({
      section_id: s.id,
      id,
      ...(ordered ? { position: listPosition } : {}),
      hidden: s.hidden,
      ...fields,
    });
  }
  for (const section of removedSystemSections) {
    overrides.push({
      section_id: section.id,
      id: savedId(section.id),
      removed: true,
    });
  }
  return overrides;
}

/**
 * Gives each admin section one new override ID and returns the same one on
 * later calls, so a quick second save on a page reuses the IDs of the first
 * before the saved overrides refetch.
 */
export function createOverrideIdSource(): (sectionId: string) => string {
  const ids = new Map<string, string>();
  return (sectionId) => {
    const id = ids.get(sectionId) ?? randomUUID();
    ids.set(sectionId, id);
    return id;
  };
}

export function applySectionDeletion(
  sections: SettingsSectionEntry[],
  removedSystemSections: RemovedSystemOverride[],
  id: string,
): { sections: SettingsSectionEntry[]; removedSystemSections: RemovedSystemOverride[] } {
  const target = sections.find((section) => section.id === id);
  if (!target) {
    return { sections, removedSystemSections };
  }

  const nextSections = sections.filter((section) => section.id !== id);
  if (target.is_custom) {
    return { sections: nextSections, removedSystemSections };
  }

  if (removedSystemSections.some((section) => section.id === id)) {
    return { sections: nextSections, removedSystemSections };
  }

  return {
    sections: nextSections,
    removedSystemSections: [...removedSystemSections, { id }],
  };
}

export function hydrateRemovedSystemSections(
  overrides: SectionOverride[] = [],
): RemovedSystemOverride[] {
  return Array.from(
    new Set(
      overrides
        .filter((override) => override.removed && Boolean(override.section_id))
        .map((override) => override.section_id as string),
    ),
  ).map((id) => ({ id }));
}

interface ReadyQueryState {
  isSuccess: boolean;
  isError: boolean;
}

export function canMutateSectionSettings(
  settingsQuery?: ReadyQueryState,
  rawOverridesQuery?: ReadyQueryState,
): boolean {
  return Boolean(
    settingsQuery?.isSuccess &&
    !settingsQuery.isError &&
    rawOverridesQuery?.isSuccess &&
    !rawOverridesQuery.isError,
  );
}

/**
 * A permission denial carries its cause in the detail: the custom-sections
 * refusal and the demo-mode gate both answer 403 permission_denied.
 */
export function sectionSaveErrorMessage(error: unknown): string {
  const detail =
    error instanceof V2ProblemError && error.problemType === "permission_denied"
      ? error.problem.detail?.trim()
      : undefined;
  return detail ? `Could not save your rows: ${detail}` : "Could not save your rows";
}
