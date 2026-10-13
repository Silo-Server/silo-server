/**
 * One description of where a collection lives, so a page or editor can work
 * with server (library) and personal collections through the same calls.
 *
 * `SERVER_SCOPE` covers the admin library collections under `/admin/...`;
 * `PERSONAL_SCOPE` covers a profile's own collections. Both use the query
 * keys and request bodies the pages already use, so a cache entry or a request
 * looks the same whichever path produced it. Hooks live in
 * `hooks/queries/collectionScope.ts`, not on these objects.
 */
import type { QueryClient, QueryKey } from "@tanstack/react-query";

import {
  adminCreateBody,
  adminImportBody,
  adminMutationMessage,
  adminUpdateBody,
  fetchAdminCollections,
  fetchAdminCollectionSnapshot,
  saveAdminArtwork,
} from "@/api/adminCollections";
import {
  collectionCreateToV2,
  collectionsFromV2,
  collectionUpdateToV2,
  fetchCollectionEditSnapshot,
  importBodyToV2,
  previewFromV2,
  previewToV2,
  saveCollectionPoster,
  syncFromV2,
} from "@/api/personalCollections";
import type {
  Collection,
  CreateCollectionRequest,
  CreateLibraryCollectionRequest,
  DisplayQueryDefinition,
  LibraryCollection,
  QueryDefinition,
  QueryDefinitionInput,
  UpdateCollectionRequest,
  UpdateLibraryCollectionRequest,
  UserCollectionSyncSchedule,
} from "@/api/types";
import { normalizeQueryDefinition } from "@/api/types";
import { requiredETag } from "@/api/v2/etag";
import { v2, V2ProblemError } from "@/api/v2/request";
import { normalizeSmartCollectionLimit } from "@/components/collections/smartCollectionLimits";
import {
  invalidateAdminCollectionQueries,
  invalidateUserCollectionQueries,
} from "@/hooks/queries/collectionSurfaceRefresh";
import { adminKeys, collectionKeys, libraryCollectionKeys } from "@/hooks/queries/keys";
import {
  buildLibraryCollectionCatalogHref,
  buildUserCollectionCatalogHref,
} from "@/pages/catalogSearchParams";

import { changedFields, draftRules } from "./draft";
import { isOwnCollection } from "./personalOwnership";
import { cleanMDBListLink, emptySyncedDraft, franchiseIdOf, type SyncedDraft } from "./synced";
import {
  chartOfSourceConfig,
  chartSource,
  franchiseSource,
  tmdbListSource,
  withLimit,
  type StoredSource,
  type TMDBChart,
} from "./tmdbSources";
import { collectionKindOf, syncedSourceOf, type CollectionKind, type SyncedSource } from "./types";

export type ScopeKind = "server" | "personal";
export type ArtworkSlot = "poster" | "backdrop";
export type WireCollection = LibraryCollection | Collection;

/** A staged artwork change, sent after the collection itself saves. */
export interface ArtworkSlotDraft {
  file?: File | null;
  sourceUrl?: string;
  /** Remove the saved image on Save; a new file or link in the same slot wins. */
  remove?: boolean;
}
export type ArtworkDraft = Partial<Record<ArtworkSlot, ArtworkSlotDraft>>;

/**
 * A saved synced list: the list it follows and how it syncs, as the editor
 * changes them.
 */
export interface ListDraft {
  source: SyncedSource;
  /** The MDBList or TMDB list link, as typed. */
  link: string;
  /** The chart a TMDB chart list follows. */
  chart?: TMDBChart;
  /** A franchise list's TMDB collection ID, as typed; "" when it has none yet. */
  franchiseId: string;
  /** Blank takes the whole list. */
  limit?: number;
  /** Server: a cron expression. Personal: "", a cadence name, or "custom" until another is picked. */
  schedule: string;
  /** The stored `source_config`, for sources the editor can't rebuild. */
  stored: Record<string, unknown>;
}

/** What an editor edits, independent of scope. */
export interface CollectionDraft {
  kind: CollectionKind;
  name: string;
  description: string;
  /** Server: the libraries the collection belongs to. Personal smart: the libraries it matches; [] = all. */
  libraryIds: number[];
  /** Smart only. A "no limit" limit is already cleared. */
  rules?: QueryDefinition;
  /** The stored `sort_config`, sent back with smart rules. */
  rawSortConfig?: Record<string, unknown>;
  /** Personal manual only: the display filter. */
  showOnly?: DisplayQueryDefinition;
  artwork: ArtworkDraft;
  /** Manual, before the collection exists: titles to add, in order, once it is created. */
  stagedItems?: string[];
  /** Synced list, before it exists: the list it follows and how it syncs. */
  synced?: SyncedDraft;
  /** A saved synced list: what it follows and how it syncs. */
  list?: ListDraft;
  /**
   * Pin (`featured`) is not here: it is set in Arrange, so an editor creates
   * with it off and never sends it on a save.
   */
  server?: { visibility: "visible" | "hidden" };
  personal?: { shared: boolean; inLibraryTabs: boolean };
}

