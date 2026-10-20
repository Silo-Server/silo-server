import { Check } from "lucide-react";
import type { CSSProperties } from "react";

import type { MediaItemUserState } from "@/api/types";
import type { WatchStatusMode } from "@/lib/overlays";

import {
  EDGE_INSET,
  WATCH_STATUS_SIZE,
  overlayLength,
  supportsContainerQueryUnits,
} from "./overlayScale";

interface CardWatchStatusProps {
  userState: MediaItemUserState | undefined;
  mode: WatchStatusMode;
}

// The episodes a started, unfinished series or season has left, or null.
// An untouched one shows nothing: its count would be on every new show.
function remainingEpisodes(userState: MediaItemUserState | undefined): number | null {
  if (!userState || userState.played) return null;
  const watched = userState.watched_count ?? 0;
  const left = userState.unplayed_count ?? 0;
  return watched > 0 && left > 0 ? left : null;
}

// The poster's top-right corner: a check on a watched title, or the episodes
// a started series has left. No badge is ever placed in this corner (see
// CardOverlays), and it shows whether or not badges are on. The layer is
// sized from the card width the same way the badges are.
export default function CardWatchStatus({ userState, mode }: CardWatchStatusProps) {
  if (mode === "off") return null;
  const played = userState?.played === true;
  const left = mode === "remaining" ? remainingEpisodes(userState) : null;
  if (!played && left === null) return null;

  // Poster cards have no legacy CSS-variable pass for this corner; a browser
  // without container query units gets the reference size.
  const scaling = supportsContainerQueryUnits() ? "container" : "fixed";
  const length = (pixels: number) => overlayLength(pixels, scaling, "--card-watch-status");
  const size = length(WATCH_STATUS_SIZE);
  const style: CSSProperties = {
    top: length(EDGE_INSET),
    right: length(EDGE_INSET),
    minWidth: size,
    height: size,
    fontSize: length(11),
  };

  return (
    <div className="@container/card-watch-status pointer-events-none absolute inset-0 z-10">
      {played ? (
        <span
          role="img"
          aria-label="Watched"
          data-watched-indicator="poster"
          className="absolute grid place-items-center rounded-full border border-white/35 bg-emerald-500 text-white shadow-sm"
          style={style}
        >
          <Check aria-hidden="true" strokeWidth={3.25} style={{ width: "62%", height: "62%" }} />
        </span>
      ) : (
        <span
          role="img"
          aria-label={`${left} ${left === 1 ? "episode" : "episodes"} left`}
          data-watch-status-remaining={left}
          className="absolute grid place-items-center rounded-full border border-white/20 bg-black/65 px-[0.4em] leading-none font-bold text-white tabular-nums shadow-sm backdrop-blur-sm"
          style={style}
        >
          {left}
        </span>
      )}
    </div>
  );
}
