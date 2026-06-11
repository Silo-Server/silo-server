import { useOptionalAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { isActingAdmin } from "@/lib/permissions";

/**
 * Whether the signed-in account is acting with admin powers right now.
 * See isActingAdmin in lib/permissions for the policy. Safe to call from
 * components that may render outside AuthProvider (returns false there).
 */
export function useIsActingAdmin() {
  const user = useOptionalAuth()?.user;
  const { profile } = useCurrentProfile();
  return isActingAdmin(user, profile);
}
