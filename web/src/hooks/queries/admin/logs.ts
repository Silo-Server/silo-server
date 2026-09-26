import { useQuery } from "@tanstack/react-query";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import type { AuditLogListResponse, OperationalLogListResponse } from "@/api/types";
import { adminKeys } from "../keys";

export interface AdminLogQuery {
  from?: string;
  to?: string;
  cursor?: string;
  limit?: number;
  /** One level, or a comma-separated list of them (e.g. `"error,warn"`). */
  level?: string;
  component?: string;
  node_id?: string;
  request_id?: string;
  user_id?: number;
  session_id?: string;
  playback_session_id?: string;
  q?: string;
  method?: string;
  status_code?: number;
  path_prefix?: string;
  client_ip?: string;
}

function numericLogID(value: string): number {
  const parsed = Number(value);
  if (!/^[1-9][0-9]*$/.test(value) || !Number.isSafeInteger(parsed))
    throw new Error("Log identifier cannot be represented by this client.");
  return parsed;
}

export function useOperationalLogs(params: AdminLogQuery, enabled = true) {
  const profileContext = captureProfileRequestContext();
  const query = {
    cursor: params.cursor,
    limit: params.limit,
    from: params.from,
    to: params.to,
    level: params.level,
    component: params.component,
    node_id: params.node_id,
    request_id: params.request_id,
    user_id: params.user_id === undefined ? undefined : String(params.user_id),
    session_id: params.session_id,
    playback_session_id: params.playback_session_id,
    q: params.q,
  };
  return useQuery({
    queryKey: [
      ...adminKeys.operationalLogs(query),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
      profileContext?.profileTokenGeneration,
    ],
    queryFn: async (): Promise<OperationalLogListResponse> => {
      if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const page = await v2("GET /api/v2/admin/logs/app", { query, profileContext });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      if (!page.page || (page.page.has_more && !page.page.next_cursor))
        throw new Error("Log response is missing pagination metadata.");
      return {
        entries: page.items.map((entry) => ({
          ...entry,
          id: numericLogID(entry.id),
          user_id: entry.user_id == null ? undefined : numericLogID(entry.user_id),
        })),
        next_cursor: page.page.next_cursor ?? undefined,
      };
    },
    staleTime: 5_000,
    enabled: enabled && profileContext !== null,
  });
}

export function useAuditLogs(params: AdminLogQuery, enabled = true) {
  const profileContext = captureProfileRequestContext();
  const query = {
    cursor: params.cursor,
    limit: params.limit,
    from: params.from,
    to: params.to,
    method: params.method,
    status_code: params.status_code === undefined ? undefined : String(params.status_code),
    path_prefix: params.path_prefix,
    client_ip: params.client_ip,
    request_id: params.request_id,
    user_id: params.user_id === undefined ? undefined : String(params.user_id),
    session_id: params.session_id,
    playback_session_id: params.playback_session_id,
  };
  return useQuery({
    queryKey: [
      ...adminKeys.auditLogs(query),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
      profileContext?.profileTokenGeneration,
    ],
    queryFn: async (): Promise<AuditLogListResponse> => {
      if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const page = await v2("GET /api/v2/admin/logs/audit", { query, profileContext });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      if (!page.page || (page.page.has_more && !page.page.next_cursor))
        throw new Error("Log response is missing pagination metadata.");
      return {
        entries: page.items.map((entry) => ({
          ...entry,
          id: numericLogID(entry.id),
          user_id: entry.user_id == null ? undefined : numericLogID(entry.user_id),
          impersonator_user_id:
            entry.impersonator_user_id == null
              ? undefined
              : numericLogID(entry.impersonator_user_id),
        })),
        next_cursor: page.page.next_cursor ?? undefined,
      };
    },
    staleTime: 5_000,
    enabled: enabled && profileContext !== null,
  });
}
