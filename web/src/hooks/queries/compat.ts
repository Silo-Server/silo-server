import { useQuery } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";
import { compatKeys } from "./keys";

// Compat listener details only change when an admin edits server settings, so
// this can sit cached for the length of a session.
const CONNECT_INFO_STALE_MS = 5 * 60 * 1000;

export function useCompatConnectInfo() {
  return useQuery({
    queryKey: compatKeys.connectInfo(),
    queryFn: () => v2("GET /api/v2/compat/connect-info"),
    staleTime: CONNECT_INFO_STALE_MS,
  });
}
