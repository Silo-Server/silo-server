import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext } from "@/api/client";
import { adminUserScope, captureAdminUserAuthority } from "@/api/v2/adminUsers";
import {
  endedAfterFor,
  getAdminRequestUsage,
  getAdminUserDownloadSummary,
  getAdminUserWatchSummary,
  listAdminUserDevices,
  listAdminUserLiveSessions,
  listAdminUserPlays,
  listAdminUserProfileActivity,
  listAllAdminUserDownloadSubscriptions,
  listAllAdminUserDownloads,
  type AdminUserPlay,
} from "@/api/v2/adminUserActivity";
import { adminUsersKey } from "./users";

const ACTIVITY_STALE_TIME = 30_000;
const LIVE_REFRESH_INTERVAL = 15_000;

/**
 * Every key lives under the account-list scope, so the invalidations that
 * follow an account write (useUpdateUser, useDeleteUser, …) refresh these too,
 * and an authority change (profile, server, sign-in) never serves another
 * authority's cached rows.
 */
function useActivityScope() {
  const context = captureProfileRequestContext();
  return { context, base: adminUsersKey(adminUserScope(context)) };
}

function validUser(userId: number) {
  return Number.isSafeInteger(userId) && userId > 0;
}

export function useAdminUserLiveSessions(userId: number, enabled = true) {
  const { context, base } = useActivityScope();
  return useQuery({
    queryKey: [...base, "live-sessions", userId],
    queryFn: ({ signal }) =>
      listAdminUserLiveSessions(userId, context ?? captureAdminUserAuthority(), signal),
    enabled: enabled && context !== null && validUser(userId),
    retry: false,
    staleTime: LIVE_REFRESH_INTERVAL,
    refetchInterval: LIVE_REFRESH_INTERVAL,
  });
}

export function useAdminUserProfileActivity(userId: number) {
  const { context, base } = useActivityScope();
  return useQuery({
    queryKey: [...base, "profile-activity", userId],
    queryFn: ({ signal }) =>
      listAdminUserProfileActivity(userId, context ?? captureAdminUserAuthority(), signal),
    enabled: context !== null && validUser(userId),
    retry: false,
    staleTime: ACTIVITY_STALE_TIME,
  });
}

export function useAdminUserDevices(userId: number, enabled = true) {
  const { context, base } = useActivityScope();
  return useQuery({
    queryKey: [...base, "account-devices", userId],
    queryFn: ({ signal }) =>
      listAdminUserDevices(userId, context ?? captureAdminUserAuthority(), signal),
    enabled: enabled && context !== null && validUser(userId),
    retry: false,
    staleTime: ACTIVITY_STALE_TIME,
  });
}

export function useAdminUserWatchSummary(
  userId: number,
  opts: { days?: number; profileId?: string; enabled?: boolean } = {},
) {
  const { context, base } = useActivityScope();
  const days = opts.days ?? 30;
  const profileId = opts.profileId || undefined;
  return useQuery({
    queryKey: [...base, "watch-summary", userId, days, profileId ?? ""],
    queryFn: ({ signal }) =>
      getAdminUserWatchSummary(
        userId,
        { days, profileId },
        context ?? captureAdminUserAuthority(),
        signal,
      ),
    enabled: (opts.enabled ?? true) && context !== null && validUser(userId),
    retry: false,
    staleTime: ACTIVITY_STALE_TIME,
  });
}

export function useAdminUserRequestUsage(userId: number, enabled = true) {
  const { context, base } = useActivityScope();
  return useQuery({
    queryKey: [...base, "request-usage", userId],
    queryFn: ({ signal }) =>
      getAdminRequestUsage(userId, context ?? captureAdminUserAuthority(), signal),
    enabled: enabled && context !== null && validUser(userId),
    retry: false,
    staleTime: ACTIVITY_STALE_TIME,
  });
}

type PlayPageParam = { cursor?: string; endedAfter?: string } | undefined;

