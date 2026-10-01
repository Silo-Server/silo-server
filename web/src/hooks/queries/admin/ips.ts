import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import { adminKeys } from "../keys";
function authority() {
  const value = captureProfileRequestContext();
  if (!value) throw new StaleApiRequestContextError();
  return value;
}
function scope() {
  const c = captureProfileRequestContext();
  return c ? `${c.serverOrigin}:${c.authContextVersion}:${c.profileId}` : "unavailable";
}
function numericUserID(value: string) {
  const id = Number(value);
  if (!Number.isSafeInteger(id) || id <= 0)
    throw new Error("Unsupported account ID in IP history.");
  return id;
}
function nextPage(page: { has_more: boolean; next_cursor?: string } | undefined, cursor?: string) {
  if (
    !page ||
    typeof page.has_more !== "boolean" ||
    (page.has_more &&
      (!page.next_cursor || typeof page.next_cursor !== "string" || page.next_cursor === cursor)) ||
    (!page.has_more && page.next_cursor)
  )
    throw new Error("Invalid activity page. Reload history.");
  return page.has_more ? page.next_cursor : undefined;
}
export function useUserIPs(userId: number, days = 30) {
  const identity = scope();
  const client = useQueryClient();
  const queryKey = [...adminKeys.userIPs(userId, days), identity];
  const query = useInfiniteQuery({
    queryKey,
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const c = authority();
      if (scope() !== identity) throw new StaleApiRequestContextError();
      const result = await v2("GET /api/v2/admin/users/{id}/ips", {
        path: { id: String(userId) },
        query: { days, limit: 50, cursor: pageParam },
        profileContext: c,
      });
      if (!isCapturedProfileAuthorityActive(c)) throw new StaleApiRequestContextError();
      if (!Array.isArray(result.items)) throw new Error("Invalid activity items. Reload history.");
      const next = nextPage(result.page, pageParam);
      const params =
        client.getQueryData<{ pageParams: (string | undefined)[] }>(queryKey)?.pageParams ?? [];
      const index = params.indexOf(pageParam);
      const prior = index < 0 ? params : params.slice(0, index + 1);
      if (next && prior.includes(next))
        throw new Error("Activity cursor repeated. Reload history.");
      return result;
    },
    getNextPageParam: (page) => nextPage(page.page),
    retry: false,
    staleTime: 30000,
  });
  return {
    ...query,
    restart: () => client.resetQueries({ queryKey, exact: true }),
    data: query.data?.pages.flatMap((page) => page.items),
  };
}
export function useIPUsers(ip: string, days = 30) {
  const identity = scope();
  const client = useQueryClient();
  const queryKey = [...adminKeys.ipUsers(ip, days), identity];
  const query = useInfiniteQuery({
    queryKey,
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const c = authority();
      if (scope() !== identity) throw new StaleApiRequestContextError();
      const result = await v2("GET /api/v2/admin/ips", {
        query: { ip, days, limit: 50, cursor: pageParam },
        profileContext: c,
      });
      if (!isCapturedProfileAuthorityActive(c)) throw new StaleApiRequestContextError();
      if (!Array.isArray(result.items)) throw new Error("Invalid activity items. Reload history.");
      for (const row of result.items) numericUserID(row.user_id);
      const next = nextPage(result.page, pageParam);
      const params =
        client.getQueryData<{ pageParams: (string | undefined)[] }>(queryKey)?.pageParams ?? [];
      const index = params.indexOf(pageParam);
      const prior = index < 0 ? params : params.slice(0, index + 1);
      if (next && prior.includes(next))
        throw new Error("Activity cursor repeated. Reload history.");
      return result;
    },
    getNextPageParam: (page) => nextPage(page.page),
    enabled: ip.length > 0,
    retry: false,
    staleTime: 30000,
  });
  return {
    ...query,
    restart: () => client.resetQueries({ queryKey, exact: true }),
    data: query.data?.pages.flatMap((page) =>
      page.items.map((row) => ({ ...row, user_id: numericUserID(row.user_id) })),
    ),
  };
}
