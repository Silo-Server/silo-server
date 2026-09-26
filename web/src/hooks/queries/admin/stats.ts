import { v2 } from "@/api/v2/request";
import { useQuery } from "@tanstack/react-query";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { AdminStats } from "@/api/types";
import { adminKeys } from "../keys";

import { listAdminPlaybackSessions } from "@/api/v2/adminSessions";
import { captureAdminUserAuthority } from "@/api/v2/adminUsers";

import { adminSessionsKey } from "@/api/v2/adminSessionsCache";

const ADMIN_STALE_TIME = 30_000;

export function adminStatsKey(context: ProfileRequestContextSnapshot) {
  return [
    ...adminKeys.stats(),
    context.serverOrigin,
    context.authContextVersion,
    context.profileId,
  ] as const;
}
export async function fetchAdminStats(
  options: { refresh?: boolean; profileContext?: ProfileRequestContextSnapshot } = {},
): Promise<AdminStats> {
  const profileContext = options.profileContext ?? captureProfileRequestContext();
  if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
    throw new StaleApiRequestContextError();
  const stats = await v2("GET /api/v2/admin/stats", {
    profileContext,
    query: { refresh: options.refresh ?? false },
  });
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  return stats;
}
export function useAdminStats() {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: profileContext ? adminStatsKey(profileContext) : [...adminKeys.stats(), null],
    enabled: profileContext !== null,
    queryFn: () => {
      if (!profileContext) throw new StaleApiRequestContextError();
      return fetchAdminStats({ profileContext });
    },
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminSessions() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: adminSessionsKey(context),
    queryFn: () => listAdminPlaybackSessions(context ?? captureAdminUserAuthority()),
    enabled: context !== null,
    staleTime: ADMIN_STALE_TIME,
  });
}