/** A Manual or Smart draft, which `create` and `update` send the same way. */
export type ManualOrSmartDraft = CollectionDraft & { kind: "manual" | "smart" };
/** A saved synced list's draft. */
export type ListEditDraft = CollectionDraft & { kind: "synced"; list: ListDraft };
/** A draft `update` can send. */
export type SavableDraft = ManualOrSmartDraft | ListEditDraft;
/** A new synced list: created through the scope's import route. */
export type SyncedCreateDraft = CollectionDraft & { kind: "synced"; synced: SyncedDraft };
export type CreatableDraft = ManualOrSmartDraft | SyncedCreateDraft;
/** The kinds an editor can start a new collection as. */
export type CreateKind = "manual" | "smart" | "synced";

/** The read model shared by both scopes, for headers, rows and editors. */
export interface CollectionView<Raw extends WireCollection = WireCollection> {
  id: string;
  scope: ScopeKind;
  kind: CollectionKind;
  source: SyncedSource | null;
  name: string;
  description: string;
  posterUrl?: string;
  posterThumbhash?: string;
  /** `posterUrl` is the server's collage of its titles, not an image someone chose. */
  posterIsCollage: boolean;
  backdropUrl?: string;
  itemCount: number;
  libraryIds: number[];
  /** Personal: the profile that created it, the only one that may change it. */
  ownerProfileId?: string;
  sync?: { status: string; message: string; lastAt?: string; nextAt?: string; schedule: string };
  server?: { visibility: "visible" | "hidden"; pinFirst: boolean; managementKey?: string };
  personal?: { shared: boolean; inLibraryTabs: boolean };
  raw: Raw;
}

export interface EditorSnapshot<Raw extends WireCollection = WireCollection> {
  view: CollectionView<Raw>;
  etag: string;
}

export interface PreviewItem {
  content_id: string;
  title: string;
  type: string;
  poster_url?: string;
}

export interface SaveOutcome {
  warnings: string[];
  failedArtwork: ArtworkSlot[];
}

export interface SyncOutcome {
  status: string;
  message: string;
  itemsMatched: number;
  /** Titles on the list found in none of its libraries, so skipped. */
  itemsUnmatched: number;
}

export interface CollectionScope<Raw extends WireCollection = WireCollection> {
  readonly kind: ScopeKind;
  /** The value the item hooks take to pick the admin or personal item routes. */
  readonly itemSource: "library" | "user";
  readonly artworkSlots: readonly ArtworkSlot[];
  readonly requireLibraries: boolean;
  /**
   * True when the editor read carries no artwork, so the editor waits for the
   * list before it opens. A personal read carries its own poster and opens at once.
   */
  readonly editorAwaitsList: boolean;
  readonly allowPersonalizedRules: boolean;

  paths: {
    list(options?: { libraryId?: number | null; view?: "list" | "arrange" }): string;
    create(options?: {
      type?: CollectionKind;
      source?: SyncedSource;
      libraryId?: number | null;
    }): string;
    edit(
      id: string,
      options?: { libraryId?: number | null; view?: "list" | "arrange"; focus?: "titles" },
    ): string;
    browse(view: CollectionView<Raw>): string;
  };

  /** The keys the existing hooks already use; the scope adds no cache namespace. */
  keys: {
    root: QueryKey;
    list: QueryKey;
    capabilities: QueryKey;
    snapshot(id: string): QueryKey;
    itemOrder(id: string): QueryKey;
    preview(fingerprint: string): QueryKey;
  };

  toView(raw: Raw): CollectionView<Raw>;
  toDraft(
    view: CollectionView<Raw> | null,
    init: { kind: CreateKind; libraryId?: number | null },
  ): CollectionDraft;
  /** True when the acting profile may not change it. Fails closed while the profile is unknown. */
  isReadOnly(view: CollectionView<Raw>, actingProfileId: string | null | undefined): boolean;

