import type { Query, QueryClient } from "@tanstack/react-query";
import type {
  HomeSectionItemsResponse,
  ItemDetail,
  SeasonDetailResponse,
  SeasonsResponse,
} from "@/api/types";
import {
  adminKeys,
  calendarKeys,
  catalogKeys,
  collectionKeys,
  episodeKeys,
  favoriteKeys,
  historyKeys,
  itemKeys,
  libraryCollectionKeys,
  personKeys,
  progressKeys,
  recKeys,
  sectionKeys,
  shuffleKeys,
  watchlistKeys,
} from "./keys";
import {
  activeCatalogQueryMatchesLibrary,
  activeSectionQueryMatchesLibrary,
} from "@/lib/queryInvalidation";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";

interface InvalidateMediaSurfaceOptions {
  itemId?: string;
  libraryId?: number;
  watchedKeys?: Array<readonly unknown[]>;
  skipItemDetail?: boolean;
  /**
   * Skip every query whose key names itemId, for an item that moved to a new
   * content ID: refetching its old ID would only return 404.
   */
  skipItemQueries?: boolean;
  skipSimilarItems?: boolean;
  // Items already found related to itemId, such as through a watch detail
  // dropped since (dropInactiveWatchDetail), which was the only link from an
  // episode to its series.
  knownRelatedIds?: ReadonlySet<string>;
  // The item may be one this client has never seen, such as an episode a
  // scan just added. Only catalog events can say so; progress, favorite and
  // watchlist events are about items that already exist.
  itemMayBeNew?: boolean;
  // Set internally for such an item when nothing in the cache names it,
  // with the items owning the cached lists, whose detail (a season's
  // episode count) changes with them.
  refreshItemKeyedLists?: boolean;
  listOwnerIds?: ReadonlySet<string>;
}

const MEDIA_SURFACE_REFRESH_DELAY_MS = 600;
// Upper bound on how long coalescing may postpone a refresh. Without it a
// sustained event stream (a history import, a watch-provider sync) re-arms the
// timer indefinitely and the surfaces never refresh at all.
const MEDIA_SURFACE_REFRESH_MAX_WAIT_MS = 3000;
const MEDIA_SURFACE_PREFIXES = [
  itemKeys.all,
  progressKeys.all,
  historyKeys.all,
  favoriteKeys.all,
  watchlistKeys.all,
  personKeys.all,
  adminKeys.playbackHistory({}).slice(0, 2),
];
const scheduledInvalidations = new WeakMap<
  QueryClient,
  Map<
    string,
    {
      timer: ReturnType<typeof setTimeout>;
      deadline: number;
      options: InvalidateMediaSurfaceOptions;
    }
  >
>();

export function updateCatalogItemDetail(
  queryClient: QueryClient,
  itemId: string,
  updater: (detail: ItemDetail) => ItemDetail,
) {
  queryClient.setQueriesData<ItemDetail>(
    {
      predicate: (query) => isItemDetailQueryKey(query.queryKey, itemId),
    },
    (current) => (current ? updater(current) : current),
  );
}

// Callers must await this before writing optimistic state. The default
// `revert: true` restores the pre-fetch snapshot and leaves the query idle;
// `revert: false` would instead put an in-flight query into an error state
// carrying a CancelledError, which surfaces as a bogus "CancelledError" toast.
export function cancelItemDetailQueries(queryClient: QueryClient, itemId: string) {
  return queryClient.cancelQueries({
    predicate: (query) => isItemDetailQueryKey(query.queryKey, itemId),
  });
}

// dropInactiveWatchDetail drops a closed player's cached watch detail for an
// item whose resume position changed, at once: the player starts from that
// detail's position while it refetches. The detail may be the only link from
// an episode to its series, so the items it led to are returned for the
// media surface refresh (knownRelatedIds). An open player's query is active
// and keeps its data. Undefined when there was nothing to drop.
export function dropInactiveWatchDetail(
  queryClient: QueryClient,
  itemId: string,
): ReadonlySet<string> | undefined {
  const filters = { queryKey: [...itemKeys.all, "watchDetail", itemId], type: "inactive" } as const;
  if (queryClient.getQueryCache().findAll(filters).length === 0) return undefined;
  const related = relatedItemIds(queryClient, itemId);
  queryClient.removeQueries(filters);
  return related;
}

