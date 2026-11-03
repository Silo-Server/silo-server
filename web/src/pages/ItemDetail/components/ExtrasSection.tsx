import { useMemo, useState } from "react";
import { useLocation } from "react-router";
import { Play } from "lucide-react";
import type { ItemExtra } from "@/api/types";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { extraKindGroupLabel } from "@/lib/extraKinds";
import { formatClock } from "@/lib/markers";
import { decodeThumbhash } from "@/lib/thumbhash";

interface ExtrasSectionProps {
  extras: ItemExtra[];
}

/**
 * Local extras files grouped by kind, as 16:9 cards. Each card starts playback
 * through the same watch flow as the main Play button — an extra's content_id
 * is a first-class watch target.
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
    playbackController.startPlayback(
      {
        contentId: extra.content_id,
        returnHref: currentHref,
      },
      "viewer",
    );
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
            <ul
              role="list"
              className="grid list-none grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5"
            >
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
  const thumbhashUrl = extra.still_thumbhash ? decodeThumbhash(extra.still_thumbhash) : "";
  // A still that fails to load (an expired URL, a deleted file) falls back to
  // the placeholder rather than an empty frame.
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const stillUrl = extra.still_url && extra.still_url !== failedSrc ? extra.still_url : null;

  return (
    <button type="button" onClick={onPlay} className="group/extra block w-full text-left">
      <div
        className="media-card-image bg-muted relative mb-2 aspect-video overflow-hidden rounded-lg"
        style={
          thumbhashUrl
            ? { backgroundImage: `url(${thumbhashUrl})`, backgroundSize: "cover" }
            : undefined
        }
      >
        {stillUrl ? (
          <img
            src={stillUrl}
            alt=""
            className="h-full w-full object-cover transition-transform duration-300 group-hover/extra:scale-105"
            loading="lazy"
            decoding="async"
            onError={() => setFailedSrc(stillUrl)}
          />
        ) : (
          <div className="flex h-full w-full items-center justify-center">
            <Play className="text-muted-foreground/40 h-7 w-7" />
          </div>
        )}
        <div className="absolute inset-0 flex items-center justify-center bg-black/0 transition-colors duration-200 group-hover/extra:bg-black/30">
          <span className="flex h-10 w-10 items-center justify-center rounded-full bg-black/60 opacity-0 backdrop-blur-sm transition-opacity duration-200 group-hover/extra:opacity-100 group-focus-visible/extra:opacity-100">
            <Play className="ml-0.5 h-4 w-4 fill-white text-white" />
          </span>
        </div>
      </div>
      <div className="px-0.5">
        <span className="text-foreground block truncate text-[0.8125rem] font-medium">{title}</span>
        {extra.duration_seconds != null && extra.duration_seconds > 0 && (
          <span className="text-muted-foreground block text-[0.6875rem] tabular-nums">
            {formatClock(extra.duration_seconds)}
          </span>
        )}
      </div>
    </button>
  );
}
