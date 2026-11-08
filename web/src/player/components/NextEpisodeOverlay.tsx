import { codeNumber, useT } from "@/i18n";
import type { EpisodeRef } from "../types";

interface NextEpisodeOverlayProps {
  episode: EpisodeRef;
  secondsRemaining: number;
  onSkip: () => void;
  onCancel: () => void;
}

/**
 * Overlay shown during the credits region with a countdown to the next episode.
 */
export function NextEpisodeOverlay({
  episode,
  secondsRemaining,
  onSkip,
  onCancel,
}: NextEpisodeOverlayProps) {
  const t = useT();
  return (
    <div className="absolute end-6 bottom-24 z-50 flex min-w-[260px] flex-col gap-2 rounded-lg bg-black/80 p-4 text-white">
      <div className="text-xs tracking-wider text-white/60 uppercase">
        {t("web.player.nextEpisode.countdown", { count: secondsRemaining })}
      </div>
      <div className="text-sm font-medium">
        {t("web.player.nextEpisode.episodeLine", {
          season: codeNumber(episode.seasonNumber),
          episode: codeNumber(episode.episodeNumber),
          title: episode.title,
        })}
      </div>
      <div className="mt-1 flex gap-2">
        <button
          onClick={onSkip}
          type="button"
          className="flex-1 rounded bg-white px-3 py-1.5 text-sm font-medium text-black transition-colors hover:bg-white/90"
        >
          {t("upNext.playNow")}
        </button>
        <button
          onClick={onCancel}
          type="button"
          className="rounded border border-white/30 px-3 py-1.5 text-sm transition-colors hover:bg-white/10"
        >
          {t("common.cancel")}
        </button>
      </div>
    </div>
  );
}