export function removeItemFromHomeSectionCaches(
  queryClient: QueryClient,
  itemId: string,
  sectionType?: string | ReadonlySet<string>,
) {
  queryClient.setQueriesData<HomeSectionItemsResponse>(
    {
      predicate: (query) =>
        Array.isArray(query.queryKey) &&
        query.queryKey[0] === sectionKeys.homeItemsRoot()[0] &&
        query.queryKey[1] === sectionKeys.homeItemsRoot()[1] &&
        query.queryKey[2] === sectionKeys.homeItemsRoot()[2],
    },
    (current) => {
      if (!current?.section) {
        return current;
      }
      if (
        typeof sectionType === "string"
          ? current.section.section_type !== sectionType
          : sectionType && !sectionType.has(current.section.section_type)
      ) {
        return current;
      }
      const nextItems = current.section.items.filter((item) => item.content_id !== itemId);
      if (nextItems.length === current.section.items.length) {
        return current;
      }
      return {
        ...current,
        section: {
          ...current.section,
          total_count: Math.max(
            0,
            current.section.total_count - (current.section.items.length - nextItems.length),
          ),
          items: nextItems,
        },
      };
    },
  );
}

function isWatchDetailQueryKey(queryKey: readonly unknown[]) {
  return queryKey[0] === "items" && queryKey[1] === "watchDetail";
}

export function isItemDetailQueryKey(queryKey: unknown, itemId: string) {
  return (
    Array.isArray(queryKey) &&
    ((queryKey[0] === "catalog" &&
      queryKey[1] === "items" &&
      queryKey[2] === itemId &&
      queryKey[3] === "detail") ||
      (queryKey[0] === "items" && queryKey[1] === "detail" && queryKey[2] === itemId))
  );
}

function queryKeyStartsWith(queryKey: readonly unknown[], prefix: readonly unknown[]) {
  return (
    prefix.length <= queryKey.length &&
    prefix.every((part, index) => {
      const candidate = queryKey[index];
      if (Object.is(part, candidate)) return true;
      if (
        typeof part !== "object" ||
        part === null ||
        typeof candidate !== "object" ||
        candidate === null
      ) {
        return false;
      }
      return JSON.stringify(part) === JSON.stringify(candidate);
    })
  );
}

// Only these shapes are narrowed by itemId. episodeKeys.byItem is left out on
// purpose: it is invalidated only through watchedKeys, and mapping it here
// would change which episode lists a refresh touches.
export function getQueryKeyItemId(queryKey: readonly unknown[]): string | undefined {
  if (!Array.isArray(queryKey) || queryKey.length < 2) return undefined;
  if (
    queryKey[0] === "items" &&
    (queryKey[1] === "detail" || queryKey[1] === "watchDetail" || queryKey[1] === "markers") &&
    typeof queryKey[2] === "string"
  ) {
    return queryKey[2];
  }
  // A series' season list, season detail and episodes are keyed by series.
  if (
    queryKey[0] === "catalog" &&
    (queryKey[1] === "items" || queryKey[1] === "series") &&
    typeof queryKey[2] === "string"
  ) {
    return queryKey[2];
  }
  // ratingKeys.list() shares the ["ratings", x] shape; "list" is not an item ID.
  if (queryKey[0] === "ratings" && typeof queryKey[1] === "string" && queryKey[1] !== "list") {
    return queryKey[1];
  }
  return undefined;
}

