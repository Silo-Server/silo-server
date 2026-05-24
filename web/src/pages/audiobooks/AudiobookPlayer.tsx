import { useEffect, useRef, useState } from "react";
import { X, Pause, Play, RotateCcw, RotateCw } from "lucide-react";
import { buildDirectDownloadUrl } from "@/hooks/queries/downloads";
import { useReportAudiobookProgress } from "@/hooks/audiobooks/useReportAudiobookProgress";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { SeekBar, formatTime } from "@/player/components/SeekBar";
import { ChaptersMenu } from "@/player/components/ChaptersMenu";
import { SpeedMenu } from "@/player/components/SpeedMenu";
import type { PlayerChapter } from "@/player/types";

export interface AudiobookPlayerProps {
  contentId: string;
  title?: string;
  files: AudiobookFile[];
  initialPositionSeconds?: number;
  autoPlay?: boolean;
  onClose?: () => void;
}

const SKIP_BACK_SECONDS = 30;
const SKIP_FORWARD_SECONDS = 30;
const PLAYBACK_RATES = [0.75, 1, 1.25, 1.5, 2] as const;
const REPORT_INTERVAL_MS = 10_000;

function safeNumber(value: number): number {
  return Number.isFinite(value) && value >= 0 ? value : 0;
}

/**
 * Build a flat PlayerChapter list across all files, adjusting per-file
 * chapter offsets by cumulative file durations. For single-file
 * audiobooks this is identical to file.chapters. For multi-file
 * audiobooks (which we don't queue across files yet, but display anyway)
 * the chapters reflect their absolute position in the whole work.
 */
function buildPlayerChapters(files: AudiobookFile[]): PlayerChapter[] {
  const out: PlayerChapter[] = [];
  let offset = 0;
  let nextIndex = 0;
  for (const file of files) {
    if (file.chapters) {
      for (const ch of file.chapters) {
        out.push({
          index: nextIndex++,
          title: ch.title || `Chapter ${ch.index + 1}`,
          start_seconds: offset + ch.start_seconds,
          end_seconds: offset + (ch.end_seconds || ch.start_seconds),
          source: ch.source || "embedded",
        });
      }
    }
    offset += file.duration_seconds ?? 0;
  }
  return out;
}

