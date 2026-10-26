import type { AdminSession } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { classifyActivityMethod } from "@/pages/adminActivityPresentation";

import { episodeCode, formatPlayMethod } from "../format";
import { DetailCard, ListRow, ProgressBar } from "../ui";

function sessionTitle(session: AdminSession): string {
  if (session.series_name) {
    const code = episodeCode(session.season_number, session.episode_number);
    return code ? `${session.series_name} · ${code}` : session.series_name;
  }
  return session.media_title || "Unknown title";
}

function minutes(seconds: number | null | undefined) {
  return Math.max(0, Math.floor((seconds ?? 0) / 60));
}

/** What the account is playing right now; shown only while it plays something. */
export function WatchingNowCard({ sessions }: { sessions: AdminSession[] }) {
  const first = sessions[0];
  return (
    <DetailCard
      title={
        <span className="inline-flex items-center gap-2">
          <span aria-hidden="true" className="bg-success size-2 rounded-full" />
          Watching now
        </span>
      }
      actions={
        first ? (
          <Badge variant="outline">{formatPlayMethod(classifyActivityMethod(first))}</Badge>
        ) : null
      }
    >
      {sessions.map((session) => {
        const duration = session.file_duration;
        const meta = [
          session.profile_name ? `Profile ${session.profile_name}` : "",
          session.client_label || session.client_name || "",
          session.client_ip ?? "",
        ]
          .filter(Boolean)
          .join(" · ");
        return (
          <ListRow
            key={session.session_id}
            title={sessionTitle(session)}
            meta={
              <>
                {meta}
                {duration ? (
                  <ProgressBar
                    className="mt-1.5 max-w-52"
                    value={session.position_seconds / duration}
                  />
                ) : null}
              </>
            }
            trailing={
              duration
                ? `${minutes(session.position_seconds)} of ${Math.round(duration / 60)} min`
                : undefined
            }
          />
        );
      })}
    </DetailCard>
  );
}
