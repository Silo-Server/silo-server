import {
  type QueryClient,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { toast } from "sonner";
import type { Profile } from "@/api/types";
import { catalogItemFromV2 } from "@/api/v2/catalog";
import { v2, type V2Result } from "@/api/v2/request";
import { useProfiles } from "@/hooks/queries/profiles";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";

/**
 * Title allowances: the household's primary profile lets a limited profile
 * watch specific movies and series above its rating or advisory-age limit,
 * one by one or through a personal collection.
 */

export const titleAllowanceKeys = {
  all: ["titleAllowances"] as const,
  capability: () => ["titleAllowances", "capability"] as const,
  item: (itemId: string) => ["titleAllowances", "item", itemId] as const,
  titles: (profileId: string) => ["titleAllowances", "titles", profileId] as const,
  collections: (profileId: string) => ["titleAllowances", "collections", profileId] as const,
};

export type TitleAllowanceProfile =
  V2Result<"GET /api/v2/catalog/items/{id}/title-allowances">["items"][number];
export type AllowedCollection =
  V2Result<"GET /api/v2/profiles/{id}/allowed-collections">["items"][number];

/** Whether the server keeps allowances for this account. */
export function useTitleAllowancesAvailable(enabled = true): boolean {
  const { data } = useQuery({
    queryKey: titleAllowanceKeys.capability(),
    queryFn: async ({ signal }) => {
      const capability = await v2("GET /api/v2/capabilities/title-allowances", { signal });
      return capability.state === "available";
    },
    staleTime: 5 * 60 * 1000,
    enabled,
  });
  return enabled && data === true;
}

/**
 * Whether the acting profile can manage allowances: it is the household's
 * primary profile, another profile on the account has a limit to make an
 * exception to, and the server keeps allowances. The capability is read only
 * once the first two hold, so other households send no extra request.
 */
export function useCanManageTitleAllowances(): boolean {
  const { profile } = useCurrentProfile();
  const { data: profiles } = useProfiles({ enabled: profile?.is_primary === true });
  const relevant =
    profile?.is_primary === true &&
    profiles.some((other) => other.id !== profile.id && profileHasMaturityLimit(other));
  return useTitleAllowancesAvailable(relevant);
}

/** True when a profile has a rating or advisory-age limit an allowance can lift. */
export function profileHasMaturityLimit(
  profile: Pick<Profile, "max_content_rating" | "max_advisory_age">,
): boolean {
  return profile.max_content_rating.trim() !== "" || (profile.max_advisory_age ?? 0) > 0;
}

/** Describes a profile's limit, e.g. "PG-13 · Age 12+". */
export function describeMaturityLimit(limit: {
  max_content_rating: string;
  max_advisory_age?: number | null;
}): string {
  const parts: string[] = [];
  if (limit.max_content_rating.trim() !== "") parts.push(limit.max_content_rating);
  if ((limit.max_advisory_age ?? 0) > 0) parts.push(`Age ${limit.max_advisory_age}+`);
  return parts.join(" · ");
}

/** The rows the title dialog offers: every profile the title is not already within. */
export function allowanceDialogRows(profiles: TitleAllowanceProfile[]): TitleAllowanceProfile[] {
  return profiles.filter((p) => p.state !== "within_limits");
}

/** A row's checkbox state and note for a profile's standing. */
export function allowanceRowState(state: TitleAllowanceProfile["state"]): {
  checked: boolean;
  disabled: boolean;
  note?: string;
} {
  switch (state) {
    case "allowed":
      return { checked: true, disabled: false };
    case "allowed_by_collection":
      return { checked: true, disabled: true, note: "Allowed through a collection" };
    case "no_library_access":
      return { checked: false, disabled: true, note: "Can't see this library" };
    default:
      return { checked: false, disabled: false };
  }
}

/** Whether the title page should offer "Allow for…" at all. */
export function hasAllowableProfile(profiles: TitleAllowanceProfile[] | undefined): boolean {
  return (profiles ?? []).some(
    (p) =>
      p.state === "above_limit" || p.state === "allowed" || p.state === "allowed_by_collection",
  );
}

/** Every other profile's standing for a movie or series. */
export function useTitleAllowanceProfiles(itemId: string, enabled: boolean) {
  return useQuery({
    queryKey: titleAllowanceKeys.item(itemId),
    queryFn: async ({ signal }) => {
      const result = await v2("GET /api/v2/catalog/items/{id}/title-allowances", {
        path: { id: itemId },
        signal,
      });
      return result.items;
    },
    enabled: enabled && itemId !== "",
  });
}

/** Allowed titles read per page; the server's default page size. */
const ALLOWED_TITLES_PAGE_SIZE = 50;

/**
 * A profile's own allowed titles as cards, newest first, a page at a time.
 * `data.pages` holds each page's cards; `fetchNextPage` reads the next one.
 */
export function useAllowedTitles(profileId: string, enabled: boolean) {
  return useInfiniteQuery({
    queryKey: titleAllowanceKeys.titles(profileId),
    queryFn: async ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) => {
      const result = await v2("GET /api/v2/profiles/{id}/allowed-titles", {
        path: { id: profileId },
        query: { limit: ALLOWED_TITLES_PAGE_SIZE, cursor: pageParam || undefined },
        signal,
      });
      return {
        items: result.items.map(catalogItemFromV2),
        next: result.page?.has_more ? result.page.next_cursor : undefined,
      };
    },
    initialPageParam: "",
    getNextPageParam: (lastPage) => lastPage.next || undefined,
    enabled: enabled && profileId !== "",
  });
}

