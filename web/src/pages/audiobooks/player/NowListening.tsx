import { useState } from "react";
import { ChevronDown, MoreHorizontal, Pause, Play, RotateCcw, RotateCw } from "lucide-react";
import { SeekBar, formatTime } from "@/player/components/SeekBar";
import { ChaptersMenu } from "@/player/components/ChaptersMenu";
import { CircleButton } from "@/player/components/CircleButton";
import { SpeedMenu } from "@/player/components/SpeedMenu";
import { SleepTimerMenu } from "@/player/components/SleepTimerMenu";
import type { AudiobookPlayback } from "./useAudiobookPlayback";

const SKIP_BACK_SECONDS = 30;
const SKIP_FORWARD_SECONDS = 30;
const PLAYBACK_RATES = [0.75, 1, 1.25, 1.5, 2] as const;

interface NowListeningProps {
  contentId: string;
  title: string;
  author?: string;
  narrator?: string;
  posterUrl: string;
  playback: AudiobookPlayback;
  onCollapse: () => void;
}

export function NowListening({
  contentId,
  title,
  author,
  narrator,
  posterUrl,
  playback,
  onCollapse,
}: NowListeningProps) {
  const [showRemaining, setShowRemaining] = useState(false);

  const rightTimeLabel = showRemaining
    ? `-${formatTime(Math.max(0, playback.duration - playback.currentTime))}`
    : formatTime(playback.duration);

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
        <button
          type="button"
          aria-label="More"
          className="text-muted-foreground hover:text-foreground rounded p-1.5"
        >
          <MoreHorizontal className="h-5 w-5" />
        </button>
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
              introRegion={null}
              creditsRegion={null}
              onSeek={playback.seekTo}
            />
            <div className="text-muted-foreground flex items-center justify-between text-xs tabular-nums">
              <span>{formatTime(playback.currentTime)}</span>
              <button
                type="button"
                data-testid="now-listening-right-time"
                onClick={() => setShowRemaining((v) => !v)}
                className="hover:text-foreground transition-colors"
              >
                {rightTimeLabel}
              </button>
            </div>
          </div>

          <div className="flex items-center justify-center gap-4">
            <CircleButton
              size="sm"
              variant="secondary"
              ariaLabel={`Back ${SKIP_BACK_SECONDS} seconds`}
              onClick={() => playback.skip(-SKIP_BACK_SECONDS)}
              disabled={!playback.hasFile}
            >
              <SkipIcon direction="back" seconds={SKIP_BACK_SECONDS} />
            </CircleButton>

            <CircleButton
              size="lg"
              variant="primary"
              ariaLabel={playback.playing ? "Pause" : "Play"}
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
              ariaLabel={`Forward ${SKIP_FORWARD_SECONDS} seconds`}
              onClick={() => playback.skip(SKIP_FORWARD_SECONDS)}
              disabled={!playback.hasFile}
            >
              <SkipIcon direction="forward" seconds={SKIP_FORWARD_SECONDS} />
            </CircleButton>
          </div>

          <div className="text-muted-foreground flex items-center justify-center gap-6 text-sm">
            <SleepTimerMenu
              setting={playback.sleep.setting}
              remainingMs={playback.sleep.remainingMs}
              onChange={playback.setSleep}
            />
            {playback.chapters.length > 0 && (
              <ChaptersMenu
                chapters={playback.chapters}
                currentTime={playback.currentTime}
                onSeek={playback.seekTo}
              />
            )}
            <SpeedMenu rates={PLAYBACK_RATES} value={playback.rate} onChange={playback.setRate} />
          </div>
        </div>
      </div>
    </div>
  );
}

function SkipIcon({ direction, seconds }: { direction: "back" | "forward"; seconds: number }) {
  const Arrow = direction === "back" ? RotateCcw : RotateCw;
  return (
    <span className="relative flex h-7 w-7 items-center justify-center">
      <Arrow className="h-7 w-7" strokeWidth={1.6} />
      <span className="absolute inset-0 flex items-center justify-center pb-[1px] text-[8.5px] font-semibold tracking-tight tabular-nums">
        {seconds}
      </span>
    </span>
  );
}
