import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import { catalogItemFromV2, type CatalogCardItem } from "@/api/v2/catalog";
import { captureProfileRequestContext } from "@/api/client";
import { v2 } from "@/api/v2/request";
import { recKeys, favoriteKeys } from "./keys";
import { invalidateMediaSurfaceQueries } from "./mediaSurfaceRefresh";
import { PERSONAL_STATE_WRITE_TIMEOUT_MS } from "./personalStateWrites";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";
import { storage } from "@/utils/storage";

export interface TasteSeedItemsPage {
  items: CatalogCardItem[];
  next_cursor?: string;
}

const TASTE_SEED_PAGE_SIZE = 30;

/**
 * Fetches catalog posters for the taste-seeding picker. Blends server engagement
 * with rating reliability and recency so even a fresh server with no watch
 * history surfaces meaningful posters. Items already favorited by the profile
 * carry user_state.is_favorite=true so the UI can pre-select them.
 */
export function useTasteSeedItems(enabled = true) {
  return useInfiniteQuery({
    queryKey: recKeys.tasteSeedItems(),
    queryFn: ({ pageParam, signal }): Promise<TasteSeedItemsPage> =>
      v2("GET /api/v2/recommendations/taste-seed/items", {
        query: { limit: TASTE_SEED_PAGE_SIZE, cursor: pageParam || undefined },
        signal,
      }).then((page) => ({
        items: page.items.map(catalogItemFromV2),
        next_cursor: page.page?.next_cursor,
      })),
    initialPageParam: "",
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    staleTime: 600_000,
    enabled,
  });
}

/**
 * When the recommendation surfaces are refetched again after a taste-seed
 * submission. The server answers once the profile's refresh is done or after
 * a few seconds; a slower refresh lands later, and these refetches pick it up.
 * A submission that arrives while a refresh is reading makes that refresh run
 * once more when it ends, each run within the server's two-minute timeout, so
 * the last refetch comes after two timeouts, when neither can be running.
 */
const TASTE_SEED_REFETCH_DELAYS_MS = [3_000, 15_000, 45_000, 125_000, 245_000] as const;

/** The pending delayed refetches of the latest submission. */
let tasteSeedRefetchTimers: ReturnType<typeof setTimeout>[] = [];

function cancelTasteSeedRefetches() {
  for (const timer of tasteSeedRefetchTimers) clearTimeout(timer);
  tasteSeedRefetchTimers = [];
}

/**
 * Schedules the delayed refetches for the current profile, replacing those of
 * an earlier submission. Each refetches only the recommendation reads, which
 * reload just the queries on screen; Home, which reloads every section on a
 * bump, is bumped once more with the last. A profile or account switch
 * cancels what is left: the refetches would only reload the new profile's
 * rows.
 */
function scheduleTasteSeedRefetches(queryClient: QueryClient) {
  cancelTasteSeedRefetches();
  const profileId = storage.get(storage.KEYS.PROFILE_ID);
  const last = TASTE_SEED_REFETCH_DELAYS_MS.length - 1;
  tasteSeedRefetchTimers = TASTE_SEED_REFETCH_DELAYS_MS.map((delay, i) =>
    setTimeout(() => {
      if (storage.get(storage.KEYS.PROFILE_ID) !== profileId) {
        cancelTasteSeedRefetches();
        return;
      }
      void queryClient.invalidateQueries({ queryKey: recKeys.all });
      if (i === last) {
        bumpHomeRefreshSignal(queryClient);
        tasteSeedRefetchTimers = [];
      }
    }, delay),
  );
}

/** What the picker changed: titles to add to favourites and favourites to remove. */
export interface TasteSeedChanges {
  add: string[];
  remove: string[];
}

export interface TasteSeedSaveResult {
  /** Favourites the server actually created; titles already favourited don't count. */
  added: number;
  removed: number;
}

/**
 * Saves the picker: submits the new picks as one batch, so the server triggers
 * a single taste-profile refresh rather than one per favourite, then removes
 * the favourites the user unselected.
 *
 * The batch goes first because the server waits for its refresh before
 * answering, and only when no refresh is already queued or running for the
 * profile; a removal sent before it would queue one and cut that wait short.
 * Each removal asks for a further refresh, which the delayed refetches pick up.
 * The batch endpoint needs at least one title, so a save that only removes
 * favourites skips it. Adding and removing are both idempotent, so a save that
 * failed partway can simply be sent again.
 *
 * Every request carries the profile captured when the save started, so a
 * profile switch partway through (in another tab, say) cannot send the rest of
 * the save as the newly active profile.
 */
export function useSubmitTasteSeed() {
  const queryClient = useQueryClient();
  // Favourites and recommendation surfaces. The server waits for the
  // profile's refresh before answering the batch, so the rows usually already
  // reflect the picks; the delayed refetches cover a refresh that took longer
  // than that wait, and the ones the removals asked for.
  const refreshSurfaces = async () => {
    await queryClient.invalidateQueries({ queryKey: favoriteKeys.all });
    await queryClient.invalidateQueries({ queryKey: recKeys.all });
    await invalidateMediaSurfaceQueries(queryClient);
    bumpHomeRefreshSignal(queryClient);
    scheduleTasteSeedRefetches(queryClient);
  };
  return useMutation({
    mutationFn: async ({ add, remove }: TasteSeedChanges): Promise<TasteSeedSaveResult> => {
      const profileContext = captureProfileRequestContext() ?? undefined;
      const added =
        add.length > 0
          ? (
              await v2("POST /api/v2/recommendations/taste-seed", {
                body: { item_ids: add },
                profileContext,
              })
            ).added
          : 0;
      // One at a time: each removal also notifies connected watch providers,
      // and the save stops at the first failure.
      for (const itemId of remove) {
        await v2("DELETE /api/v2/favorites/{item_id}", {
          path: { item_id: itemId },
          timeoutMs: PERSONAL_STATE_WRITE_TIMEOUT_MS,
          profileContext,
        });
      }
      return { added, removed: remove.length };
    },
    onSuccess: refreshSurfaces,
    // Part of the save may have landed, so refresh the same surfaces, without
    // holding up the error.
    onError: () => {
      void refreshSurfaces();
    },
  });
}
