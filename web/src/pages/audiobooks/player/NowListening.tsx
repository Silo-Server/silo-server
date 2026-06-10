import { useState } from "react";
import { ChevronDown, Pause, Play, SkipBack, SkipForward } from "lucide-react";
import { SeekBar, formatTime } from "@/player/components/SeekBar";
import { ChaptersMenu } from "@/player/components/ChaptersMenu";
import { CircleButton } from "@/player/components/CircleButton";
import { SleepTimerMenu } from "@/player/components/SleepTimerMenu";
import { VolumeControl } from "@/player/components/VolumeControl";
import { PlayerSettingsMenu } from "./PlayerSettingsMenu";
import { SkipIcon } from "./SkipIcon";
import { SpeedControl, formatRate } from "./SpeedControl";
import type { AudiobookPlayback } from "./useAudiobookPlayback";
import type { AudiobookPrefs } from "./useAudiobookPrefs";

type TimeMode = "total" | "remaining" | "remaining-at-speed";

interface NowListeningProps {
  contentId: string;
  title: string;
  author?: string;
  narrator?: string;
  posterUrl: string;
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
  playback,
  prefs,
  onCollapse,
}: NowListeningProps) {
  const [timeMode, setTimeMode] = useState<TimeMode>("total");

  const remaining = Math.max(0, playback.duration - playback.currentTime);
  // "At current speed" only exists as a distinct reading when rate ≠ 1.
  const effectiveMode =
    timeMode === "remaining-at-speed" && playback.rate === 1 ? "remaining" : timeMode;
  const rightTimeLabel =
    effectiveMode === "total"
      ? formatTime(playback.duration)
      : effectiveMode === "remaining"
        ? `-${formatTime(remaining)}`
        : `-${formatTime(remaining / playback.rate)} at ${formatRate(playback.rate)}`;

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
  const hasNextChapter =
    playback.currentChapter != null && playback.currentChapter.index + 1 < playback.chapters.length;

  return (
    <div className="bg-background fixed inset-0 z-50 flex flex-col overflow-y-auto">
      <div className="bg-background/95 sticky top-0 z-10 flex items-center justify-between px-6 py-4 backdrop-blur">
        <button
          type="button"
          onClick={onCollapse}
          className="text-muted-foreground hover:bg-muted hover:text-foreground -ml-2 inline-flex items-center gap-1 rounded-full px-3 py-1.5 text-sm font-medium transition-colors"
        >
          <ChevronDown className="h-5 w-5" />
          <span>Back to player</span>
        </button>
        <PlayerSettingsMenu prefs={prefs} />
      </div>

      <div className="grid flex-1 grid-cols-1 items-center gap-8 px-6 pt-2 pb-10 sm:gap-10 md:grid-cols-[auto_1fr] md:px-16">
        <div className="mx-auto w-full max-w-[min(70vw,320px)] md:mx-0 md:max-w-[360px]">
          <div
            className="bg-muted aspect-square w-full overflow-hidden rounded-2xl shadow-2xl"
            style={{ viewTransitionName: `audiobook-cover-${contentId}` }}
          >
            {posterUrl ? (
              <img src={posterUrl} alt={title} className="h-full w-full object-cover" />
            ) : null}
          </div>
        </div>

        <div className="flex max-w-xl flex-col gap-6 md:gap-8">
          <div className="space-y-1">
            <h1 className="text-3xl font-semibold tracking-tight">{title}</h1>
            {author && <p className="text-muted-foreground text-base">{author}</p>}
            {narrator && (
              <p className="text-muted-foreground text-sm">
                Narrated by <span className="text-foreground">{narrator}</span>
              </p>
            )}
          </div>

          {playback.currentChapter && (
            <div className="space-y-1">
              <p className="text-muted-foreground text-[11px] tracking-[0.18em] uppercase">
                Chapter {playback.currentChapter.index + 1}
              </p>
              <p className="text-foreground text-lg font-medium">{playback.currentChapter.title}</p>
            </div>
          )}

          <div className="space-y-2">
            <SeekBar
              currentTime={playback.currentTime}
              duration={playback.duration}
              buffered={playback.buffered}
              chapters={playback.chapters}
              onSeek={playback.seekTo}
            />
            <div className="text-muted-foreground flex items-center justify-between text-xs tabular-nums">
              <span>{formatTime(playback.currentTime)}</span>
              <button
                type="button"
                data-testid="now-listening-right-time"
                onClick={cycleTimeMode}
                title="Toggle total / remaining / remaining at speed"
                className="hover:text-foreground transition-colors"
              >
                {rightTimeLabel}
              </button>
            </div>
          </div>

          <div className="flex items-center justify-center gap-3 sm:gap-4">
            {hasChapters && (
              <CircleButton
                size="sm"
                variant="secondary"
                ariaLabel="Previous chapter"
                title="Previous chapter (P)"
                onClick={playback.prevChapter}
                disabled={!playback.hasFile}
              >
                <SkipBack className="h-4 w-4" strokeWidth={0} fill="currentColor" />
              </CircleButton>
            )}

            <CircleButton
              size="sm"
              variant="secondary"
              ariaLabel={`Back ${prefs.skipBack} seconds`}
              title={`Back ${prefs.skipBack} seconds (←)`}
              className="group"
              onClick={() => playback.skip(-prefs.skipBack)}
              disabled={!playback.hasFile}
            >
              <SkipIcon direction="back" seconds={prefs.skipBack} size="lg" />
            </CircleButton>

            <CircleButton
              size="lg"
              variant="primary"
              ariaLabel={playback.playing ? "Pause" : "Play"}
              title={playback.playing ? "Pause (space)" : "Play (space)"}
              onClick={playback.togglePlay}
              disabled={!playback.hasFile}
              data-paused={!playback.playing}
            >
              {playback.playing ? (
                <Pause className="h-8 w-8" strokeWidth={0} fill="currentColor" />
              ) : (
                <Play className="ml-[2px] h-8 w-8" strokeWidth={0} fill="currentColor" />
              )}
            </CircleButton>

            <CircleButton
              size="sm"
              variant="secondary"
              ariaLabel={`Forward ${prefs.skipForward} seconds`}
              title={`Forward ${prefs.skipForward} seconds (→)`}
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
                ariaLabel="Next chapter"
                title="Next chapter (N)"
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
      </div>
    </div>
  );
}
