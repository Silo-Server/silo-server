import { useMemo, useState } from "react";
import { Link } from "react-router";
import { ArrowUpRight } from "lucide-react";

import type { AdminSession } from "@/api/types";
import type { AdminUserPlay } from "@/api/v2/adminUserActivity";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  useAdminUserLiveSessions,
  useAdminUserProfileActivity,
  useAdminUserWatchHistory,
  useAdminUserWatchSummary,
} from "@/hooks/queries/admin/userActivity";
import { cn } from "@/lib/utils";

import {
  episodeCode,
  formatPlayMethod,
  formatShortDate,
  playMethodBadgeClass,
  watchedFraction,
} from "../format";
import { DetailCard, ProgressBar } from "../ui";

const ALL_PROFILES = "all";
const PERIODS = [
  { days: 7, label: "Last 7 days" },
  { days: 30, label: "Last 30 days" },
  { days: 90, label: "Last 90 days" },
  { days: 365, label: "Last year" },
] as const;
const PAGE_SIZE = 25;

interface HistoryRow {
  key: string;
  live: boolean;
  itemId: string;
  title: string;
  code: string;
  profile: string;
  method: string;
  watched: number;
  endedAt: string;
}

function playRow(play: AdminUserPlay): HistoryRow {
  const episode = episodeCode(play.season_number, play.episode_number);
  return {
    key: play.session_id,
    live: false,
    itemId: play.media_item_id,
    title: play.series_title || play.media_title || play.media_item_id || "Unknown title",
    code: play.series_title ? episode : "",
    profile: play.profile_name || play.profile_id,
    method: play.play_method,
    watched: watchedFraction(play.watched_seconds, play.duration_seconds, play.completed),
    endedAt: play.ended_at,
  };
}

function liveRow(session: AdminSession): HistoryRow {
  return {
    key: `live:${session.session_id}`,
    live: true,
    itemId: session.content_id ?? "",
    title: session.series_name || session.media_title || "Unknown title",
    code: session.series_name ? episodeCode(session.season_number, session.episode_number) : "",
    profile: session.profile_name || session.profile_id,
    method: session.effective_play_method || session.play_method,
    watched: watchedFraction(session.position_seconds, session.file_duration, false),
    endedAt: "",
  };
}

export function WatchHistoryCard({
  userId,
  summaryAvailable,
}: {
  userId: number;
  summaryAvailable: boolean;
}) {
  const [profileId, setProfileId] = useState<string>(ALL_PROFILES);
  const [days, setDays] = useState(30);
  const profileFilter = profileId === ALL_PROFILES ? undefined : profileId;

  const profiles = useAdminUserProfileActivity(userId);
  const history = useAdminUserWatchHistory({
    userId,
    profileId: profileFilter,
    days,
    pageSize: PAGE_SIZE,
  });
  const summary = useAdminUserWatchSummary(userId, {
    days,
    profileId: profileFilter,
    enabled: summaryAvailable,
  });
  const live = useAdminUserLiveSessions(userId);

  const rows = useMemo(() => {
    const liveRows = (live.data ?? [])
      .filter((session) => !profileFilter || session.profile_id === profileFilter)
      .map(liveRow);
    return [...liveRows, ...(history.data ?? []).map(playRow)];
  }, [live.data, history.data, profileFilter]);

  const period = PERIODS.find((p) => p.days === days) ?? PERIODS[1];
  const plays = summaryAvailable ? summary.data?.plays : undefined;
  const shown = history.data?.length ?? 0;
  const historyLink =
    `/admin/history?user_id=${userId}` +
    (profileFilter ? `&profile_id=${encodeURIComponent(profileFilter)}` : "");
  // With no rows yet: loading text, the empty state, or nothing beside the error.
  const emptyMessage = history.isLoading
    ? "Loading watch history..."
    : history.isError
      ? null
      : "No plays in this period.";

  return (
    <DetailCard
      title="Watch history"
      description={
        plays !== undefined
          ? `${plays.toLocaleString()} ${plays === 1 ? "play" : "plays"} in the last ${days} days`
          : undefined
      }
      actions={
        <div className="flex flex-wrap items-center gap-2">
          <Select value={profileId} onValueChange={setProfileId}>
            <SelectTrigger size="sm" className="w-36" aria-label="Profile">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_PROFILES}>All profiles</SelectItem>
              {(profiles.data ?? []).map((profile) => (
                <SelectItem key={profile.id} value={profile.id}>
                  {profile.name || profile.id}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={String(days)} onValueChange={(value) => setDays(Number(value))}>
            <SelectTrigger size="sm" className="w-36" aria-label="Period">
              <SelectValue>{period.label}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {PERIODS.map((option) => (
                <SelectItem key={option.days} value={String(option.days)}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      }
      footer={
        <>
          <span className="text-muted-foreground mr-auto text-xs">
            {plays !== undefined ? `Showing ${shown} of ${plays}` : `Showing ${shown}`}
          </span>
          <Button variant="ghost" size="sm" asChild>
            <Link to={historyLink}>
              Open in Playback History
              <ArrowUpRight className="h-3 w-3" />
            </Link>
          </Button>
          {history.hasNextPage && (
            <Button
              variant="outline"
              size="sm"
              disabled={history.isFetchingNextPage}
              onClick={() => history.fetchNextPage()}
            >
              Load more
            </Button>
          )}
        </>
      }
    >
      {history.isError && (
        <div role="alert" className="flex items-center gap-3 px-5 py-4 text-sm">
          <span className="text-destructive">Couldn&apos;t load watch history.</span>
          <Button variant="outline" size="sm" onClick={() => history.refetch()}>
            Retry
          </Button>
        </div>
      )}
      {rows.length > 0 ? (
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4 sm:pl-5">Title</TableHead>
                <TableHead className="hidden sm:table-cell">Profile</TableHead>
                <TableHead>Method</TableHead>
                <TableHead className="hidden sm:table-cell">Watched</TableHead>
                <TableHead className="pr-4 sm:pr-5">When</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <TableRow key={row.key} data-live={row.live ? "true" : undefined}>
                  <TableCell className="max-w-[18rem] pl-4 whitespace-normal sm:pl-5">
                    {row.itemId ? (
                      <Link
                        to={`/item/${encodeURIComponent(row.itemId)}`}
                        className="hover:text-primary font-semibold transition-colors hover:underline"
                      >
                        {row.title}
                      </Link>
                    ) : (
                      <span className="font-semibold">{row.title}</span>
                    )}
                    {row.code ? (
                      <span className="text-muted-foreground ml-1.5 text-xs">{row.code}</span>
                    ) : null}
                  </TableCell>
                  <TableCell className="hidden sm:table-cell">{row.profile}</TableCell>
                  <TableCell>
                    <Badge
                      variant="outline"
                      className={cn("rounded-full", playMethodBadgeClass(row.method))}
                    >
                      {formatPlayMethod(row.method)}
                    </Badge>
                  </TableCell>
                  <TableCell className="hidden sm:table-cell">
                    <ProgressBar value={row.watched} className="w-24" />
                  </TableCell>
                  <TableCell className="text-muted-foreground pr-4 sm:pr-5">
                    {row.live ? (
                      <span className="text-foreground inline-flex items-center gap-1.5">
                        <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-emerald-400" />
                        Now
                      </span>
                    ) : (
                      formatShortDate(row.endedAt)
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : (
        emptyMessage && (
          <p className="text-muted-foreground px-5 py-8 text-center text-sm">{emptyMessage}</p>
        )
      )}
    </DetailCard>
  );
}