/** A profile's allowed collections, newest first. */
export function useAllowedCollections(profileId: string, enabled: boolean) {
  return useQuery({
    queryKey: titleAllowanceKeys.collections(profileId),
    queryFn: async ({ signal }) => {
      const result = await v2("GET /api/v2/profiles/{id}/allowed-collections", {
        path: { id: profileId },
        signal,
      });
      return result.items;
    },
    enabled: enabled && profileId !== "",
  });
}

/**
 * Refreshes what an allowance change for profileId can affect. Its title list
 * starts over at the first page: refetching an infinite list reads every
 * loaded page in turn.
 */
function refreshAllowances(queryClient: QueryClient, profileId: string) {
  void queryClient.resetQueries({ queryKey: titleAllowanceKeys.titles(profileId) });
  void queryClient.invalidateQueries({
    queryKey: titleAllowanceKeys.all,
    predicate: (query) => query.queryKey[1] !== "titles",
  });
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

/** Allows or stops allowing one title for a profile. */
export function useSetTitleAllowance() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      profileId,
      itemId,
      allowed,
    }: {
      profileId: string;
      itemId: string;
      allowed: boolean;
    }) =>
      allowed
        ? v2("PUT /api/v2/profiles/{id}/allowed-titles/{item_id}", {
            path: { id: profileId, item_id: itemId },
          })
        : v2("DELETE /api/v2/profiles/{id}/allowed-titles/{item_id}", {
            path: { id: profileId, item_id: itemId },
          }),
    onError: (err) => {
      toast.error(errorMessage(err, "Failed to change the allowed title"));
    },
    onSettled: (_data, _err, { profileId }) => {
      refreshAllowances(queryClient, profileId);
    },
  });
}

/** Allows or stops allowing a personal collection, live, for a profile. */
export function useSetCollectionAllowance() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      profileId,
      collectionId,
      allowed,
    }: {
      profileId: string;
      collectionId: string;
      allowed: boolean;
    }) =>
      allowed
        ? v2("PUT /api/v2/profiles/{id}/allowed-collections/{collection_id}", {
            path: { id: profileId, collection_id: collectionId },
          })
        : v2("DELETE /api/v2/profiles/{id}/allowed-collections/{collection_id}", {
            path: { id: profileId, collection_id: collectionId },
          }),
    onError: (err) => {
      toast.error(errorMessage(err, "Failed to change the allowed collection"));
    },
    onSettled: (_data, _err, { profileId }) => {
      refreshAllowances(queryClient, profileId);
    },
  });
}

/** Allows a profile every movie and series a collection holds now. */
export function useAllowCollectionTitles() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({
      profileId,
      collectionId,
    }: {
      profileId: string;
      collectionId: string;
    }) => {
      const result = await v2("POST /api/v2/profiles/{id}/allowed-titles/copy-collection", {
        path: { id: profileId },
        body: { collection_id: collectionId },
      });
      return result.added;
    },
    onError: (err) => {
      toast.error(errorMessage(err, "Failed to allow the collection's titles"));
    },
    onSettled: (_data, _err, { profileId }) => {
      refreshAllowances(queryClient, profileId);
    },
  });
}
