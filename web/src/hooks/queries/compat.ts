import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import type { CompatConnectInfo } from "@/api/types";
import { compatKeys } from "./keys";

// Compat listener details only change when an admin edits server settings, so
// this can sit cached for the length of a session.
const CONNECT_INFO_STALE_MS = 5 * 60 * 1000;

export function useCompatConnectInfo() {
  return useQuery({
    queryKey: compatKeys.connectInfo(),
    queryFn: () => api<CompatConnectInfo>("/compat/connect-info"),
    staleTime: CONNECT_INFO_STALE_MS,
  });
}
