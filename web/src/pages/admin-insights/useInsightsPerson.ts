import { useAdminUserProfiles } from "@/hooks/queries/admin/history";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { useT } from "@/i18n";

import type { InsightsPersonScope } from "./insightsHelpers";

/**
 * Names for a person scope: the account's username, its household profiles,
 * and the scoped profile's name. A profile the account's store no longer lists
 * keeps the People tab's "Profile …" label rather than reading as the account.
 */
export function useInsightsPerson(person: InsightsPersonScope | undefined) {
  const t = useT();
  const { data: users = [] } = useAdminUsers();
  const profiles = useAdminUserProfiles(person?.userId);
  const accountName = users.find((u) => u.id === person?.userId)?.username;
  const listedProfile = profiles.data?.find((p) => p.id === person?.profileId);
  const profileLabel =
    listedProfile?.name ??
    (person?.profileId
      ? t("web.admin.insights.people.unnamedProfile", { id: person.profileId.slice(0, 8) })
      : undefined);
  return {
    accountName,
    profiles: profiles.data ?? [],
    profileListed: Boolean(listedProfile),
    profileLabel,
    /** The profile when scoped to one, else the account. */
    name: profileLabel ?? accountName,
  };
}
