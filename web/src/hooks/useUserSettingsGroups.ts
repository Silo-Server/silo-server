import { useMemo } from "react";

import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { useAppLanguage } from "@/i18n";
import { visibleUserSettingsGroups, type UserSettingsNavGroup } from "@/lib/userSettingsNav";

/** The personal settings pages the signed-in profile can open, grouped. */
export function useUserSettingsGroups(): UserSettingsNavGroup[] {
  const { profile } = useCurrentProfile();
  const actingAdmin = useIsActingAdmin();
  const canManageProfiles = actingAdmin || profile?.is_primary === true;
  const requestsEnabled = useRequestFeatureStatus().data?.requests_enabled === true;
  // The labels are translated when built, so a language change rebuilds them.
  const language = useAppLanguage();

  return useMemo(
    () => visibleUserSettingsGroups({ canManageProfiles, requestsEnabled }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `language` changes what t() returns inside.
    [canManageProfiles, requestsEnabled, language],
  );
}
