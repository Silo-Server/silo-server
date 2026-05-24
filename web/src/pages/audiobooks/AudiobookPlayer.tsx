import { useEffect, useRef, useState, useCallback } from "react";
import { buildDirectDownloadUrl } from "@/hooks/queries/downloads";
import { useReportAudiobookProgress } from "@/hooks/audiobooks/useReportAudiobookProgress";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { Button } from "@/components/ui/button";
import { X, Play, Pause, RotateCcw, RotateCw } from "lucide-react";

export interface AudiobookPlayerProps {
  contentId: string;
  files: AudiobookFile[];
  initialPositionSeconds?: number;
  onClose?: () => void;
}

function formatTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "0:00";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (h > 0) {
    return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
  }
  return `${m}:${String(s).padStart(2, "0")}`;
}

const PLAYBACK_RATES = [0.75, 1, 1.25, 1.5, 2] as const;
const REPORT_INTERVAL_MS = 10_000;

export default function AudiobookPlayer({
  contentId,
  files,
  initialPositionSeconds = 0,
  onClose,
}: AudiobookPlayerProps) {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(initialPositionSeconds);
  const [duration, setDuration] = useState(0);
  const [rate, setRate] = useState(1);
  const reportProgress = useReportAudiobookProgress();
  const lastReportedRef = useRef<number>(initialPositionSeconds);
  const reportTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const file = files[0];
  const isMultiFile = files.length > 1;

  // Build the stream URL once so it doesn't change on re-render.
  const streamUrl = file ? buildDirectDownloadUrl(file.id) : "";

  // Report helper — fire-and-forget, won't throw.
  const report = useCallback(
    (posSeconds: number) => {
      if (!file) return;
      lastReportedRef.current = posSeconds;
      reportProgress.mutate({
        contentId,
        positionSeconds: Math.floor(posSeconds),
        mediaFileId: file.id,
      });
    },
    [contentId, file, reportProgress],
  );

  // Set initial position once audio is ready.
  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;
    const handle = () => {
      if (initialPositionSeconds > 0 && audio.duration > 0) {
        audio.currentTime = Math.min(initialPositionSeconds, audio.duration - 1);
      }
    };
    audio.addEventListener("loadedmetadata", handle);
    return () => audio.removeEventListener("loadedmetadata", handle);
  }, [initialPositionSeconds]);

  // Wire audio element events.
  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;

    const onTimeUpdate = () => setCurrentTime(audio.currentTime);
    const onDurationChange = () => setDuration(audio.duration ?? 0);
    const onPlay = () => setPlaying(true);
    const onPause = () => {
      setPlaying(false);
      report(audio.currentTime);
    };
    const onSeeked = () => report(audio.currentTime);
    const onEnded = () => {
      setPlaying(false);
      report(audio.currentTime);
    };

    audio.addEventListener("timeupdate", onTimeUpdate);
    audio.addEventListener("durationchange", onDurationChange);
    audio.addEventListener("play", onPlay);
    audio.addEventListener("pause", onPause);
    audio.addEventListener("seeked", onSeeked);
    audio.addEventListener("ended", onEnded);

    return () => {
      audio.removeEventListener("timeupdate", onTimeUpdate);
      audio.removeEventListener("durationchange", onDurationChange);
      audio.removeEventListener("play", onPlay);
      audio.removeEventListener("pause", onPause);
      audio.removeEventListener("seeked", onSeeked);
      audio.removeEventListener("ended", onEnded);
    };
  }, [report]);

  // Periodic progress reporting while playing.
  useEffect(() => {
    if (playing) {
      reportTimerRef.current = setInterval(() => {
        const audio = audioRef.current;
        if (audio) report(audio.currentTime);
      }, REPORT_INTERVAL_MS);
    } else {
      if (reportTimerRef.current) {
        clearInterval(reportTimerRef.current);
        reportTimerRef.current = null;
      }
    }
    return () => {
      if (reportTimerRef.current) {
        clearInterval(reportTimerRef.current);
        reportTimerRef.current = null;
      }
    };
  }, [playing, report]);

  // Report and pause on unmount.
  useEffect(() => {
    const audio = audioRef.current;
    return () => {
      if (audio && !audio.paused) {
        audio.pause();
        report(audio.currentTime);
      }
    };
  }, [report]);

  function togglePlay() {
    const audio = audioRef.current;
    if (!audio) return;
    if (audio.paused) {
      void audio.play();
    } else {
      audio.pause();
    }
  }

  function skip(delta: number) {
    const audio = audioRef.current;
    if (!audio) return;
    audio.currentTime = Math.max(0, Math.min(audio.currentTime + delta, audio.duration ?? 0));
  }

  function handleRateChange(e: React.ChangeEvent<HTMLSelectElement>) {
    const r = Number(e.target.value);
    setRate(r);
    if (audioRef.current) audioRef.current.playbackRate = r;
  }

  function handleSeek(e: React.ChangeEvent<HTMLInputElement>) {
    const val = Number(e.target.value);
    setCurrentTime(val);
    if (audioRef.current) audioRef.current.currentTime = val;
  }

  const progress = duration > 0 ? (currentTime / duration) * 100 : 0;

  return (
    <div className="flex items-center gap-3 px-4 py-3">
      {/* Hidden audio element */}
      {file && (
        <audio ref={audioRef} src={streamUrl} preload="metadata" style={{ display: "none" }} />
      )}

      {/* Close */}
      {onClose && (
        <Button
          variant="ghost"
          size="icon"
          onClick={onClose}
          aria-label="Close player"
          className="h-8 w-8 shrink-0"
        >
          <X className="h-4 w-4" />
        </Button>
      )}

      {/* Multi-file notice */}
      {isMultiFile && (
        <span className="text-muted-foreground text-xs">
          Multi-file playback coming soon — playing first file only.
        </span>
      )}

      {/* Controls */}
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        {/* Top row: skip-back, play/pause, skip-forward */}
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="icon"
            onClick={() => skip(-30)}
            aria-label="Skip back 30 seconds"
            className="h-8 w-8"
            disabled={!file}
          >
            <RotateCcw className="h-4 w-4" />
          </Button>

          <Button
            variant="default"
            size="icon"
            onClick={togglePlay}
            aria-label={playing ? "Pause" : "Play"}
            className="h-9 w-9 shrink-0"
            disabled={!file}
          >
            {playing ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4" />}
          </Button>

          <Button
            variant="ghost"
            size="icon"
            onClick={() => skip(30)}
            aria-label="Skip forward 30 seconds"
            className="h-8 w-8"
            disabled={!file}
          >
            <RotateCw className="h-4 w-4" />
          </Button>

          {/* Time */}
          <span className="text-muted-foreground shrink-0 font-mono text-xs tabular-nums">
            {formatTime(currentTime)}
            {" / "}
            {formatTime(duration)}
          </span>

          {/* Playback rate */}
          <select
            value={rate}
            onChange={handleRateChange}
            aria-label="Playback speed"
            className="bg-background text-muted-foreground hover:text-foreground focus:ring-ring ml-auto shrink-0 rounded border-0 px-1.5 py-1 text-xs outline-none focus:ring-1"
            disabled={!file}
          >
            {PLAYBACK_RATES.map((r) => (
              <option key={r} value={r}>
                {r}×
              </option>
            ))}
          </select>
        </div>

        {/* Seek bar */}
        <input
          type="range"
          min={0}
          max={duration > 0 ? duration : 100}
          step={1}
          value={currentTime}
          onChange={handleSeek}
          aria-label="Seek"
          disabled={!file || duration === 0}
          className="accent-primary h-1 w-full cursor-pointer"
          style={{
            background: `linear-gradient(to right, hsl(var(--primary)) ${progress}%, hsl(var(--muted)) ${progress}%)`,
          }}
        />
      </div>
    </div>
  );
}