// The item whose episodes or seasons a cached list query holds: an episode
// list belongs to its season (or series), a season list to its series.
function listOwnerId(queryKey: readonly unknown[]): string | undefined {
  if (queryKey[0] === "catalog" && queryKey[1] === "series" && typeof queryKey[2] === "string") {
    return queryKey[2];
  }
  return getQueryKeyItemId(queryKey);
}

function listedChildIds(data: unknown): string[] {
  if (typeof data !== "object" || data === null) return [];
  const ids: string[] = [];
  for (const field of ["episodes", "seasons"] as const) {
    const list = (data as Record<string, unknown>)[field];
    if (!Array.isArray(list)) continue;
    for (const entry of list) {
      const id = (entry as { content_id?: unknown } | null)?.content_id;
      if (typeof id === "string") ids.push(id);
    }
  }
  return ids;
}

// relatedItemIds is itemId plus the items whose cached state derives from it,
// as far as the cache shows: its parents (the series named by its detail, and
// the season or series whose cached list holds it, and theirs) and its
// children (the episodes or seasons its own cached lists hold, the cached
// details that name it as their series, and theirs).
// Realtime events and watched marks name only the item itself, so narrowing
// to that one ID would leave an open season grid, series watched totals or a
// marked season's episodes stale.
export function relatedItemIds(queryClient: QueryClient, itemId: string): Set<string> {
  // Index season identities before the links, then walk them, so a
  // series with many cached seasons and episodes costs two cache scans,
  // not one per item.
  const childrenOf = new Map<string, string[]>();
  const parentsOf = new Map<string, string[]>();
  const queries = queryClient.getQueryCache().getAll();
  const seasonIds = new Map<string, Map<number, string>>();
  const rememberSeason = (
    seriesId: string,
    season: Pick<ItemDetail, "content_id" | "season_number"> | undefined,
  ) => {
    if (!season?.content_id || season.season_number == null) return;
    let seasons = seasonIds.get(seriesId);
    if (!seasons) {
      seasons = new Map();
      seasonIds.set(seriesId, seasons);
    }
    seasons.set(season.season_number, season.content_id);
  };
  for (const query of queries) {
    const owner = listOwnerId(query.queryKey);
    if (!owner) continue;
    if (query.queryKey[0] === "catalog" && query.queryKey[1] === "series") {
      for (const season of (query.state.data as SeasonsResponse | undefined)?.seasons ?? []) {
        rememberSeason(owner, season);
      }
      rememberSeason(owner, (query.state.data as SeasonDetailResponse | undefined)?.season);
    }
    if (isItemDetailQueryKey(query.queryKey, owner)) {
      const detail = query.state.data as ItemDetail | undefined;
      if (detail?.type === "season" && detail.series_id) {
        rememberSeason(detail.series_id, detail);
      }
    }
  }
  const add = (edges: Map<string, string[]>, from: string, to: string) => {
    const list = edges.get(from);
    if (list) list.push(to);
    else edges.set(from, [to]);
  };
  const link = (parent: string, child: string) => {
    if (parent === child) return;
    add(childrenOf, parent, child);
    add(parentsOf, child, parent);
  };
  for (const query of queries) {
    const owner = listOwnerId(query.queryKey);
    if (!owner) continue;
    // Numbered season episode keys name the series, while their members
    // belong to the season whose identity the first pass found.
    const numberedSeason =
      query.queryKey[0] === "catalog" &&
      query.queryKey[1] === "series" &&
      query.queryKey[5] === "episodes" &&
      typeof query.queryKey[4] === "number"
        ? seasonIds.get(owner)?.get(query.queryKey[4])
        : undefined;
    if (numberedSeason) link(owner, numberedSeason);
    for (const child of listedChildIds(query.state.data)) link(numberedSeason ?? owner, child);
    // A season's detail is cached under its series, so its own ID is only
    // in the data.
    const seasonId = (query.state.data as { season?: { content_id?: unknown } } | undefined)?.season
      ?.content_id;
    if (typeof seasonId === "string") link(owner, seasonId);
    if (isItemDetailQueryKey(query.queryKey, owner) || isWatchDetailQueryKey(query.queryKey)) {
      const detail = query.state.data as ItemDetail | undefined;
      const seriesId = detail?.series_id;
      // A detail names its series, and derives its series title, backdrop
      // and credits from it. A season page opened directly has no cached
      // season list, so this edge is how a series change reaches it. A
      // playing episode's watch detail names it too, so its progress
      // reaches the series' watched summary.
      if (seriesId) {
        link(seriesId, owner);
        const seasonId =
          detail.season_number == null
            ? undefined
            : seasonIds.get(seriesId)?.get(detail.season_number);
        if (seasonId) link(seasonId, owner);
      }
    }
  }

  const related = new Set([itemId]);
  const walk = (edges: Map<string, string[]>) => {
    const pending = [itemId];
    while (pending.length > 0) {
      for (const next of edges.get(pending.pop() as string) ?? []) {
        if (!related.has(next)) {
          related.add(next);
          pending.push(next);
        }
      }
    }
  };
  walk(parentsOf);
  walk(childrenOf);
  return related;
}

