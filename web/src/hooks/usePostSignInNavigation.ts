import { useCallback } from "react";
import { useNavigate } from "react-router";
import type { User } from "@/api/types";
import { listProfiles } from "@/hooks/queries/profiles";
import { getBootstrapProfile, useAuth } from "@/hooks/useAuth";

/** The screen a session holding a temporary password is confined to. */
export const CHANGE_PASSWORD_PATH = "/change-password";

/**
 * Where a fresh sign-in lands: first the password change a temporary password
 * requires, then the page the sign-in was asked for, else the sole unlocked
 * profile, else the profile picker.
 */
export function usePostSignInNavigation(redirectTarget: string | null) {
  const navigate = useNavigate();
  const { selectProfile } = useAuth();
  return useCallback(
    async (user: Pick<User, "password_change_required">) => {
      if (user.password_change_required) {
        const redirect = redirectTarget ? `?redirect=${encodeURIComponent(redirectTarget)}` : "";
        navigate(`${CHANGE_PASSWORD_PATH}${redirect}`, { replace: true });
        return;
      }
      if (redirectTarget) {
        navigate(redirectTarget, { replace: true });
        return;
      }

      try {
        const profileList = await listProfiles();
        const soleProfile = getBootstrapProfile(profileList.profiles ?? []);
        if (soleProfile) {
          selectProfile(soleProfile);
          navigate("/");
          return;
        }
      } catch {
        navigate("/profiles");
        return;
      }
      navigate("/profiles");
    },
    [navigate, redirectTarget, selectProfile],
  );
}
