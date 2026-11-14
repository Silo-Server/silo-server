import { Play } from "lucide-react";
import "@/styles/player-hud.css";
import { useEpisodeSpoilerPrefs } from "@/hooks/useEpisodeSpoilerPrefs";
import { useT } from "@/i18n";
import { isEpisodeStill, isEpisodeUnwatched, SPOILER_IMAGE_CLASS } from "@/lib/episodeSpoilers";
import { NEXT_EPISODE_COUNTDOWN_SECONDS } from "../hooks/useNextEpisode";
import type { EpisodeRef } from "../types";

interface NextEpisodeOverlayProps {
  episode: EpisodeRef;
  secondsRemaining: number;
  onSkip: () => void;
  onCancel: () => void;
  /** Height of the control dock while it is up; the card sits just above it. */
  dockHeight?: number;
}

/**
 * Card shown during the credits region with a countdown to the next episode.
 */
export function NextEpisodeOverlay({
  episode,
  secondsRemaining,
  onSkip,
  onCancel,
  dockHeight = 0,
}: NextEpisodeOverlayProps) {
  const t = useT();
  const spoilerPrefs = useEpisodeSpoilerPrefs();
  const hideStill =
    spoilerPrefs.hideImages &&
    isEpisodeUnwatched(episode.watchState) &&
    isEpisodeStill(episode.stillIsEpisodeStill);
  const elapsed = Math.min(1, Math.max(0, 1 - secondsRemaining / NEXT_EPISODE_COUNTDOWN_SECONDS));

  return (
    <div
      role="region"
      aria-label={t("web.player.nextEpisode.regionLabel")}
      className="hud-card absolute right-[max(1rem,env(safe-area-inset-right))] z-50 w-[min(24rem,calc(100vw-2rem))] overflow-hidden transition-[bottom] duration-200 sm:right-[max(2rem,env(safe-area-inset-right))] rtl:right-auto rtl:left-[max(1rem,env(safe-area-inset-left))] sm:rtl:left-[max(2rem,env(safe-area-inset-left))]"
      style={{
        bottom:
          dockHeight > 0
            ? `calc(${dockHeight}px + 0.5rem)`
            : "max(1.5rem, env(safe-area-inset-bottom))",
      }}
    >
      <div className="flex gap-4 p-4">
        {episode.stillUrl ? (
          <div className="aspect-video w-28 shrink-0 overflow-hidden rounded-lg bg-white/[0.06]">
            <img
              src={episode.stillUrl}
              alt=""
              className={`h-full w-full object-cover ${hideStill ? SPOILER_IMAGE_CLASS : ""}`}
            />
          </div>
        ) : null}
        <div className="flex min-w-0 flex-col justify-center gap-1">
          <p className="text-xs font-semibold text-[var(--hud-ash)]" aria-live="polite">
            {t("web.player.nextEpisode.upNextIn", {
              count: secondsRemaining,
              num: (chunks) => (
                <span className="text-[var(--hud-lamp)] tabular-nums">{chunks}</span>
              ),
            })}
          </p>
          <p className="text-xs text-[var(--hud-ash)] tabular-nums">
            {t("web.player.nextEpisode.seasonEpisode", {
              season: episode.seasonNumber,
              episode: episode.episodeNumber,
            })}
          </p>
          <p className="line-clamp-2 text-base leading-snug font-semibold" title={episode.title}>
            {episode.title}
          </p>
        </div>
      </div>
      <div className="flex gap-2 px-4 pb-4">
        <button type="button" onClick={onSkip} className="hud-primary-btn flex-1">
          <Play fill="currentColor" strokeWidth={0} />
          {t("web.player.nextEpisode.playNow")}
        </button>
        <button type="button" onClick={onCancel} className="hud-secondary-btn">
          {t("common.cancel")}
        </button>
      </div>
      {/* Countdown as a lamp-coloured fill along the card's bottom edge. */}
      <div aria-hidden="true" className="h-1 bg-white/10">
        <div
          className="h-full bg-[var(--hud-lamp)] transition-[width] duration-1000 ease-linear"
          style={{ width: `${elapsed * 100}%` }}
        />
      </div>
    </div>
  );
}
