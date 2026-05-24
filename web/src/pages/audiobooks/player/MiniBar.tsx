import { X, Pause, Play, RotateCcw, RotateCw } from "lucide-react";
import { SeekBar, formatTime } from "@/player/components/SeekBar";
import { ChaptersMenu } from "@/player/components/ChaptersMenu";
import { CircleButton } from "@/player/components/CircleButton";
import { SpeedMenu } from "@/player/components/SpeedMenu";
import type { AudiobookPlayback } from "./useAudiobookPlayback";

const SKIP_BACK_SECONDS = 30;
const SKIP_FORWARD_SECONDS = 30;
const PLAYBACK_RATES = [0.75, 1, 1.25, 1.5, 2] as const;

interface MiniBarProps {
  title?: string;
  playback: AudiobookPlayback;
  onClose?: () => void;
}

export function MiniBar({ title, playback, onClose }: MiniBarProps) {
  return (
    <div className="bg-background border-b px-3 pt-2 pb-2 sm:px-6">
      <SeekBar
        currentTime={playback.currentTime}
        duration={playback.duration}
        buffered={playback.buffered}
        chapters={playback.chapters}
        introRegion={null}
        creditsRegion={null}
        onSeek={playback.seekTo}
      />

      <div className="mt-1 grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-3 sm:gap-5">
        <div className="flex min-w-0 flex-col gap-0.5">
          {title ? (
            <div
              className="truncate text-[14px] leading-tight font-semibold tracking-tight sm:text-[15px]"
              title={title}
            >
              {title}
            </div>
          ) : null}
          {playback.currentChapter ? (
            <div
              data-testid="minibar-chapter-title"
              className="text-muted-foreground truncate text-[11px] leading-tight"
              title={playback.currentChapter.title}
            >
              {playback.currentChapter.title}
            </div>
          ) : null}
          <div className="text-muted-foreground flex items-center gap-2 text-[10px] leading-tight uppercase">
            <span className="font-mono text-[11px] tracking-[0.12em] normal-case tabular-nums">
              {formatTime(playback.currentTime)}
              <span className="mx-1 opacity-50">/</span>
              {formatTime(playback.duration)}
            </span>
          </div>
        </div>

        <div className="flex items-center justify-center gap-2 sm:gap-3">
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
            size="md"
            variant="primary"
            ariaLabel={playback.playing ? "Pause" : "Play"}
            onClick={playback.togglePlay}
            disabled={!playback.hasFile}
            data-paused={!playback.playing}
          >
            {playback.playing ? (
              <Pause className="h-5 w-5" strokeWidth={0} fill="currentColor" />
            ) : (
              <Play className="ml-[2px] h-5 w-5" strokeWidth={0} fill="currentColor" />
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

        <div className="flex items-center justify-end gap-2">
          {playback.chapters.length > 0 && (
            <ChaptersMenu
              chapters={playback.chapters}
              currentTime={playback.currentTime}
              onSeek={playback.seekTo}
            />
          )}
          <SpeedMenu rates={PLAYBACK_RATES} value={playback.rate} onChange={playback.setRate} />
          {onClose && (
            <button
              type="button"
              onClick={onClose}
              aria-label="Close player"
              className="text-muted-foreground hover:text-foreground rounded p-1.5"
            >
              <X className="h-4 w-4" />
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function SkipIcon({ direction, seconds }: { direction: "back" | "forward"; seconds: number }) {
  const Arrow = direction === "back" ? RotateCcw : RotateCw;
  return (
    <span className="relative flex h-6 w-6 items-center justify-center">
      <Arrow className="h-6 w-6" strokeWidth={1.6} />
      <span className="absolute inset-0 flex items-center justify-center pb-[1px] text-[8px] font-semibold tracking-tight tabular-nums">
        {seconds}
      </span>
    </span>
  );
}
