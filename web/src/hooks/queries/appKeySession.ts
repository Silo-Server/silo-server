import { useQuery } from "@tanstack/react-query";
import { captureSessionIdentity } from "@/api/client";
import { v2, type V2Result } from "@/api/v2/request";
import { useAuth } from "@/hooks/useAuth";

/** What this login session may do with the account's Silo app keys. */
export type AppKeySessionAccess = V2Result<"GET /api/v2/auth/keys/session">;

/**
 * Whether this browser session may list or manage the account's app keys,
 * and whether signing in again would help. Keyed by profile, because
 * `approve` follows the active profile.
 */
export function useAppKeySessionAccess(enabled = true) {
  const session = captureSessionIdentity();
  const { profile } = useAuth();
  return useQuery({
    queryKey: [
      "app-key-session-access",
      session.serverOrigin,
      session.authContextVersion,
      profile?.id ?? null,
    ],
    enabled,
    queryFn: () => v2("GET /api/v2/auth/keys/session"),
  });
}

/** The server's app key capability: whether app keys are on, and which parts. */
export function useAppKeyCapability() {
  const session = captureSessionIdentity();
  return useQuery({
    queryKey: ["app-key-capability", session.serverOrigin],
    queryFn: () => v2("GET /api/v2/auth/keys/capability"),
  });
}
