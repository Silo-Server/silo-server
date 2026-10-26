import { Link } from "react-router";

import type { AdminUser } from "@/api/types";
import type { AdminUserPlay } from "@/api/v2/adminUserActivity";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminUserWatchHistory } from "@/hooks/queries/admin/userActivity";

import { episodeCode, formatDayTime, formatPlayMethod, watchedFraction } from "../format";
import { userDetailTabSearch } from "../userDetailTabs";
import { DetailCard, ListRow } from "../ui";

function playTitle(play: AdminUserPlay): string {
  if (play.series_title) {
    const code = episodeCode(play.season_number, play.episode_number);
    return code ? `${play.series_title} · ${code}` : play.series_title;
  }
  return play.media_title || "Unknown title";
}

/** The account's last four finished plays in the past 30 days. */
export function RecentlyWatchedCard({ user }: { user: AdminUser }) {
  const history = useAdminUserWatchHistory({ userId: user.id, days: 30, pageSize: 4 });
  const plays = history.data?.slice(0, 4);

  return (
    <DetailCard
      title="Recently watched"
      actions={
        <Button asChild variant="ghost" size="xs">
          <Link to={userDetailTabSearch("activity")}>All activity →</Link>
        </Button>
      }
    >
      {history.isError ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 px-4 py-4 text-sm sm:px-5">
          Couldn't load recent plays.
          <Button type="button" variant="outline" size="sm" onClick={() => history.refetch()}>
            Retry
          </Button>
        </div>
      ) : !plays ? (
        <div className="space-y-3 px-4 py-4 sm:px-5">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
        </div>
      ) : plays.length === 0 ? (
        <p className="text-muted-foreground px-4 py-6 text-center text-sm sm:px-5">
          Nothing watched in the last 30 days.
        </p>
      ) : (
        plays.map((play) => {
          const progress = play.completed
            ? "Finished"
            : `${Math.round(watchedFraction(play.watched_seconds, play.duration_seconds, false) * 100)}%`;
          return (
            <ListRow
              key={play.session_id}
              title={playTitle(play)}
              meta={[play.profile_name, formatPlayMethod(play.play_method), progress]
                .filter(Boolean)
                .join(" · ")}
              trailing={formatDayTime(play.ended_at)}
            />
          );
        })
      )}
    </DetailCard>
  );
}
