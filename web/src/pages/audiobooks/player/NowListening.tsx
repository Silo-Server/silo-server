import { useEffect, useRef, useState, type CSSProperties } from "react";
import { ChevronDown, Pause, Play, SkipBack, SkipForward } from "lucide-react";
import { numberText, useT } from "@/i18n";
import { formatListeningTimeLeft } from "@/lib/audiobooks/duration";
import { getAverageColor } from "@/lib/thumbhash";
import { SeekBar } from "@/player/components/SeekBar";
import { formatTime } from "@/player/utils/formatTime";
import { ChaptersMenu } from "@/player/components/ChaptersMenu";
import { CircleButton } from "@/player/components/CircleButton";
import { SleepTimerMenu } from "@/player/components/SleepTimerMenu";
import { VolumeControl } from "@/player/components/VolumeControl";
import { PlayerSettingsMenu } from "./PlayerSettingsMenu";
import { SkipIcon } from "./SkipIcon";
import { SpeedControl, formatRate } from "./SpeedControl";
import type { AudiobookPlayback } from "./useAudiobookPlayback";
import type { AudiobookPrefs } from "./useAudiobookPrefs";

import "@/pages/audiobooks/audiobooks.css";

/** Chapters listed under "Coming up" beside the controls on wide screens. */
const COMING_UP_LIMIT = 5;

type TimeMode = "total" | "remaining" | "remaining-at-speed";

interface NowListeningProps {
  contentId: string;
  title: string;
  author?: string;
  narrator?: string;
  posterUrl: string;
  posterThumbhash?: string;
  playback: AudiobookPlayback;
  prefs: AudiobookPrefs;
  onCollapse: () => void;
}