  /** The list the scope's pages read; the same request and cache entry they use. */
  fetchList(): Promise<{ collections: Raw[] }>;
  fetchSnapshot(id: string): Promise<EditorSnapshot<Raw>>;
  /**
   * `failedArtwork`: the slots whose upload or removal failed after the
   * collection saved. A synced list reports its first sync in `sync`.
   */
  create(draft: CreatableDraft): Promise<{ id: string; sync?: SyncOutcome } & SaveOutcome>;
  /** `base`: the copy the draft started from, for scopes that send only what changed. */
  update(
    ref: { id: string; etag: string },
    draft: SavableDraft,
    base?: CollectionDraft,
  ): Promise<SaveOutcome>;
  remove(ref: { id: string; etag: string }): Promise<void>;
  sync(id: string): Promise<SyncOutcome>;
  preview(rules: QueryDefinition, limit: number): Promise<{ items: PreviewItem[]; total: number }>;
  invalidate(queryClient: QueryClient, id?: string): Promise<void>;
  errorMessage(error: unknown, fallback: string): string;
}

function withQuery(path: string, params: Record<string, string | number | null | undefined>) {
  const search = new URLSearchParams();
  for (const [name, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== "") search.set(name, String(value));
  }
  const query = search.toString();
  return query ? `${path}?${query}` : path;
}

function rulesFor(kind: CollectionKind, query: QueryDefinitionInput) {
  return kind === "smart"
    ? normalizeSmartCollectionLimit(normalizeQueryDefinition(query))
    : undefined;
}

/** Smart rules match the draft's libraries; the draft's list is the one the editor shows. */
function smartRules(draft: ManualOrSmartDraft): QueryDefinition | undefined {
  return draft.kind === "smart" ? draftRules(draft) : undefined;
}

function sanitizeLibraryIds(raw: unknown): number[] {
  if (!Array.isArray(raw)) return [];
  const ids = raw
    .filter((id): id is number => typeof id === "number" && Number.isFinite(id) && id > 0)
    .map((id) => Math.trunc(id));
  return Array.from(new Set(ids));
}

/**
 * The libraries a personal collection is restricted to. A synced list keeps
 * them in `source_config.library_ids`; a smart collection in its rules; a
 * manual collection has none.
 */
export function personalCollectionLibraryIds(collection: Collection): number[] {
  if (collection.collection_type === "manual") return [];
  const configured = collection.source_config?.library_ids;
  if (Array.isArray(configured)) return sanitizeLibraryIds(configured);
  return sanitizeLibraryIds(collection.query_definition?.library_ids);
}

function serverLibraryIds(collection: LibraryCollection): number[] {
  if (collection.library_ids?.length) return collection.library_ids;
  return collection.library_id ? [collection.library_id] : [];
}

function syncView(collection: WireCollection): CollectionView["sync"] {
  if (collectionKindOf(collection.collection_type) !== "synced") return undefined;
  return {
    status: collection.last_sync_status ?? "",
    message: collection.last_sync_message ?? "",
    lastAt: collection.last_sync_at || undefined,
    nextAt: collection.next_sync_at || undefined,
    schedule: collection.sync_schedule ?? "",
  };
}

function trimmedSource(slot: ArtworkSlotDraft | undefined) {
  return slot?.sourceUrl?.trim() || undefined;
}

/** Admin artwork errors read "poster: …" or "backdrop: …". */
function serverOutcome(artworkErrors: string[]): SaveOutcome {
  return {
    warnings: artworkErrors,
    failedArtwork: (["poster", "backdrop"] as const).filter((slot) =>
      artworkErrors.some((error) => error.startsWith(`${slot}:`)),
    ),
  };
}

function personalOutcome(posterError: string | undefined): SaveOutcome {
  return posterError
    ? { warnings: [posterError], failedArtwork: ["poster"] }
    : { warnings: [], failedArtwork: [] };
}

/** The slots whose saved image a save removes. */
function removedArtwork(draft: SavableDraft): ArtworkSlot[] {
  return (["poster", "backdrop"] as const).filter((slot) => draft.artwork[slot]?.remove);
}

/** The pick's poster, unless the poster slot replaces or removes it. */
function pickedPoster(draft: SyncedCreateDraft): string | undefined {
  return isStaged(draft.artwork.poster) ? undefined : draft.synced.posterUrl;
}

function isStaged(slot: ArtworkSlotDraft | undefined) {
  return Boolean(slot?.file || trimmedSource(slot) || slot?.remove);
}

function followedList(draft: SyncedCreateDraft) {
  const { list } = draft.synced;
  if (!list) throw new Error("Pick a list first.");
  return list;
}

function chartFields(chart: TMDBChart) {
  return { preset: chart.preset, media_type: chart.mediaType, time_window: chart.timeWindow };
}

