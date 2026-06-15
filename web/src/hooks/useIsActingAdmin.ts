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
  const { profile, hasSelectedProfile } = useCurrentProfile();
  // Fail closed while a selected profile hasn't resolved yet (e.g. hard
  // refresh before the profiles query returns); otherwise a stored
  // non-primary profile would briefly look like "no profile selected" and
  // re-enable admin access until the query settles.
  if (hasSelectedProfile && !profile) return false;
  return isActingAdmin(user, profile);
}
