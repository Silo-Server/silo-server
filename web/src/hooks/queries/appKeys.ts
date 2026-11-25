import { useQuery } from "@tanstack/react-query";
import { captureSessionIdentity } from "@/api/client";
import { v2 } from "@/api/v2/request";

/** Whether app keys sign in on this server now; the panel says so when they don't. */
export function useAppKeyCapability() {
  const session = captureSessionIdentity();
  return useQuery({
    queryKey: ["app-key-capability", session.serverOrigin],
    queryFn: ({ signal }) => v2("GET /api/v2/auth/keys/capability", { signal }),
    staleTime: 60_000,
  });
}
