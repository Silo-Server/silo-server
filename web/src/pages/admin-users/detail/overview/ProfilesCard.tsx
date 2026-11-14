import { Link } from "react-router";

import type { AdminSession, AdminUser } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminUserProfileActivity } from "@/hooks/queries/admin/userActivity";
import { useT } from "@/i18n";

import { formatLastSeen } from "../format";
import { userDetailTabSearch } from "../userDetailTabs";
import { DetailCard, InitialAvatar, ListRow } from "../ui";

/** The account's profiles and when each was last used. */
export function ProfilesCard({
  user,
  live,
}: {
  user: AdminUser;
  live: AdminSession[] | undefined | null;
}) {
  const t = useT();
  const profiles = useAdminUserProfileActivity(user.id);
  const watching = new Set((live ?? []).map((session) => session.profile_id));
  const count = profiles.data?.length;

  return (
    <DetailCard
      title={t("web.admin.users.detail.overview.profiles.title")}
      description={
        count === undefined
          ? undefined
          : t("web.admin.users.detail.common.profilesAllowed", {
              count: count,
              max: user.effective_policy.max_profiles,
            })
      }
      actions={
        <Button asChild variant="ghost" size="sm">
          <Link to={userDetailTabSearch("profiles")}>
            {t("web.admin.users.detail.overview.profiles.manage")}
          </Link>
        </Button>
      }
    >
      {profiles.isError ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 px-4 py-4 text-sm sm:px-5">
          {t("web.admin.users.detail.common.loadProfilesFailed")}
          <Button type="button" variant="outline" size="sm" onClick={() => void profiles.refetch()}>
            {t("common.retry")}
          </Button>
        </div>
      ) : !profiles.data ? (
        <div className="space-y-3 px-4 py-4 sm:px-5">
          <Skeleton className="h-8 w-full" />
        </div>
      ) : profiles.data.length === 0 ? (
        <p className="text-muted-foreground px-4 py-6 text-center text-sm sm:px-5">
          {t("web.admin.users.detail.common.noProfiles")}
        </p>
      ) : (
        profiles.data.map((profile, index) => (
          <ListRow
            key={profile.id}
            leading={<InitialAvatar name={profile.name} seed={index} src={profile.avatar_url} />}
            title={profile.name}
            trailing={
              watching.has(profile.id)
                ? t("web.admin.users.detail.common.watchingNow")
                : profile.last_seen_at
                  ? formatLastSeen(profile.last_seen_at)
                  : // Only device registrations are read here, and a failed read
                    // also comes back empty, so this can't claim "never used".
                    t("web.admin.users.detail.common.noDeviceActivity")
            }
          />
        ))
      )}
    </DetailCard>
  );
}