function syncOutcome(run: {
  status: string;
  message: string;
  items_matched: number;
  items_unmatched: number;
}): SyncOutcome {
  return {
    status: run.status,
    message: run.message,
    itemsMatched: run.items_matched,
    itemsUnmatched: run.items_unmatched,
  };
}

/**
 * A new server synced list: imported unpinned (Pin is an Arrange action). The
 * import takes no visibility, so hiding it is a guarded PATCH once the list
 * exists, before its artwork.
 */
async function createServerSynced(draft: SyncedCreateDraft) {
  const list = followedList(draft);
  const common = {
    library_ids: draft.libraryIds,
    title: draft.name.trim(),
    description: draft.description.trim(),
    limit: draft.synced.limit,
    featured: false,
    sync_schedule: draft.synced.schedule || undefined,
    sort_config: draft.rawSortConfig ?? {},
    poster_url: pickedPoster(draft),
  };
  const importList = () => {
    switch (list.source) {
      case "mdblist":
        return v2("POST /api/v2/admin/collections/import/mdblist", {
          body: adminImportBody({ ...common, url: list.url }),
        });
      case "tmdb_list":
        return v2("POST /api/v2/admin/collections/import/tmdb-list", {
          body: adminImportBody({ ...common, url: list.url }),
        });
      case "tmdb_chart":
        return v2("POST /api/v2/admin/collections/import/tmdb", {
          body: adminImportBody({ ...common, ...chartFields(list.chart) }),
        });
    }
  };
  const result = await importList();
  const id = result.collection.id;
  const hideWarning = draft.server?.visibility === "hidden" ? await hideFromTabs(id) : undefined;
  const { artworkErrors } = await saveAdminArtwork(
    result.collection,
    {
      poster_source_url: trimmedSource(draft.artwork.poster),
      backdrop_source_url: trimmedSource(draft.artwork.backdrop),
    },
    draft.artwork.poster?.file,
    draft.artwork.backdrop?.file,
  );
  const outcome = serverOutcome(artworkErrors);
  if (hideWarning) outcome.warnings.unshift(hideWarning);
  return { id, sync: result.sync_run && syncOutcome(result.sync_run), ...outcome };
}

/** Hides a server collection from Collections tabs; answers a warning when that fails. */
async function hideFromTabs(id: string): Promise<string | undefined> {
  try {
    const { collection, etag } = await fetchAdminCollectionSnapshot(id);
    await v2("PATCH /api/v2/admin/collections/{id}", {
      path: { id },
      headers: { "If-Match": requiredETag(etag) },
      body: adminUpdateBody({ collection_type: collection.collection_type, visibility: "hidden" }),
    });
    return undefined;
  } catch (error) {
    // The list exists either way; say that it still shows.
    return `It still shows on Collections tabs: ${adminMutationMessage(error, "hiding it failed")}`;
  }
}

/**
 * A new personal synced list. The import takes no Collections tab choice, so
 * turning it on is a guarded PATCH once the list exists.
 */
async function createPersonalSynced(draft: SyncedCreateDraft) {
  const list = followedList(draft);
  const common = {
    title: draft.name.trim(),
    description: draft.description.trim(),
    limit: draft.synced.limit,
    sync_schedule: draft.synced.schedule as UserCollectionSyncSchedule,
    is_shared: draft.personal?.shared ?? false,
    poster_url: trimmedSource(draft.artwork.poster) ?? pickedPoster(draft),
    library_ids: draft.libraryIds.length > 0 ? draft.libraryIds : undefined,
    display_query_definition: draft.showOnly,
    sort_config: draft.rawSortConfig ?? {},
  };
  const importList = () => {
    switch (list.source) {
      case "mdblist":
        return v2("POST /api/v2/collections/import/mdblist", {
          body: importBodyToV2({ ...common, url: list.url }),
        });
      case "tmdb_list":
        return v2("POST /api/v2/collections/import/tmdb-list", {
          body: importBodyToV2({ ...common, url: list.url }),
        });
      case "tmdb_chart":
        return v2("POST /api/v2/collections/import/tmdb", {
          body: importBodyToV2({ ...common, ...chartFields(list.chart) }),
        });
    }
  };
  const result = await importList();
  const id = result.collection.id;
  const { posterError } = await saveCollectionPoster(result.collection, draft.artwork.poster?.file);
  const outcome = personalOutcome(posterError);
  if (draft.personal?.inLibraryTabs) {
    try {
      const { etag } = await fetchCollectionEditSnapshot(id);
      await v2("PATCH /api/v2/collections/{id}", {
        path: { id },
        headers: { "If-Match": requiredETag(etag) },
        body: collectionUpdateToV2({ include_in_server_collections: true }),
      });
    } catch (error) {
      outcome.warnings.push(
        personalMutationMessage(error, "Couldn't show it on the Collections tab"),
      );
    }
  }
  return { id, sync: result.sync && syncOutcome(result.sync), ...outcome };
}

