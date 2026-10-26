import type { AdminUser } from "@/api/types";
import { useAdminUserLiveSessions } from "@/hooks/queries/admin/userActivity";

import { AccessSummaryCard } from "./AccessSummaryCard";
import { DetailsCard } from "./DetailsCard";
import { ProfilesCard } from "./ProfilesCard";
import { RecentlyWatchedCard } from "./RecentlyWatchedCard";
import { StatsRow } from "./StatsRow";
import { WatchingNowCard } from "./WatchingNowCard";

/** Who the account is, what it is doing now, and a short summary of its access. */
export function OverviewTab({ user }: { user: AdminUser }) {
  const liveQuery = useAdminUserLiveSessions(user.id);
  const live = liveQuery.isError ? null : liveQuery.data;

  return (
    <div className="space-y-4">
      <StatsRow user={user} live={live} />
      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-4">
          {live && live.length > 0 ? <WatchingNowCard sessions={live} /> : null}
          <RecentlyWatchedCard user={user} />
        </div>
        <div className="flex min-w-0 flex-col gap-4">
          <AccessSummaryCard user={user} />
          <ProfilesCard user={user} live={live} />
          <DetailsCard user={user} />
        </div>
      </div>
    </div>
  );
}