export function NowListening({
  contentId,
  title,
  author,
  narrator,
  posterUrl,
  posterThumbhash,
  playback,
  prefs,
  onCollapse,
}: NowListeningProps) {
  const t = useT();
  const [timeMode, setTimeMode] = useState<TimeMode>("total");
  const rootRef = useRef<HTMLDivElement>(null);
  const collapseRef = useRef<HTMLButtonElement>(null);

  // The view covers the whole app, so take the app out of the tab order and
  // the accessibility tree while it is open, and start keyboard focus here.
  useEffect(() => {
    const root = rootRef.current;
    const covered = Array.from(root?.parentElement?.children ?? []).filter(
      (el): el is HTMLElement => el !== root && el instanceof HTMLElement && !el.inert,
    );
    for (const el of covered) el.inert = true;
    collapseRef.current?.focus();
    return () => {
      for (const el of covered) el.inert = false;
    };
  }, []);

  const remaining = Math.max(0, playback.duration - playback.currentTime);
  // "At current speed" only exists as a distinct reading when rate ≠ 1.
  const effectiveMode =
    timeMode === "remaining-at-speed" && playback.rate === 1 ? "remaining" : timeMode;
  const rightTimeLabel =
    effectiveMode === "total"
      ? formatTime(playback.duration)
      : effectiveMode === "remaining"
        ? t("web.audiobooks.player.timeRemaining", { time: formatTime(remaining) })
        : t("web.audiobooks.player.timeRemainingAtSpeed", {
            time: formatTime(remaining / playback.rate),
            rate: formatRate(t, playback.rate),
          });

  const cycleTimeMode = () => {
    setTimeMode((mode) => {
      if (mode === "total") return "remaining";
      if (mode === "remaining") {
        return playback.rate !== 1 ? "remaining-at-speed" : "total";
      }
      return "total";
    });
  };

  const hasChapters = playback.chapters.length > 0;
  const currentChapter = playback.currentChapter;
  const comingUp = currentChapter
    ? playback.chapters.slice(currentChapter.index + 1, currentChapter.index + 1 + COMING_UP_LIMIT)
    : [];
  const bookTimeLeft = formatListeningTimeLeft(playback.currentTime, playback.duration);
  const tint = posterThumbhash ? getAverageColor(posterThumbhash) : null;
  const tintStyle = tint ? ({ "--book-tint": tint } as CSSProperties) : undefined;
  const hasNextChapter =
    playback.currentChapter != null && playback.currentChapter.index + 1 < playback.chapters.length;

  return (
    <div
      ref={rootRef}
      className="audiobook-player bg-background fixed inset-0 z-50 flex flex-col overflow-y-auto"
      style={tintStyle}
    >
      <div className="sticky top-0 z-10 flex items-center justify-between px-6 py-4 backdrop-blur-md">
        <button
          ref={collapseRef}
          type="button"
          onClick={onCollapse}
          className="text-foreground/75 hover:text-foreground -ms-2 inline-flex items-center gap-1 rounded-full px-3 py-1.5 text-sm font-medium transition-colors hover:bg-white/[0.06]"
        >
          <ChevronDown className="h-5 w-5" />
          <span>{t("web.audiobooks.player.backToPlayer")}</span>
        </button>
        <PlayerSettingsMenu prefs={prefs} placement="below" />
      </div>

      <div className="grid flex-1 grid-cols-[minmax(0,1fr)] content-center items-center gap-8 px-6 pt-2 pb-10 sm:gap-10 md:grid-cols-[minmax(0,22rem)_minmax(0,32rem)] md:justify-center md:gap-12 md:px-12 xl:grid-cols-[minmax(0,24rem)_minmax(0,32rem)_17rem]">
        <div className="mx-auto w-full max-w-[min(70vw,320px)] md:mx-0 md:max-w-none">
          <div
            className="bg-muted aspect-square w-full overflow-hidden rounded-2xl shadow-[0_34px_70px_-24px_rgb(0_0_0/0.9)]"
            style={{ viewTransitionName: `audiobook-cover-${contentId}` }}
          >
            {posterUrl ? (
              <img src={posterUrl} alt={title} className="h-full w-full object-cover" />
            ) : null}
          </div>
        </div>

        <div className="flex min-w-0 flex-col gap-6 md:gap-7">
          <div className="space-y-1.5">
            {currentChapter && (
              <p className="text-foreground/70 text-sm">
                {t("web.audiobooks.hero.chapterPosition", {
                  index: currentChapter.index + 1,
                  total: playback.chapters.length,
                })}
              </p>
            )}
            <h1 className="text-[1.75rem] leading-tight font-extrabold tracking-[-0.025em] text-balance sm:text-[2.125rem]">
              {currentChapter ? currentChapter.title : title}
            </h1>
            {currentChapter && <p className="text-foreground/80 text-base">{title}</p>}
            {author && <p className="text-foreground/70 text-sm">{author}</p>}
            {narrator && (
              <p className="text-foreground/70 text-sm">
                {t("web.audiobooks.player.narratedBy", {
                  narrator,
                  name: (chunks) => <span className="text-foreground">{chunks}</span>,
                })}
              </p>
            )}
          </div>

          <div className="space-y-2">
            <SeekBar
              currentTime={playback.currentTime}
              duration={playback.duration}
              buffered={playback.buffered}
              chapters={playback.chapters}
              onSeek={playback.seekTo}
              onSkip={{
                back: () => playback.skip(-prefs.skipBack),
                forward: () => playback.skip(prefs.skipForward),
              }}
            />
            {/* Time and transport read left to right in every language, like the timeline. */}
            <div
              dir="ltr"
              className="text-muted-foreground flex items-center justify-between text-xs tabular-nums"
            >
              <span>{formatTime(playback.currentTime)}</span>
              <button
                type="button"
                data-testid="now-listening-right-time"
                onClick={cycleTimeMode}
                title={t("web.audiobooks.player.timeModeTitle")}
                className="hover:text-foreground transition-colors"
              >
                {rightTimeLabel}
              </button>
            </div>
            {bookTimeLeft && playback.duration > 0 && (
              <p className="text-foreground text-center text-sm font-semibold md:text-start">
                {bookTimeLeft}
              </p>
            )}
          </div>

          <div dir="ltr" className="flex items-center justify-center gap-3 sm:gap-4">
            {hasChapters && (
              <CircleButton
                size="sm"
                variant="secondary"
                ariaLabel={t("web.audiobooks.player.previousChapter")}
                title={t("web.audiobooks.player.previousChapterTitle")}
                onClick={playback.prevChapter}
                disabled={!playback.hasFile}
              >
                <SkipBack className="h-4 w-4" strokeWidth={0} fill="currentColor" />
              </CircleButton>
            )}

            <CircleButton
              size="sm"
              variant="secondary"
              ariaLabel={t("web.audiobooks.player.skipBack", { seconds: prefs.skipBack })}
              title={t("web.audiobooks.player.skipBackTitle", { seconds: prefs.skipBack })}
              className="group"
              onClick={() => playback.skip(-prefs.skipBack)}
              disabled={!playback.hasFile}
            >
              <SkipIcon direction="back" seconds={prefs.skipBack} size="lg" />
            </CircleButton>

            <CircleButton
              size="lg"
              variant="primary"
              ariaLabel={
                playback.playing
                  ? t("web.audiobooks.player.pause")
                  : t("web.audiobooks.player.play")
              }
              title={
                playback.playing
                  ? t("web.audiobooks.player.pauseTitle")
                  : t("web.audiobooks.player.playTitle")
              }
              onClick={playback.togglePlay}
              disabled={!playback.hasFile}
              data-paused={!playback.playing}
            >
              {playback.playing ? (
                <Pause className="h-8 w-8" strokeWidth={0} fill="currentColor" />
              ) : (
                // eslint-disable-next-line silo/no-physical-direction -- centres the play triangle, which doesn't mirror
                <Play className="ml-[2px] h-8 w-8" strokeWidth={0} fill="currentColor" />
              )}
            </CircleButton>

            <CircleButton
              size="sm"
              variant="secondary"
              ariaLabel={t("web.audiobooks.player.skipForward", { seconds: prefs.skipForward })}
              title={t("web.audiobooks.player.skipForwardTitle", { seconds: prefs.skipForward })}
              className="group"
              onClick={() => playback.skip(prefs.skipForward)}
              disabled={!playback.hasFile}
            >
              <SkipIcon direction="forward" seconds={prefs.skipForward} size="lg" />
            </CircleButton>

            {hasChapters && (
              <CircleButton
                size="sm"
                variant="secondary"
                ariaLabel={t("web.audiobooks.player.nextChapter")}
                title={t("web.audiobooks.player.nextChapterTitle")}
                onClick={playback.nextChapter}
                disabled={!playback.hasFile || !hasNextChapter}
              >
                <SkipForward className="h-4 w-4" strokeWidth={0} fill="currentColor" />
              </CircleButton>
            )}
          </div>

          <div className="text-muted-foreground flex flex-wrap items-center justify-center gap-4 text-sm sm:gap-6">
            <VolumeControl
              tone="surface"
              volume={playback.volume}
              muted={playback.muted}
              onVolumeChange={playback.setVolume}
              onMutedChange={playback.setMuted}
            />
            <SleepTimerMenu
              setting={playback.sleep.setting}
              remainingMs={playback.sleep.remainingMs}
              onChange={playback.setSleep}
            />
            {hasChapters && (
              <ChaptersMenu
                chapters={playback.chapters}
                currentTime={playback.currentTime}
                onSeek={playback.seekTo}
              />
            )}
            <SpeedControl value={playback.rate} onChange={playback.setRate} />
          </div>
        </div>

        {comingUp.length > 0 && (
          <aside className="bg-background/30 hidden self-center rounded-2xl border border-white/[0.07] p-3 backdrop-blur-md xl:grid">
            <h2 className="px-2 pt-1 pb-2 text-sm font-bold">
              {t("web.audiobooks.player.comingUp")}
            </h2>
            <ol className="grid gap-0.5">
              {comingUp.map((chapter) => (
                <li key={chapter.index}>
                  <button
                    type="button"
                    onClick={() => playback.seekTo(chapter.start_seconds)}
                    disabled={!playback.hasFile}
                    className="grid w-full grid-cols-[1.75rem_minmax(0,1fr)_auto] items-center gap-2 rounded-lg px-2 py-2 text-start text-sm transition-colors hover:bg-white/[0.06] disabled:opacity-60"
                  >
                    <span className="text-muted-foreground text-xs tabular-nums">
                      {numberText(chapter.index + 1)}
                    </span>
                    <span className="truncate font-medium">{chapter.title}</span>
                    <span dir="ltr" className="text-muted-foreground text-xs tabular-nums">
                      {formatTime(Math.max(0, chapter.end_seconds - chapter.start_seconds))}
                    </span>
                  </button>
                </li>
              ))}
            </ol>
          </aside>
        )}
      </div>
    </div>
  );
}