function shouldInvalidateMediaSurfaceQuery(
  queryKey: readonly unknown[],
  options: InvalidateMediaSurfaceOptions,
  relatedIds?: ReadonlySet<string>,
) {
  if (options.skipItemDetail && options.itemId && isItemDetailQueryKey(queryKey, options.itemId)) {
    return false;
  }
  if (options.skipItemQueries && options.itemId && queryKey.includes(options.itemId)) {
    return false;
  }

  if (options.itemId) {
    const targetItemId = getQueryKeyItemId(queryKey);
    // Keys for related items (see relatedItemIds) or named in watchedKeys (a
    // watched episode's season or series, say) belong to other item IDs by
    // design; don't narrow them away.
    if (
      targetItemId &&
      targetItemId !== options.itemId &&
      !relatedIds?.has(targetItemId) &&
      !isWatchedKey(queryKey, options) &&
      !(
        options.refreshItemKeyedLists &&
        (isItemKeyedListQuery(queryKey) ||
          (options.listOwnerIds?.has(targetItemId) && isItemDetailQueryKey(queryKey, targetItemId)))
      )
    ) {
      return false;
    }
  }

  if (queryKeyStartsWith(queryKey, catalogKeys.all)) {
    return activeCatalogQueryMatchesLibrary(queryKey, options.libraryId);
  }
  if (queryKeyStartsWith(queryKey, sectionKeys.all)) {
    return activeSectionQueryMatchesLibrary(queryKey, options.libraryId);
  }
  if (queryKeyStartsWith(queryKey, recKeys.all)) {
    return !(options.skipSimilarItems && queryKey[1] === "similar");
  }
  if (queryKeyStartsWith(queryKey, libraryCollectionKeys.all)) {
    return options.libraryId === undefined || queryKey[2] === options.libraryId;
  }

  if (MEDIA_SURFACE_PREFIXES.some((prefix) => queryKeyStartsWith(queryKey, prefix))) {
    return true;
  }

  return isWatchedKey(queryKey, options);
}

// isItemKeyedListQuery reports a list of a series' or season's children, keyed
// by the parent: a season list, a season's detail and episodes, or an item's
// episodes. A new child can't be traced to its parent's lists from the cache.
function isItemKeyedListQuery(queryKey: readonly unknown[]): boolean {
  return queryKey[0] === "catalog" && (queryKey[1] === "series" || queryKey[3] === "episodes");
}

// isWatchedKey is a prefix match on purpose: everything under a watched key
// (a watched season's or episode's detail variants) changes with it, so it is
// exempt from the itemId narrowing too. Don't tighten it to an exact match.
// An item key also matches a watched key for the same item in any library:
// watched keys carry the "default" library segment, while an item opened
// through its library caches under that library's ID.
function isWatchedKey(queryKey: readonly unknown[], options: InvalidateMediaSurfaceOptions) {
  const targetItemId = getQueryKeyItemId(queryKey);
  return (options.watchedKeys ?? []).some(
    (key) =>
      queryKeyStartsWith(queryKey, key) ||
      (targetItemId !== undefined &&
        getQueryKeyItemId(key) === targetItemId &&
        queryKeyStartsWith(queryKey, key.slice(0, 2))),
  );
}