function positiveLimit(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? Math.trunc(value)
    : undefined;
}

function textOf(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

/** A saved synced list's draft: what it follows, read from its stored source. */
function listDraftOf(view: CollectionView, schedule: string): ListDraft | undefined {
  if (!view.source) return undefined;
  const stored = view.raw.source_config ?? {};
  const sourceUrl = view.raw.source_url ?? "";
  let link = "";
  // A chart's source_url is an internal tmdb:// name, never a link.
  if (view.source === "mdblist") link = sourceUrl || textOf(stored.url);
  if (view.source === "tmdb_list") link = textOf(stored.url) || sourceUrl;
  const franchise = positiveLimit(stored.collection_id);
  return {
    source: view.source,
    link,
    chart: view.source === "tmdb_chart" ? chartOfSourceConfig(stored) : undefined,
    franchiseId: view.source === "tmdb_franchise" && franchise ? String(franchise) : "",
    limit: positiveLimit(stored.limit),
    schedule,
    stored,
  };
}

/** A list's stored config with a changed Max titles; nothing when it didn't change. */
function storedWithLimit(list: ListDraft): Partial<StoredSource> {
  if (list.limit === positiveLimit(list.stored.limit)) return {};
  const { limit: _limit, ...rest } = list.stored;
  return { source_config: withLimit(rest, list.limit) };
}

/**
 * What a saved list's PATCH sends for its source. MDBList, chart, TMDB list
 * and franchise sources are rebuilt whole; a Discover list, and a franchise
 * list with no ID yet, send their stored config only when Max titles changed;
 * a Trakt list never sends its source, which the server keeps as it is.
 */
function editedSource(list: ListDraft, linkChanged: boolean): Partial<StoredSource> {
  switch (list.source) {
    case "mdblist": {
      // A stored link is sent back as it is; only a newly typed one is cleaned.
      const url = linkChanged ? cleanMDBListLink(list.link) : list.link.trim();
      return {
        source_url: url,
        source_config: withLimit({ mode: "mdblist_json", url }, list.limit),
      };
    }
    case "tmdb_chart":
      return chartSource(list.chart ?? chartOfSourceConfig(list.stored), list.limit);
    case "tmdb_list":
      return tmdbListSource(list.link, list.limit);
    case "tmdb_franchise": {
      const id = franchiseIdOf(list.franchiseId);
      return id ? franchiseSource(id, list.limit) : storedWithLimit(list);
    }
    case "tmdb_discover":
      return storedWithLimit(list);
    case "trakt":
      return {};
  }
}

const COLLECTION_TYPE_OF: Readonly<Record<SyncedSource, "mdblist" | "tmdb" | "trakt">> = {
  mdblist: "mdblist",
  tmdb_chart: "tmdb",
  tmdb_list: "tmdb",
  tmdb_franchise: "tmdb",
  tmdb_discover: "tmdb",
  trakt: "trakt",
};

/** A saved server list's PATCH: every field, its source rebuilt, never `featured`. */
function serverListRequest(
  draft: ListEditDraft,
  base: CollectionDraft,
): UpdateLibraryCollectionRequest {
  return {
    library_ids: draft.libraryIds,
    title: draft.name,
    description: draft.description,
    collection_type: COLLECTION_TYPE_OF[draft.list.source],
    visibility: draft.server?.visibility ?? "visible",
    sort_config: draft.rawSortConfig ?? {},
    sync_schedule: draft.list.schedule.trim(),
    ...editedSource(draft.list, changedFields(base, draft).includes("list")),
    poster_source_url: trimmedSource(draft.artwork.poster),
    backdrop_source_url: trimmedSource(draft.artwork.backdrop),
  };
}

/**
 * A saved personal list's PATCH. Name, sharing, libraries, the Collections
 * tab switch and Show only always go; everything else only when it changed
 * from `base`. A legacy Trakt list never sends libraries: the server refuses
 * any it's sent. A "custom" schedule is never sent: it stays until another is picked.
 */
function personalListRequest(draft: ListEditDraft, base: CollectionDraft): UpdateCollectionRequest {
  const changed = new Set(changedFields(base, draft));
  const { list } = draft;
  const body: UpdateCollectionRequest = {
    name: draft.name,
    is_shared: draft.personal?.shared ?? false,
    include_in_server_collections: draft.personal?.inLibraryTabs ?? false,
    display_query_definition: draft.showOnly,
  };
  if (list.source !== "trakt") body.library_ids = draft.libraryIds;
  if (changed.has("description")) body.description = draft.description;
  if (changed.has("rawSortConfig")) body.sort_config = draft.rawSortConfig ?? {};
  if (changed.has("list") && list.source === "mdblist") {
    body.source_url = cleanMDBListLink(list.link);
  }
  if (changed.has("list") && list.source === "tmdb_list") body.source_url = list.link.trim();
  if (changed.has("limit")) body.max_items = list.limit ?? 0;
  if (changed.has("schedule") && list.schedule !== "custom") {
    body.sync_schedule = list.schedule as UserCollectionSyncSchedule;
  }
  return body;
}

function serverRequest(
  draft: ManualOrSmartDraft,
): Omit<CreateLibraryCollectionRequest, "featured"> {
  return {
    library_ids: draft.libraryIds,
    title: draft.name,
    description: draft.description,
    collection_type: draft.kind,
    visibility: draft.server?.visibility ?? "visible",
    query_definition: smartRules(draft),
    sort_config: draft.kind === "smart" ? (draft.rawSortConfig ?? {}) : undefined,
    poster_source_url: trimmedSource(draft.artwork.poster),
    backdrop_source_url: trimmedSource(draft.artwork.backdrop),
  };
}

function personalFields(draft: ManualOrSmartDraft) {
  return {
    name: draft.name,
    description: draft.description,
    is_shared: draft.personal?.shared ?? false,
    query_definition: smartRules(draft),
    sort_config: draft.kind === "smart" ? (draft.rawSortConfig ?? {}) : undefined,
    include_in_server_collections: draft.personal?.inLibraryTabs ?? false,
    display_query_definition: draft.kind === "manual" ? draft.showOnly : undefined,
  };
}

function personalMutationMessage(error: unknown, fallback: string) {
  if (error instanceof V2ProblemError && error.status === 412) {
    return "This collection changed while you were editing. Reload it and review your changes before saving again.";
  }
  return error instanceof Error ? error.message : fallback;
}

/** The admin preview also answers with paging state; the editor needs only items and total. */
function previewItems(value: { items: PreviewItem[]; total: number }) {
  return { items: value.items, total: value.total };
}

/**
 * A personal list's schedule by name. A server that predates `sync_cadence`
 * reads as "custom" when it has a schedule, so the editor never shows raw cron.
 */
function personalSchedule(collection: Collection): string {
  return collection.sync_cadence ?? (collection.sync_schedule ? "custom" : "");
}

/**
 * The server collections a page-wide action reaches: every collection for
 * All libraries, or the ones the rendered board shows for one library.
 */
export function collectionsInAdminScope(
  allCollections: LibraryCollection[],
  board:
    | { groups: Array<{ collections: LibraryCollection[] }>; ungrouped: LibraryCollection[] }
    | undefined,
  selectedLibraryId: number | null,
): LibraryCollection[] {
  let collections = allCollections;
  if (selectedLibraryId !== null) {
    if (!board) return [];
    collections = [...board.ungrouped, ...board.groups.flatMap((group) => group.collections)];
  }
  return [...new Map(collections.map((collection) => [collection.id, collection])).values()];
}

export const SERVER_SCOPE: CollectionScope<LibraryCollection> = {
  kind: "server",
  itemSource: "library",
  artworkSlots: ["poster", "backdrop"],
  requireLibraries: true,
  editorAwaitsList: true,
  allowPersonalizedRules: false,

  paths: {
    // A bare `?libraryId=N` opens that library's Arrange (older links), so a
    // library's List names its view.
    list: ({ libraryId, view = libraryId ? "list" : undefined } = {}) =>
      withQuery("/admin/collections", { libraryId, view }),
    create: ({ type, source, libraryId } = {}) =>
      withQuery("/admin/collections/new", { type, source, libraryId }),
    edit: (id, { libraryId, view, focus } = {}) =>
      withQuery(`/admin/collections/${encodeURIComponent(id)}/edit`, { libraryId, view, focus }),
    browse: (view) => buildLibraryCollectionCatalogHref(view.id, view.name),
  },

  keys: {
    root: ["admin", "collections"],
    list: adminKeys.collections(),
    capabilities: ["admin", "collections", "capabilities"],
    snapshot: (id) => ["admin", "collections", "edit", id],
    itemOrder: (id) => ["libraryCollections", "items", id, "order"],
    preview: (fingerprint) => collectionKeys.preview("admin", fingerprint),
  },

  toView: (raw) => ({
    id: raw.id,
    scope: "server",
    kind: collectionKindOf(raw.collection_type),
    source: syncedSourceOf(raw.collection_type, raw.source_config),
    name: raw.title,
    description: raw.description ?? "",
    posterUrl: raw.poster_url || undefined,
    posterThumbhash: raw.poster_thumbhash || undefined,
    posterIsCollage: Boolean(raw.poster_url && raw.poster_is_collage),
    backdropUrl: raw.backdrop_url || undefined,
    itemCount: raw.item_count ?? 0,
    libraryIds: serverLibraryIds(raw),
    sync: syncView(raw),
    server: {
      visibility: raw.visibility,
      pinFirst: raw.featured,
      managementKey: raw.management_key || undefined,
    },
    raw,
  }),

  toDraft: (view, { kind, libraryId }) => {
    const draftKind = view?.kind ?? kind;
    const libraryIds = view ? view.libraryIds : libraryId ? [libraryId] : [];
    return {
      kind: draftKind,
      name: view?.name ?? "",
      description: view?.description ?? "",
      libraryIds,
      rules: rulesFor(draftKind, { ...view?.raw.query_definition, library_ids: libraryIds }),
      rawSortConfig: view?.raw.sort_config ?? {},
      artwork: {},
      server: { visibility: view?.server?.visibility ?? "visible" },
      ...(!view && kind === "synced" ? { synced: emptySyncedDraft() } : {}),
      list: view ? listDraftOf(view, view.raw.sync_schedule ?? "") : undefined,
    };
  },

  isReadOnly: () => false,

  fetchList: () => fetchAdminCollections(),

  async fetchSnapshot(id) {
    const { collection, etag } = await fetchAdminCollectionSnapshot(id);
    return { view: SERVER_SCOPE.toView(collection), etag };
  },

  async create(draft) {
    if (draft.kind === "synced") return createServerSynced(draft);
    const request = serverRequest(draft);
    // Made in the editor, so not pinned: Pin is an Arrange action.
    const created = await v2("POST /api/v2/admin/collections", {
      body: adminCreateBody({ ...request, featured: false }),
    });
    const { artworkErrors } = await saveAdminArtwork(
      created,
      request,
      draft.artwork.poster?.file,
      draft.artwork.backdrop?.file,
    );
    return { id: created.id, ...serverOutcome(artworkErrors) };
  },

  async update(ref, draft, base) {
    const request =
      draft.kind === "synced" ? serverListRequest(draft, base ?? draft) : serverRequest(draft);
    const updated = await v2("PATCH /api/v2/admin/collections/{id}", {
      path: { id: ref.id },
      headers: { "If-Match": requiredETag(ref.etag) },
      // No `featured`: a stale editor must not undo a Pin set in Arrange.
      body: adminUpdateBody(request),
    });
    const { artworkErrors } = await saveAdminArtwork(
      updated,
      request,
      draft.artwork.poster?.file,
      draft.artwork.backdrop?.file,
      removedArtwork(draft),
    );
    return serverOutcome(artworkErrors);
  },

  async remove(ref) {
    await v2("DELETE /api/v2/admin/collections/{id}", {
      path: { id: ref.id },
      headers: { "If-Match": requiredETag(ref.etag) },
    });
  },

  async sync(id) {
    return syncOutcome(await v2("POST /api/v2/admin/collections/{id}/sync", { path: { id } }));
  },

  preview: (rules, limit) =>
    v2("POST /api/v2/admin/collections/preview", {
      body: { query_definition: rules, limit },
    }).then(previewItems),

  invalidate: (queryClient) => invalidateAdminCollectionQueries(queryClient),

  errorMessage: (error, fallback) => adminMutationMessage(error, fallback),
};

export const PERSONAL_SCOPE: CollectionScope<Collection> = {
  kind: "personal",
  itemSource: "user",
  artworkSlots: ["poster"],
  requireLibraries: false,
  editorAwaitsList: false,
  allowPersonalizedRules: true,

  paths: {
    list: () => "/collections",
    create: ({ type, source } = {}) => withQuery("/collections/new", { type, source }),
    edit: (id, { focus } = {}) =>
      withQuery(`/collections/${encodeURIComponent(id)}/edit`, { focus }),
    browse: (view) => buildUserCollectionCatalogHref(view.id, view.name),
  },

  keys: {
    root: collectionKeys.all,
    list: collectionKeys.list(),
    capabilities: ["collections", "capabilities"],
    snapshot: (id) => ["collections", "edit", id],
    itemOrder: (id) => ["collections", "items", id, "order"],
    preview: (fingerprint) => collectionKeys.preview("user", fingerprint),
  },

  toView: (raw) => ({
    id: raw.id,
    scope: "personal",
    kind: collectionKindOf(raw.collection_type),
    source: syncedSourceOf(raw.collection_type, raw.source_config),
    name: raw.name,
    description: raw.description ?? "",
    posterUrl: raw.poster_url || undefined,
    posterThumbhash: raw.poster_thumbhash || undefined,
    posterIsCollage: Boolean(raw.poster_url && raw.poster_is_collage),
    itemCount: raw.item_count ?? 0,
    libraryIds: personalCollectionLibraryIds(raw),
    ownerProfileId: raw.creator_profile_id,
    sync: syncView(raw),
    personal: {
      shared: raw.is_shared,
      inLibraryTabs: raw.include_in_server_collections ?? false,
    },
    raw,
  }),

  toDraft: (view, { kind }) => {
    const draftKind = view?.kind ?? kind;
    const libraryIds = view?.libraryIds ?? [];
    const synced = !view && kind === "synced" ? { synced: emptySyncedDraft() } : {};
    return {
      kind: draftKind,
      name: view?.name ?? "",
      description: view?.description ?? "",
      libraryIds,
      rules: rulesFor(draftKind, { ...view?.raw.query_definition, library_ids: libraryIds }),
      rawSortConfig: view?.raw.sort_config ?? {},
      showOnly: view?.raw.display_query_definition,
      artwork: {},
      personal: {
        shared: view?.personal?.shared ?? false,
        inLibraryTabs: view?.personal?.inLibraryTabs ?? false,
      },
      ...synced,
      list: view ? listDraftOf(view, personalSchedule(view.raw)) : undefined,
    };
  },

  isReadOnly: (view, actingProfileId) =>
    !isOwnCollection({ creator_profile_id: view.ownerProfileId ?? "" }, actingProfileId),

  fetchList: () => v2("GET /api/v2/collections").then(collectionsFromV2),

  async fetchSnapshot(id) {
    const { collection, etag } = await fetchCollectionEditSnapshot(id);
    return { view: PERSONAL_SCOPE.toView(collection), etag };
  },

  async create(draft) {
    if (draft.kind === "synced") return createPersonalSynced(draft);
    const body: CreateCollectionRequest = {
      ...personalFields(draft),
      collection_type: draft.kind,
      poster_source_url: trimmedSource(draft.artwork.poster),
    };
    const created = await v2("POST /api/v2/collections", { body: collectionCreateToV2(body) });
    const { posterError } = await saveCollectionPoster(created, draft.artwork.poster?.file);
    return { id: created.id, ...personalOutcome(posterError) };
  },

  async update(ref, draft, base) {
    const body: UpdateCollectionRequest =
      draft.kind === "synced" ? personalListRequest(draft, base ?? draft) : personalFields(draft);
    // An absent display_query_definition keeps the saved one, so Show only set
    // back to All sends an empty filter, which the server stores as none.
    if (draft.kind !== "smart" && !draft.showOnly && base?.showOnly) {
      body.display_query_definition = { match: "all", groups: [] };
    }
    const updated = await v2("PATCH /api/v2/collections/{id}", {
      path: { id: ref.id },
      headers: { "If-Match": requiredETag(ref.etag) },
      body: collectionUpdateToV2(body),
    });
    const { posterError } = await saveCollectionPoster(
      updated,
      draft.artwork.poster?.file,
      trimmedSource(draft.artwork.poster),
      draft.artwork.poster?.remove,
    );
    return personalOutcome(posterError);
  },

  async remove(ref) {
    await v2("DELETE /api/v2/collections/{id}", {
      path: { id: ref.id },
      headers: { "If-Match": requiredETag(ref.etag) },
    });
  },

  async sync(id) {
    return syncOutcome(
      syncFromV2(await v2("POST /api/v2/collections/{id}/sync", { path: { id } })),
    );
  },

  preview: (rules, limit) =>
    v2("POST /api/v2/collections/preview", {
      body: previewToV2({ query_definition: rules, limit }),
    }).then(previewFromV2),

  async invalidate(queryClient, id) {
    await Promise.all([
      invalidateUserCollectionQueries(queryClient, id),
      // A collection shown on the Collections tab is listed in each library's tab.
      queryClient.invalidateQueries({ queryKey: libraryCollectionKeys.all }),
    ]);
  },

  errorMessage: personalMutationMessage,
};