export default function AudiobookPlayer({
  contentId,
  title,
  files,
  initialPositionSeconds = 0,
  autoPlay = true,
  onClose,
}: AudiobookPlayerProps) {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [buffered, setBuffered] = useState<TimeRanges | null>(null);
  const [rate, setRate] = useState(1);

  const reportProgress = useReportAudiobookProgress();
  const file = files[0];
  const fileId = file?.id;

  const reportRef = useRef<(pos: number) => void>(() => {});
  reportRef.current = (posSeconds: number) => {
    if (!fileId) return;
    reportProgress.mutate({
      contentId,
      positionSeconds: Math.floor(posSeconds),
      mediaFileId: fileId,
    });
  };

  const streamUrl = fileId ? buildDirectDownloadUrl(fileId) : "";
  const chapters = buildPlayerChapters(files);

  // Wire all audio element events once per file change.
  useEffect(() => {
    const audio = audioRef.current;
    if (!audio || !fileId) return;

    const handleTimeUpdate = () => setCurrentTime(safeNumber(audio.currentTime));
    const handleProgress = () => setBuffered(audio.buffered);
    const handleDurationChange = () => setDuration(safeNumber(audio.duration));
    const handleLoadedMetadata = () => {
      setDuration(safeNumber(audio.duration));
      if (initialPositionSeconds > 0 && Number.isFinite(audio.duration)) {
        const target = Math.min(initialPositionSeconds, audio.duration - 1);
        if (target > 0) audio.currentTime = target;
      }
      if (autoPlay) {
        audio.play().catch((err) => {
          console.warn("audiobook autoplay blocked", err);
        });
      }
    };
    const handlePlay = () => setPlaying(true);
    const handlePause = () => {
      setPlaying(false);
      reportRef.current(audio.currentTime);
    };
    const handleSeeked = () => {
      setCurrentTime(safeNumber(audio.currentTime));
      reportRef.current(audio.currentTime);
    };
    const handleEnded = () => {
      setPlaying(false);
      reportRef.current(audio.currentTime);
    };
    const handleError = () => {
      const err = audio.error;
      console.error("audiobook audio error", {
        code: err?.code,
        message: err?.message,
        networkState: audio.networkState,
        readyState: audio.readyState,
        src: audio.currentSrc,
      });
    };

    audio.addEventListener("timeupdate", handleTimeUpdate);
    audio.addEventListener("progress", handleProgress);
    audio.addEventListener("durationchange", handleDurationChange);
    audio.addEventListener("loadedmetadata", handleLoadedMetadata);
    audio.addEventListener("play", handlePlay);
    audio.addEventListener("pause", handlePause);
    audio.addEventListener("seeked", handleSeeked);
    audio.addEventListener("ended", handleEnded);
    audio.addEventListener("error", handleError);

    return () => {
      audio.removeEventListener("timeupdate", handleTimeUpdate);
      audio.removeEventListener("progress", handleProgress);
      audio.removeEventListener("durationchange", handleDurationChange);
      audio.removeEventListener("loadedmetadata", handleLoadedMetadata);
      audio.removeEventListener("play", handlePlay);
      audio.removeEventListener("pause", handlePause);
      audio.removeEventListener("seeked", handleSeeked);
      audio.removeEventListener("ended", handleEnded);
      audio.removeEventListener("error", handleError);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fileId]);

  // Periodic progress reporting while playing.
  useEffect(() => {
    if (!playing) return;
    const id = window.setInterval(() => {
      const audio = audioRef.current;
      if (audio) reportRef.current(audio.currentTime);
    }, REPORT_INTERVAL_MS);
    return () => window.clearInterval(id);
  }, [playing]);

  // Pause and report on unmount.
  useEffect(() => {
    return () => {
      const audio = audioRef.current;
      if (audio && !audio.paused) {
        audio.pause();
        reportRef.current(audio.currentTime);
      }
    };
  }, []);

  function togglePlay() {
    const audio = audioRef.current;
    if (!audio) return;
    if (audio.paused) {
      audio.play().catch((err) => {
        console.error("audiobook play failed", err);
      });
    } else {
      audio.pause();
    }
  }

  function seekTo(seconds: number) {
    const audio = audioRef.current;
    if (!audio) return;
    const clamped = Math.max(0, Math.min(seconds, audio.duration || seconds));
    audio.currentTime = clamped;
    setCurrentTime(safeNumber(clamped));
  }

  function skip(delta: number) {
    const audio = audioRef.current;
    if (!audio) return;
    seekTo(audio.currentTime + delta);
  }

  const canPlay = Boolean(file);

  return (
    <div className="bg-background border-b px-3 pt-2 pb-2 sm:px-6">
      {file && (
        <audio ref={audioRef} src={streamUrl} preload="metadata" style={{ display: "none" }} />
      )}

      {/* SeekBar — full width across the top of the bar, mirroring the video
          player's bottom-HUD pattern (chapter markers, hover preview). */}
      <SeekBar
        currentTime={currentTime}
        duration={duration}
        buffered={buffered}
        chapters={chapters}
        introRegion={null}
        creditsRegion={null}
        onSeek={seekTo}
      />

      {/* Three-column grid: title/time left, playback cluster center,
          utility rail right. Matches PlayerControls layout. */}
      <div className="mt-1 grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-3 sm:gap-5">
        {/* Left: title + time */}
        <div className="flex min-w-0 flex-col gap-0.5">
          {title ? (
            <div
              className="truncate text-[14px] leading-tight font-semibold tracking-tight sm:text-[15px]"
              title={title}
            >
              {title}
            </div>
          ) : null}
          <div className="text-muted-foreground flex items-center gap-2 text-[10px] leading-tight uppercase">
            <span className="font-mono text-[11px] tracking-[0.12em] normal-case tabular-nums">
              {formatTime(currentTime)}
              <span className="mx-1 opacity-50">/</span>
              {formatTime(duration)}
            </span>
          </div>
        </div>

        {/* Center: skip-back / play/pause / skip-forward */}
        <div className="flex items-center justify-center gap-2 sm:gap-3">
          <CircleButton
            ariaLabel={`Back ${SKIP_BACK_SECONDS} seconds`}
            onClick={() => skip(-SKIP_BACK_SECONDS)}
            disabled={!canPlay}
          >
            <SkipIcon direction="back" seconds={SKIP_BACK_SECONDS} />
          </CircleButton>

          <CircleButton
            ariaLabel={playing ? "Pause" : "Play"}
            onClick={togglePlay}
            disabled={!canPlay}
            variant="primary"
          >
            {playing ? (
              <Pause className="h-5 w-5" strokeWidth={0} fill="currentColor" />
            ) : (
              <Play className="ml-[2px] h-5 w-5" strokeWidth={0} fill="currentColor" />
            )}
          </CircleButton>

          <CircleButton
            ariaLabel={`Forward ${SKIP_FORWARD_SECONDS} seconds`}
            onClick={() => skip(SKIP_FORWARD_SECONDS)}
            disabled={!canPlay}
          >
            <SkipIcon direction="forward" seconds={SKIP_FORWARD_SECONDS} />
          </CircleButton>
        </div>

        {/* Right: chapters + speed + close */}
        <div className="flex items-center justify-end gap-2">
          {chapters.length > 0 && (
            <ChaptersMenu chapters={chapters} currentTime={currentTime} onSeek={seekTo} />
          )}
          <SpeedMenu
            rates={PLAYBACK_RATES}
            value={rate}
            onChange={(r) => {
              setRate(r);
              if (audioRef.current) audioRef.current.playbackRate = r;
            }}
          />
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

interface CircleButtonProps {
  ariaLabel: string;
  onClick: () => void;
  disabled?: boolean;
  variant?: "primary" | "secondary";
  children: React.ReactNode;
}

function CircleButton({
  ariaLabel,
  onClick,
  disabled,
  variant = "secondary",
  children,
}: CircleButtonProps) {
  const base =
    "flex items-center justify-center rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/75 disabled:opacity-40 disabled:cursor-not-allowed";
  const sizing = variant === "primary" ? "h-10 w-10" : "h-9 w-9";
  const skin =
    variant === "primary"
      ? "bg-primary text-primary-foreground hover:opacity-90"
      : "bg-muted text-foreground hover:bg-muted/80";
  return (
    <button
      type="button"
      aria-label={ariaLabel}
      onClick={onClick}
      disabled={disabled}
      className={`${base} ${sizing} ${skin}`}
    >
      {children}
    </button>
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
