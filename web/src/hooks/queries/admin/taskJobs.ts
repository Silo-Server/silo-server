import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import { adminTaskJobFromV2 } from "@/api/v2/adminTasks";
import { adminKeys } from "@/hooks/queries/keys";

export function useAdminTaskJobs(kind: string, limit = 20, poll = false) {
  const client = useQueryClient();
  const query = useInfiniteQuery({
    queryKey: [...adminKeys.jobs(kind || "__all"), "pages", limit],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      v2("GET /api/v2/admin/jobs", {
        query: { kind: kind || undefined, limit, cursor: pageParam },
      }),
    getNextPageParam: (page) => (page.page?.has_more ? page.page.next_cursor : undefined),
    staleTime: 0,
    refetchInterval: (currentQuery) => {
      if (!poll) return false;
      const latest = currentQuery.state.data?.pages[0]?.items[0];
      if (!latest) return false;
      const status = adminTaskJobFromV2(latest).status;
      return status === "queued" || status === "running" ? 2_000 : false;
    },
  });
  return {
    ...query,
    data: query.data?.pages.flatMap((page) => page.items.map(adminTaskJobFromV2)),
    restart: () =>
      client.resetQueries({
        queryKey: [...adminKeys.jobs(kind || "__all"), "pages", limit],
        exact: true,
      }),
  };
}
