import type { AdminSession, AdminUser } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminUserProfileActivity } from "@/hooks/queries/admin/userActivity";

import { formatLastSeen } from "../format";
import { DetailCard, InitialAvatar, ListRow } from "../ui";

/** The account's profiles and when each was last used. */
export function ProfilesCard({
  user,
  live,
}: {
  user: AdminUser;
  live: AdminSession[] | undefined | null;
}) {
  const profiles = useAdminUserProfileActivity(user.id);
  const watching = new Set((live ?? []).map((session) => session.profile_id));
  const count = profiles.data?.length;

  return (
    <DetailCard
      title="Profiles"
      description={
        count === undefined
          ? undefined
          : `${count} of ${user.effective_policy.max_profiles} allowed`
      }
    >
      {profiles.isError ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 px-4 py-4 text-sm sm:px-5">
          Couldn't load profiles.
          <Button type="button" variant="outline" size="sm" onClick={() => void profiles.refetch()}>
            Retry
          </Button>
        </div>
      ) : !profiles.data ? (
        <div className="space-y-3 px-4 py-4 sm:px-5">
          <Skeleton className="h-8 w-full" />
        </div>
      ) : profiles.data.length === 0 ? (
        <p className="text-muted-foreground px-4 py-6 text-center text-sm sm:px-5">
          This account has no profiles.
        </p>
      ) : (
        profiles.data.map((profile, index) => (
          <ListRow
            key={profile.id}
            leading={<InitialAvatar name={profile.name} seed={index} />}
            title={profile.name}
            trailing={
              watching.has(profile.id)
                ? "Watching now"
                : profile.last_seen_at
                  ? formatLastSeen(profile.last_seen_at)
                  : // Only device registrations are read here, and a failed read
                    // also comes back empty, so this can't claim "never used".
                    "No device activity"
            }
          />
        ))
      )}
    </DetailCard>
  );
}
