import type { AdminSession } from "@/api/types";
import { getSessionRouteNodes, type ActivityRouteNode } from "@/pages/adminActivityPresentation";

const BADGE_COLORS: Record<ActivityRouteNode["kind"], string> = {
  transcode: "border-warning/25 bg-warning/10 text-warning",
  proxy: "border-info/25 bg-info/10 text-info",
  server: "border-primary/10 bg-primary/5 text-primary",
  legacy: "border-primary/10 bg-primary/5 text-primary",
};

/** Compact, role-labeled badges for each participant in a playback route. */
export function PlaybackRouteBadges({ session }: { session: AdminSession }) {
  return getSessionRouteNodes(session).map((node) => (
    <span
      key={node.key}
      title={`${node.label}: ${node.name}`}
      aria-label={`${node.label}: ${node.name}`}
      className={`inline-flex max-w-full items-center gap-1 rounded border px-1.5 py-0.5 text-[9px] font-semibold ${BADGE_COLORS[node.kind]}`}
    >
      {node.kind === "transcode" || node.kind === "proxy" ? (
        <>
          <span className="opacity-70">{node.label}</span>
          <span aria-hidden="true">·</span>
        </>
      ) : null}
      <span className="truncate">{node.name}</span>
    </span>
  ));
}
