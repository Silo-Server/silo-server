import { useMemo } from "react";
import { useLocation } from "react-router";
import { Play } from "lucide-react";
import type { ItemExtra } from "@/api/types";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { extraKindGroupLabel } from "@/lib/extraKinds";
import { formatClock } from "@/lib/markers";

interface ExtrasSectionProps {
  extras: ItemExtra[];
}

/**
 * Local extras files grouped by kind. Each card starts playback through the
 * same watch flow as the main Play button — an extra's content_id is a
 * first-class watch target.
 */
export default function ExtrasSection({ extras }: ExtrasSectionProps) {
  const location = useLocation();
  const playbackController = useWatchPlaybackController();
  const currentHref = `${location.pathname}${location.search}`;

  // Server order is already kind-sorted; group while preserving that order.
  const groups = useMemo(() => {
    const byKind = new Map<string, ItemExtra[]>();
    for (const extra of extras) {
      const group = byKind.get(extra.kind);
      if (group) {
        group.push(extra);
      } else {
        byKind.set(extra.kind, [extra]);
      }
    }
    return [...byKind.entries()];
  }, [extras]);

  if (groups.length === 0) return null;

  const playExtra = (extra: ItemExtra) => {
    playbackController.startPlayback({
      contentId: extra.content_id,
      returnHref: currentHref,
    });
  };

  return (
    <div>
      <h2 className="mb-5 text-xl font-semibold tracking-tight">Extras</h2>
      <div className="space-y-6">
        {groups.map(([kind, groupExtras]) => (
          <div key={kind}>
            <h3 className="text-muted-foreground mb-2.5 text-xs font-semibold tracking-wider uppercase">
              {extraKindGroupLabel(kind)}
            </h3>
            <ul role="list" className="grid list-none gap-2.5 sm:grid-cols-2 lg:grid-cols-3">
              {groupExtras.map((extra) => (
                <li key={extra.content_id}>
                  <ExtraCard extra={extra} onPlay={() => playExtra(extra)} />
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    </div>
  );
}

function ExtraCard({ extra, onPlay }: { extra: ItemExtra; onPlay: () => void }) {
  const title = extra.title || extraKindGroupLabel(extra.kind);

  return (
    <button
      type="button"
      onClick={onPlay}
      className="group/extra border-border bg-surface hover:bg-surface-hover flex w-full items-center gap-3 rounded-lg border px-3 py-2.5 text-left transition-colors duration-150"
    >
      <span className="bg-muted text-foreground group-hover/extra:bg-primary group-hover/extra:text-primary-foreground flex h-9 w-9 shrink-0 items-center justify-center rounded-full transition-colors duration-150">
        <Play className="ml-0.5 h-4 w-4 fill-current" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="text-foreground block truncate text-[13px] font-medium">{title}</span>
        {extra.duration_seconds != null && extra.duration_seconds > 0 && (
          <span className="text-muted-foreground block text-[11px] tabular-nums">
            {formatClock(extra.duration_seconds)}
          </span>
        )}
      </span>
    </button>
  );
}
