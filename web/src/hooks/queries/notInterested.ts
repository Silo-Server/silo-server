import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { BrowseItem } from "@/api/types";
import { catalogItemFromV2 } from "@/api/v2/catalog";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";
import { notInterestedKeys } from "./keys";
import {
  cancelItemDetailQueries,
  removeItemFromHomeSectionCaches,
  scheduleMediaSurfaceInvalidation,
  updateCatalogItemDetail,
} from "./mediaSurfaceRefresh";
import {
  PERSONAL_STATE_WRITE_TIMEOUT_MS,
  personalStateMutationOptions,
} from "./personalStateWrites";

/**
 * The Home section types a Not interested title leaves: the recommendation
 * rows and the discovery rows the server filters (sections.HidesNotInterested).
 * Rows of what is new, in progress, saved or chosen keep it.
 */
export const NOT_INTERESTED_HIDING_SECTION_TYPES: ReadonlySet<string> = new Set([
  "recommended_for_you",
  "because_you_watched",
  "similar_users_liked",
  "taste_match",
  "hidden_gems",
  "critically_acclaimed",
  "award_winners",
  "forgotten_favorites",
  "editorial_spotlight",
  "seasonal_themed",
  "mood_collection",
  "trending_on_server",
  "most_watched",
  "trending_discover",
  "genre_roulette",
  "anniversaries",
  "short_watches",
]);

/** Only movies and series can be marked; a series mark covers its episodes. */
export function canMarkNotInterested(type: string | undefined): boolean {
  return type === "movie" || type === "series";
}

const NOT_INTERESTED_PAGE_SIZE = 50;

interface NotInterestedPage {
  items: BrowseItem[];
  next_cursor?: string;
}

/** The acting profile's marked titles, newest mark first, a page at a time. */
export function useNotInterestedList() {
  return useInfiniteQuery({
    queryKey: notInterestedKeys.list(),
    queryFn: async ({
      pageParam,
      signal,
    }: {
      pageParam: string;
      signal: AbortSignal;
    }): Promise<NotInterestedPage> => {
      const page = await v2("GET /api/v2/not-interested", {
        query: { limit: NOT_INTERESTED_PAGE_SIZE, cursor: pageParam || undefined },
        signal,
      });
      return {
        items: page.items.map(catalogItemFromV2),
        next_cursor: page.page?.has_more ? page.page.next_cursor : undefined,
      };
    },
    initialPageParam: "",
    getNextPageParam: (lastPage) => lastPage.next_cursor,
  });
}

function setDetailNotInterested(
  queryClient: ReturnType<typeof useQueryClient>,
  itemId: string,
  notInterested: boolean,
) {
  updateCatalogItemDetail(queryClient, itemId, (detail) => ({
    ...detail,
    user_state: {
      ...detail.user_state,
      played: detail.user_state?.played ?? detail.user_data?.played ?? false,
      is_favorite: detail.user_state?.is_favorite ?? false,
      in_watchlist: detail.user_state?.in_watchlist ?? false,
      not_interested: notInterested,
    },
  }));
}

/**
 * Marks or clears the acting profile's Not interested mark. The argument is
 * whether the title is marked now. A new mark takes the title off the Home
 * recommendation and discovery rows at once and offers an undo; the server
 * rebuilds the rows within seconds.
 */
export function useToggleNotInterested(itemId: string) {
  const queryClient = useQueryClient();

  const settle = () => {
    void queryClient.invalidateQueries({ queryKey: notInterestedKeys.all });
    scheduleMediaSurfaceInvalidation(queryClient, { itemId, skipItemDetail: true });
  };

  // Undo clears the mark as the profile that made it, whichever profile is
  // active when it is clicked, and patches caches only while that profile is
  // still the active one.
  const undo = useMutation({
    ...personalStateMutationOptions,
    mutationFn: (authority: ProfileRequestContextSnapshot) =>
      v2("DELETE /api/v2/not-interested/{item_id}", {
        path: { item_id: itemId },
        profileContext: authority,
        timeoutMs: PERSONAL_STATE_WRITE_TIMEOUT_MS,
      }),
    onMutate: async (authority) => {
      if (!isCapturedProfileAuthorityActive(authority)) return;
      await cancelItemDetailQueries(queryClient, itemId);
      setDetailNotInterested(queryClient, itemId, false);
    },
    onError: (_err, authority) => {
      if (isCapturedProfileAuthorityActive(authority)) {
        setDetailNotInterested(queryClient, itemId, true);
      }
      toast.error("Failed to undo Not interested");
    },
    onSettled: (_data, _err, authority) => {
      if (isCapturedProfileAuthorityActive(authority)) settle();
    },
  });

  const toggle = useMutation({
    ...personalStateMutationOptions,
    mutationFn: ({ marked, authority }: ToggleVariables) =>
      marked
        ? v2("DELETE /api/v2/not-interested/{item_id}", {
            path: { item_id: itemId },
            profileContext: authority ?? undefined,
            timeoutMs: PERSONAL_STATE_WRITE_TIMEOUT_MS,
          })
        : v2("PUT /api/v2/not-interested/{item_id}", {
            path: { item_id: itemId },
            profileContext: authority ?? undefined,
            timeoutMs: PERSONAL_STATE_WRITE_TIMEOUT_MS,
          }),
    onMutate: async ({ marked, authority }) => {
      if (!isActive(authority)) return;
      await cancelItemDetailQueries(queryClient, itemId);
      setDetailNotInterested(queryClient, itemId, !marked);
    },
    onError: (_err, { marked, authority }) => {
      if (isActive(authority)) setDetailNotInterested(queryClient, itemId, marked);
      toast.error("Failed to update Not interested");
    },
    onSuccess: (_data, { marked, authority }) => {
      if (marked) {
        toast.success("Back in your recommendations");
        return;
      }
      if (isActive(authority)) {
        removeItemFromHomeSectionCaches(queryClient, itemId, NOT_INTERESTED_HIDING_SECTION_TYPES);
        bumpHomeRefreshSignal(queryClient);
      }
      toast.success(
        "We won't recommend this",
        authority
          ? { action: { label: "Undo", onClick: () => undo.mutate(authority) } }
          : undefined,
      );
    },
    onSettled: (_data, _err, { authority }) => {
      if (isActive(authority)) settle();
    },
  });

  // The profile is captured when the action is chosen: the write goes out as
  // that profile and its cache updates apply only while it is still active.
  return {
    isPending: toggle.isPending,
    mutate: (marked: boolean) =>
      toggle.mutate({ marked, authority: captureProfileRequestContext() }),
    mutateAsync: (marked: boolean) =>
      toggle.mutateAsync({ marked, authority: captureProfileRequestContext() }),
  };
}

interface ToggleVariables {
  /** Whether the title is marked now. */
  marked: boolean;
  authority: ProfileRequestContextSnapshot | null;
}

/** A write with no captured profile ran as whichever profile was active. */
function isActive(authority: ProfileRequestContextSnapshot | null) {
  return authority === null || isCapturedProfileAuthorityActive(authority);
}
