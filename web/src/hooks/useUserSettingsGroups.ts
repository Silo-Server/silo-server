import { useMemo } from "react";

import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { visibleUserSettingsGroups, type UserSettingsNavGroup } from "@/lib/userSettingsNav";

/** The personal settings pages the signed-in profile can open, grouped. */
export function useUserSettingsGroups(): UserSettingsNavGroup[] {
  const { profile } = useCurrentProfile();
  const actingAdmin = useIsActingAdmin();
  const canManageProfiles = actingAdmin || profile?.is_primary === true;
  const requestsEnabled = useRequestFeatureStatus().data?.requests_enabled === true;

  return useMemo(
    () => visibleUserSettingsGroups({ canManageProfiles, requestsEnabled }),
    [canManageProfiles, requestsEnabled],
  );
}