export async function invalidateMediaSurfaceQueries(
  queryClient: QueryClient,
  options: InvalidateMediaSurfaceOptions = {},
) {
  // Duplicate refetches are held down by the coalescing scheduler below, not by
  // `cancelRefetch: false` — reusing an in-flight request would let a response
  // that predates the mutation satisfy the invalidation and land in the cache
  // as fresh.
  const relatedIds = options.itemId ? relatedItemIds(queryClient, options.itemId) : undefined;
  options.knownRelatedIds?.forEach((id) => relatedIds?.add(id));
  // A new item nothing in the cache knows about, such as an episode a scan
  // just added, can't be traced to the season or series lists that should now
  // show it, so its event refreshes every such list. Other items' detail
  // stays narrowed away. A caller that names the keys it changed
  // (watchedKeys) has scoped it already.
  const scoped =
    options.itemMayBeNew &&
    options.itemId &&
    !options.watchedKeys?.length &&
    relatedIds?.size === 1 &&
    !cacheHasItemDetail(queryClient, options.itemId)
      ? { ...options, refreshItemKeyedLists: true, listOwnerIds: itemKeyedListOwners(queryClient) }
      : options;
  const matches = (query: Query) =>
    shouldInvalidateMediaSurfaceQuery(query.queryKey, scoped, relatedIds);
  const runningLetterIndexes = refetchLetterIndexesWhenDone(queryClient, matches);
  await queryClient.invalidateQueries({
    predicate: (query) => matches(query) && !runningLetterIndexes.has(query),
  });
}

// itemKeyedListOwners is the items whose children a cached list holds.
function itemKeyedListOwners(queryClient: QueryClient): Set<string> {
  const owners = new Set<string>();
  for (const query of queryClient.getQueryCache().getAll()) {
    const owner = isItemKeyedListQuery(query.queryKey) ? listOwnerId(query.queryKey) : undefined;
    if (owner) owners.add(owner);
  }
  return owners;
}

// A pending query or a markers-only read does not establish item ancestry.
function cacheHasItemDetail(queryClient: QueryClient, itemId: string): boolean {
  return queryClient
    .getQueryCache()
    .getAll()
    .some(
      (query) =>
        getQueryKeyItemId(query.queryKey) === itemId &&
        (isItemDetailQueryKey(query.queryKey, itemId) || isWatchDetailQueryKey(query.queryKey)) &&
        query.state.data != null,
    );
}

// Reads that carry localized titles and descriptions beyond the media surfaces:
// the families a viewer access change refetches, shuffle cards, collection
// peeks, the Home-row editor's peeks, previews and curated-title search, and
// the Watch Together picker's rows and catalog searches (pickerKeys in the
// picker's usePickerData; its room presence and member state are not localized).
const LOCALIZED_READ_PREFIXES = [
  collectionKeys.all,
  episodeKeys.all,
  calendarKeys.all,
  shuffleKeys.all,
  ["collection-peek"],
  ["home-row-peek"],
  ["home-row-preview"],
  ["curatedListPicker"],
  ["watch-party", "picker"],
  ["watch-party", "search"],
  ["watch-party", "recently-added"],
];

function isLocalizedMediaQuery(query: Query) {
  return (
    shouldInvalidateMediaSurfaceQuery(query.queryKey, {}) ||
    LOCALIZED_READ_PREFIXES.some((prefix) => queryKeyStartsWith(query.queryKey, prefix))
  );
}

