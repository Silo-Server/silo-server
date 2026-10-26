import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext, type ProfileRequestContextSnapshot } from "@/api/client";
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

/** One account read under `[...base, name, userId, ...rest]`, never retried. */
function useAccountQuery<T>(
  [name, userId, ...rest]: [name: string, userId: number, ...rest: unknown[]],
  read: (ctx: ProfileRequestContextSnapshot, signal: AbortSignal) => Promise<T>,
  enabled: boolean,
  timing: { staleTime: number; refetchInterval?: number } = { staleTime: ACTIVITY_STALE_TIME },
) {
  const { context, base } = useActivityScope();
  return useQuery({
    queryKey: [...base, name, userId, ...rest],
    queryFn: ({ signal }) => read(context ?? captureAdminUserAuthority(), signal),
    enabled: enabled && context !== null && validUser(userId),
    retry: false,
    ...timing,
  });
}

export function useAdminUserLiveSessions(userId: number, enabled = true) {
  return useAccountQuery(
    ["live-sessions", userId],
    (ctx, signal) => listAdminUserLiveSessions(userId, ctx, signal),
    enabled,
    { staleTime: LIVE_REFRESH_INTERVAL, refetchInterval: LIVE_REFRESH_INTERVAL },
  );
}

export function useAdminUserProfileActivity(userId: number) {
  return useAccountQuery(
    ["profile-activity", userId],
    (ctx, signal) => listAdminUserProfileActivity(userId, ctx, signal),
    true,
  );
}

export function useAdminUserDevices(userId: number, enabled = true) {
  return useAccountQuery(
    ["account-devices", userId],
    (ctx, signal) => listAdminUserDevices(userId, ctx, signal),
    enabled,
  );
}

export function useAdminUserWatchSummary(
  userId: number,
  opts: { days?: number; profileId?: string; enabled?: boolean } = {},
) {
  const days = opts.days ?? 30;
  const profileId = opts.profileId || undefined;
  return useAccountQuery(
    ["watch-summary", userId, days, profileId ?? ""],
    (ctx, signal) => getAdminUserWatchSummary(userId, { days, profileId }, ctx, signal),
    opts.enabled ?? true,
  );
}

export function useAdminUserRequestUsage(userId: number, enabled = true) {
  return useAccountQuery(
    ["request-usage", userId],
    (ctx, signal) => getAdminRequestUsage(userId, ctx, signal),
    enabled,
  );
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
  return useAccountQuery(
    ["download-summary", userId],
    (ctx, signal) => getAdminUserDownloadSummary(userId, ctx, signal),
    enabled,
  );
}

export function useAdminUserDownloads(userId: number, enabled = true) {
  return useAccountQuery(
    ["downloads", userId],
    (ctx, signal) => listAllAdminUserDownloads(userId, ctx, signal),
    enabled,
  );
}

export function useAdminUserDownloadSubscriptions(userId: number, enabled = true) {
  return useAccountQuery(
    ["download-subscriptions", userId],
    (ctx, signal) => listAllAdminUserDownloadSubscriptions(userId, ctx, signal),
    enabled,
  );
}
