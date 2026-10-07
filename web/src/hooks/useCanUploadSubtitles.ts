import { useOptionalAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { canUploadSubtitles } from "@/lib/permissions";

/**
 * Whether the signed-in viewer may upload subtitle files right now. The server
 * enforces subtitle_upload either way; this only decides what to show.
 */
export function useCanUploadSubtitles() {
  const user = useOptionalAuth()?.user;
  const { profile, hasSelectedProfile } = useCurrentProfile();
  // Fail closed while a selected profile hasn't resolved yet: a null profile
  // reads as "no profile selected", which would briefly grant an admin on a
  // non-primary profile the acting-admin bypass (see useIsActingAdmin).
  if (hasSelectedProfile && !profile) return false;
  return canUploadSubtitles(user, profile);
}