/**
 * Refreshes the localized media reads after the viewer's metadata language
 * changes. A request still in flight would answer in the old language and land
 * as fresh: TanStack reuses a running request that has no data yet instead of
 * restarting it, and an observer-less one (Home rows, prefetches) is never
 * refetched. So those requests are cancelled before the reads are invalidated.
 * Resolves once the reads are marked stale; mounted screens refetch on their
 * own, and Home's load queue restarts right away.
 */
export async function refreshLocalizedMediaQueries(queryClient: QueryClient) {
  await queryClient.cancelQueries({ predicate: isLocalizedMediaQuery });
  void queryClient.invalidateQueries({ predicate: isLocalizedMediaQuery });
  bumpHomeRefreshSignal(queryClient);
}

// A letter index sorts the whole result, which on a large library can outlast
// a scan's invalidation window, so cancelling and restarting it on every sweep
// would keep it from ever finishing. A running read finishes instead, and one
// more read follows it so changes made while it ran still land. Returns the
// running reads, for the sweep to leave alone.
function refetchLetterIndexesWhenDone(
  queryClient: QueryClient,
  matches: (query: Query) => boolean,
): Set<Query> {
  const running = queryClient.getQueryCache().findAll({
    queryKey: catalogKeys.allLetters,
    fetchStatus: "fetching",
    predicate: matches,
  });
  for (const query of running) {
    const refetchAfter = () =>
      queryClient.invalidateQueries(
        { queryKey: query.queryKey, exact: true },
        { cancelRefetch: false },
      );
    void query.promise?.then(refetchAfter, refetchAfter);
  }
  return new Set(running);
}

export function scheduleMediaSurfaceInvalidation(
  queryClient: QueryClient,
  options: InvalidateMediaSurfaceOptions = {},
) {
  let clientInvalidations = scheduledInvalidations.get(queryClient);
  if (!clientInvalidations) {
    clientInvalidations = new Map();
    scheduledInvalidations.set(queryClient, clientInvalidations);
  }

  const key = `${options.itemId ?? "all"}:${options.libraryId ?? "all"}`;
  const existing = clientInvalidations.get(key);
  if (existing) clearTimeout(existing.timer);

  const mergedOptions: InvalidateMediaSurfaceOptions = {
    ...existing?.options,
    ...options,
    // Skipping is safe only when every coalesced caller has already supplied
    // the canonical detail state. A single full-refresh request must win.
    skipItemDetail: existing
      ? Boolean(existing.options.skipItemDetail && options.skipItemDetail)
      : options.skipItemDetail,
    skipItemQueries: existing
      ? Boolean(existing.options.skipItemQueries && options.skipItemQueries)
      : options.skipItemQueries,
    skipSimilarItems: existing
      ? Boolean(existing.options.skipSimilarItems && options.skipSimilarItems)
      : options.skipSimilarItems,
    knownRelatedIds:
      existing?.options.knownRelatedIds || options.knownRelatedIds
        ? new Set([
            ...(existing?.options.knownRelatedIds ?? []),
            ...(options.knownRelatedIds ?? []),
          ])
        : undefined,
    watchedKeys: [...(existing?.options.watchedKeys ?? []), ...(options.watchedKeys ?? [])],
  };
  const now = Date.now();
  const deadline = existing?.deadline ?? now + MEDIA_SURFACE_REFRESH_MAX_WAIT_MS;
  const timer = setTimeout(
    () => {
      clientInvalidations?.delete(key);
      // Home reads its sections through one-shot `fetchQuery` calls with no
      // observers, so the signal has to be bumped after the invalidation lands
      // or Home re-reads a cache that is still marked fresh.
      void invalidateMediaSurfaceQueries(queryClient, mergedOptions).then(
        () => bumpHomeRefreshSignal(queryClient),
        () => bumpHomeRefreshSignal(queryClient),
      );
    },
    Math.max(0, Math.min(MEDIA_SURFACE_REFRESH_DELAY_MS, deadline - now)),
  );

  clientInvalidations.set(key, { timer, deadline, options: mergedOptions });
}
