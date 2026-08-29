import { Link } from "react-router";
import { Play } from "lucide-react";
import { AdminSessionActions } from "@/components/AdminSessionActions";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import { getSessionClientLabel } from "@/pages/adminActivityPresentation";
import { formatRelativeTime } from "@/lib/date";
import { ActivitySkeletonRows, SectionError } from "../feedback";
import { SessionProfilePill } from "./SessionProfilePill";

export function RecentActivityWidget() {
  const sessionsQuery = useAdminSessions();
  const sessions = sessionsQuery.data ?? [];

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">Recent Activity</CardTitle>
        <Link
          to="/admin/activity"
          className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
        >
          View all ›
        </Link>
      </CardHeader>
      <CardContent className="min-h-0 flex-1 overflow-y-auto">
        {sessionsQuery.isLoading ? (
          <ActivitySkeletonRows />
        ) : sessionsQuery.error ? (
          <SectionError message="Failed to load activity." />
        ) : sessions.length === 0 ? (
          <div className="text-muted-foreground py-4 text-center text-sm">No recent activity.</div>
        ) : (
          <div className="space-y-0">
            {sessions.slice(0, 10).map((s) => {
              const isEp = s.series_name && s.season_number != null && s.episode_number != null;
              const title = isEp
                ? s.episode_name || `S${s.season_number}E${s.episode_number}`
                : s.media_title || `File #${s.media_file_id}`;
              const username = s.username || `User #${s.user_id}`;
              const profileDisplay = s.profile_name || s.profile_id || "";
              const clientLabel = getSessionClientLabel(s);
              const meta = [
                formatRelativeTime(s.started_at, { rounding: "floor", justNowLabel: "Just now" }),
                clientLabel,
              ]
                .filter(Boolean)
                .join(" · ");
              return (
                <div
                  key={s.session_id}
                  className="border-border/30 flex items-start gap-3 border-b py-2.5"
                >
                  <div className="text-primary bg-primary/5 border-primary/10 flex h-[30px] w-[30px] flex-shrink-0 items-center justify-center rounded-lg border">
                    <Play className="h-3.5 w-3.5" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="text-muted-foreground text-xs leading-relaxed">
                      <span className="text-foreground font-semibold">{username}</span>
                      {profileDisplay ? (
                        <>
                          {" "}
                          <SessionProfilePill label={profileDisplay} />
                        </>
                      ) : null}
                      {" started watching "}
                      <Link
                        to={`/admin/history?user_id=${s.user_id}${s.profile_id ? `&profile_id=${encodeURIComponent(s.profile_id)}` : ""}`}
                        className="text-foreground hover:text-primary font-semibold transition-colors"
                      >
                        {title}
                      </Link>
                    </div>
                    <div className="text-muted-foreground mt-0.5 text-[10px]">{meta}</div>
                  </div>
                  <div className="flex-shrink-0">
                    <AdminSessionActions session={s} compact />
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