/**
 * The account's finalized plays in the last `days` days, newest first, one
 * page at a time. The first page fixes `ended_after`; later pages reuse it
 * because the server binds the cursor to that filter.
 */
export function useAdminUserWatchHistory(opts: {
  userId: number;
  profileId?: string;
  days: number;
  pageSize: number;
}): {
  data: AdminUserPlay[] | undefined;
  isLoading: boolean;
  isError: boolean;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  fetchNextPage: () => void;
  refetch: () => void;
} {
  const { context, base } = useActivityScope();
  const client = useQueryClient();
  const profileId = opts.profileId || undefined;
  const queryKey = [
    ...base,
    "watch-history",
    opts.userId,
    profileId ?? "",
    opts.days,
    opts.pageSize,
  ];
  const query = useInfiniteQuery({
    queryKey,
    initialPageParam: undefined as PlayPageParam,
    queryFn: async ({ pageParam, signal }) => {
      const endedAfter = pageParam?.endedAfter ?? endedAfterFor(opts.days);
      const page = await listAdminUserPlays(
        {
          userId: opts.userId,
          profileId,
          days: opts.days,
          limit: opts.pageSize,
          cursor: pageParam?.cursor,
          endedAfter,
        },
        context ?? captureAdminUserAuthority(),
        signal,
      );
      if (page.nextCursor) {
        // A cursor already used earlier in this walk means the server loops.
        // A refetch replays cached params, so only pages before this one count.
        const params =
          client.getQueryData<{ pageParams: PlayPageParam[] }>(queryKey)?.pageParams ?? [];
        const index = params.findIndex((param) => param?.cursor === pageParam?.cursor);
        const prior = index < 0 ? params : params.slice(0, index + 1);
        if (prior.some((param) => param?.cursor === page.nextCursor))
          throw new Error("Invalid watch history page. Reload the page.");
      }
      return { ...page, endedAfter };
    },
    getNextPageParam: (page): PlayPageParam =>
      page.nextCursor ? { cursor: page.nextCursor, endedAfter: page.endedAfter } : undefined,
    enabled: context !== null && validUser(opts.userId),
    retry: false,
    staleTime: ACTIVITY_STALE_TIME,
  });
  return {
    data: query.data?.pages.flatMap((page) => page.items),
    isLoading: query.isLoading,
    isError: query.isError,
    hasNextPage: query.hasNextPage,
    isFetchingNextPage: query.isFetchingNextPage,
    fetchNextPage: () => void query.fetchNextPage(),
    refetch: () => void client.resetQueries({ queryKey, exact: true }),
  };
}

export function useAdminUserDownloadSummary(userId: number, enabled = true) {
  const { context, base } = useActivityScope();
  return useQuery({
    queryKey: [...base, "download-summary", userId],
    queryFn: ({ signal }) =>
      getAdminUserDownloadSummary(userId, context ?? captureAdminUserAuthority(), signal),
    enabled: enabled && context !== null && validUser(userId),
    retry: false,
    staleTime: ACTIVITY_STALE_TIME,
  });
}

export function useAdminUserDownloads(userId: number, enabled = true) {
  const { context, base } = useActivityScope();
  return useQuery({
    queryKey: [...base, "downloads", userId],
    queryFn: ({ signal }) =>
      listAllAdminUserDownloads(userId, context ?? captureAdminUserAuthority(), signal),
    enabled: enabled && context !== null && validUser(userId),
    retry: false,
    staleTime: ACTIVITY_STALE_TIME,
  });
}

export function useAdminUserDownloadSubscriptions(userId: number, enabled = true) {
  const { context, base } = useActivityScope();
  return useQuery({
    queryKey: [...base, "download-subscriptions", userId],
    queryFn: ({ signal }) =>
      listAllAdminUserDownloadSubscriptions(userId, context ?? captureAdminUserAuthority(), signal),
    enabled: enabled && context !== null && validUser(userId),
    retry: false,
    staleTime: ACTIVITY_STALE_TIME,
  });
}
