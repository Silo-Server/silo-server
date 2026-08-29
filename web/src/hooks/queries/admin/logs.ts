import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { AuditLogListResponse, OperationalLogListResponse } from "@/api/types";
import { adminKeys } from "../keys";

export interface AdminLogQuery {
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

function toQueryString(params: AdminLogQuery) {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    search.set(key, String(value));
  }
  return search.toString();
}

export function useOperationalLogs(params: AdminLogQuery, enabled = true) {
  const qs = toQueryString(params);
  return useQuery({
    queryKey: adminKeys.operationalLogs({ ...params }),
    queryFn: () => api<OperationalLogListResponse>(`/admin/logs/app${qs ? `?${qs}` : ""}`),
    staleTime: 5_000,
    enabled,
  });
}

export function useAuditLogs(params: AdminLogQuery, enabled = true) {
  const qs = toQueryString(params);
  return useQuery({
    queryKey: adminKeys.auditLogs({ ...params }),
    queryFn: () => api<AuditLogListResponse>(`/admin/logs/audit${qs ? `?${qs}` : ""}`),
    staleTime: 5_000,
    enabled,
  });
}
