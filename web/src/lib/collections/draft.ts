/**
 * Pure rules for a collection editor's draft: which fields changed, and how a
 * draft absorbs a newer copy of the collection without losing what the
 * person typed (a three-way merge against the copy the draft started from).
 *
 * Artwork is staged separately and is not a draft field; `kind` is fixed once
 * the collection exists. A saved synced list's stored source config is not a
 * field either: it is what the list read last.
 */
import { normalizeQueryDefinition, type QueryDefinition } from "@/api/types";

import type { CollectionDraft, ListDraft } from "./scope";

export type DraftField =
  | "name"
  | "description"
  | "libraryIds"
  | "rules"
  | "rawSortConfig"
  | "showOnly"
  | "visibility"
  | "shared"
  | "inLibraryTabs"
  | "list"
  | "limit"
  | "schedule";

interface FieldAccess {
  get(draft: CollectionDraft): unknown;
  set(draft: CollectionDraft, value: unknown): CollectionDraft;
}

/** A saved synced list's draft with `fields` replaced; a draft with no list stays as it is. */
function withList(draft: CollectionDraft, fields: Partial<ListDraft>): CollectionDraft {
  return draft.list ? { ...draft, list: { ...draft.list, ...fields } } : draft;
}

const FIELDS: Record<DraftField, FieldAccess> = {
  name: { get: (d) => d.name, set: (d, v) => ({ ...d, name: v as string }) },
  description: {
    get: (d) => d.description,
    set: (d, v) => ({ ...d, description: v as string }),
  },
  // The order libraries were ticked in means nothing.
  libraryIds: {
    get: (d) => [...d.libraryIds].sort((a, b) => a - b),
    set: (d, v) => ({ ...d, libraryIds: [...(v as number[])] }),
  },
  // The rules' libraries are the draft's `libraryIds`; they count once, as Libraries.
  rules: {
    get: (d) => d.rules && { ...d.rules, library_ids: undefined },
    set: (d, v) => ({
      ...d,
      rules: v
        ? { ...(v as NonNullable<CollectionDraft["rules"]>), library_ids: d.libraryIds }
        : undefined,
    }),
  },
  rawSortConfig: {
    get: (d) => d.rawSortConfig,
    set: (d, v) => ({ ...d, rawSortConfig: v as CollectionDraft["rawSortConfig"] }),
  },
  showOnly: {
    get: (d) => d.showOnly,
    set: (d, v) => ({ ...d, showOnly: v as CollectionDraft["showOnly"] }),
  },
  visibility: {
    get: (d) => d.server?.visibility,
    set: (d, v) =>
      d.server ? { ...d, server: { ...d.server, visibility: v as "visible" | "hidden" } } : d,
  },
  shared: {
    get: (d) => d.personal?.shared,
    set: (d, v) => (d.personal ? { ...d, personal: { ...d.personal, shared: v as boolean } } : d),
  },
  inLibraryTabs: {
    get: (d) => d.personal?.inLibraryTabs,
    set: (d, v) =>
      d.personal ? { ...d, personal: { ...d.personal, inLibraryTabs: v as boolean } } : d,
  },
  // A saved synced list: what it follows counts once, as the list.
  list: {
    get: (d) =>
      d.list && {
        source: d.list.source,
        link: d.list.link,
        chart: d.list.chart,
        franchiseId: d.list.franchiseId,
      },
    set: (d, v) => withList(d, v as Partial<ListDraft>),
  },
  limit: {
    get: (d) => d.list?.limit,
    set: (d, v) => withList(d, { limit: v as number | undefined }),
  },
  schedule: {
    get: (d) => d.list?.schedule,
    set: (d, v) => withList(d, { schedule: v as string }),
  },
};

const FIELD_ORDER = Object.keys(FIELDS) as DraftField[];

/** The rules a Smart draft matches, across the libraries the draft names. */
export function draftRules(draft: CollectionDraft): QueryDefinition {
  return { ...normalizeQueryDefinition(draft.rules), library_ids: draft.libraryIds };
}

/**
 * A smart collection's `sort_config` once its stored default sort is cleared:
 * `field` and `order` go, any other setting stays. A stored default sort wins
 * over the rules' Order, so changing Order clears it.
 */
export function clearDefaultSort(sortConfig: Record<string, unknown> | undefined) {
  const { field: _field, order: _order, ...rest } = sortConfig ?? {};
  return rest;
}

/** JSON with sorted object keys, so key order never reads as a change. */
function stable(value: unknown): string {
  return JSON.stringify(value, (_key, entry: unknown) =>
    entry && typeof entry === "object" && !Array.isArray(entry)
      ? Object.fromEntries(
          Object.entries(entry as Record<string, unknown>).sort(([a], [b]) => a.localeCompare(b)),
        )
      : entry,
  );
}

function same(a: unknown, b: unknown) {
  return stable(a) === stable(b);
}

/** The fields whose values differ between two drafts, in a fixed order. */
export function changedFields(a: CollectionDraft, b: CollectionDraft): DraftField[] {
  return FIELD_ORDER.filter((field) => !same(FIELDS[field].get(a), FIELDS[field].get(b)));
}

/** `draft` with `fields` taken from `from`. */
export function takeFields(
  draft: CollectionDraft,
  from: CollectionDraft,
  fields: readonly DraftField[],
): CollectionDraft {
  return fields.reduce((next, field) => FIELDS[field].set(next, FIELDS[field].get(from)), draft);
}

/**
 * Three-way merge of the person's draft (`mine`) and a newer copy of the
 * collection (`theirs`) against the copy the draft started from (`base`).
 * Per field: untouched by the person → theirs; untouched by the server →
 * mine; both changed to the same value → that value; both changed
 * differently → mine, reported as a conflict. Staged artwork stays as it is.
 * `theirs` becomes the new base.
 */
export function mergeDraft(
  base: CollectionDraft,
  mine: CollectionDraft,
  theirs: CollectionDraft,
): { base: CollectionDraft; draft: CollectionDraft; conflicts: DraftField[] } {
  const conflicts: DraftField[] = [];
  let draft: CollectionDraft = { ...theirs, artwork: mine.artwork };
  for (const field of FIELD_ORDER) {
    const { get } = FIELDS[field];
    const was = get(base);
    const ours = get(mine);
    const server = get(theirs);
    if (same(ours, was) || same(ours, server)) continue; // theirs is already in `draft`
    if (!same(server, was)) conflicts.push(field);
    draft = FIELDS[field].set(draft, ours);
  }
  return { base: theirs, draft, conflicts };
}
