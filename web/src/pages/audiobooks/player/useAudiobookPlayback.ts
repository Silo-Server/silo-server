import { useEffect, useMemo, useRef, useState, useCallback } from "react";
import { buildDirectDownloadUrl } from "@/hooks/queries/downloads";
import { useReportAudiobookProgress } from "@/hooks/audiobooks/useReportAudiobookProgress";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import type { PlayerChapter } from "@/player/types";
import type { SleepSetting } from "@/player/components/SleepTimerMenu";

const REPORT_INTERVAL_MS = 10_000;

export interface UseAudiobookPlaybackOptions {
  contentId: string;
  files: AudiobookFile[];
  initialPositionSeconds: number;
  autoPlay?: boolean;
}

export interface AudiobookPlayback {
  audioRef: React.RefObject<HTMLAudioElement | null>;
  streamUrl: string;
  hasFile: boolean;
  playing: boolean;
  currentTime: number;
  duration: number;
  buffered: TimeRanges | null;
  rate: number;
  chapters: PlayerChapter[];
  currentChapter: PlayerChapter | null;
  togglePlay: () => void;
  seekTo: (seconds: number) => void;
  skip: (delta: number) => void;
  setRate: (r: number) => void;
  sleep: { setting: SleepSetting; remainingMs: number | null };
  setSleep: (next: SleepSetting) => void;
}

function safeNumber(value: number): number {
  return Number.isFinite(value) && value >= 0 ? value : 0;
}

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

export function useAudiobookPlayback({
  contentId,
  files,
  initialPositionSeconds,
  autoPlay = true,
}: UseAudiobookPlaybackOptions): AudiobookPlayback {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [buffered, setBuffered] = useState<TimeRanges | null>(null);
  const [rate, setRateState] = useState(1);

  const reportProgress = useReportAudiobookProgress();
  const file = files[0];
  const fileId = file?.id;
  const streamUrl = fileId ? buildDirectDownloadUrl(fileId) : "";
  const chapters = useMemo(() => buildPlayerChapters(files), [files]);

  const reportRef = useRef<(pos: number) => void>(() => {});
  reportRef.current = (posSeconds: number) => {
    if (!fileId) return;
    reportProgress.mutate({
      contentId,
      positionSeconds: Math.floor(posSeconds),
      mediaFileId: fileId,
    });
  };

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio || !fileId) return;

    const onTimeUpdate = () => setCurrentTime(safeNumber(audio.currentTime));
    const onProgress = () => setBuffered(audio.buffered);
    const onDurationChange = () => setDuration(safeNumber(audio.duration));
    const onLoadedMetadata = () => {
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
    const onPlay = () => setPlaying(true);
    const onPause = () => {
      setPlaying(false);
      reportRef.current(audio.currentTime);
    };
    const onSeeked = () => {
      setCurrentTime(safeNumber(audio.currentTime));
      reportRef.current(audio.currentTime);
    };
    const onEnded = () => {
      setPlaying(false);
      reportRef.current(audio.currentTime);
    };
    const onError = () => {
      const err = audio.error;
      console.error("audiobook audio error", {
        code: err?.code,
        message: err?.message,
        networkState: audio.networkState,
        readyState: audio.readyState,
        src: audio.currentSrc,
      });
    };

    audio.addEventListener("timeupdate", onTimeUpdate);
    audio.addEventListener("progress", onProgress);
    audio.addEventListener("durationchange", onDurationChange);
    audio.addEventListener("loadedmetadata", onLoadedMetadata);
    audio.addEventListener("play", onPlay);
    audio.addEventListener("pause", onPause);
    audio.addEventListener("seeked", onSeeked);
    audio.addEventListener("ended", onEnded);
    audio.addEventListener("error", onError);

    return () => {
      audio.removeEventListener("timeupdate", onTimeUpdate);
      audio.removeEventListener("progress", onProgress);
      audio.removeEventListener("durationchange", onDurationChange);
      audio.removeEventListener("loadedmetadata", onLoadedMetadata);
      audio.removeEventListener("play", onPlay);
      audio.removeEventListener("pause", onPause);
      audio.removeEventListener("seeked", onSeeked);
      audio.removeEventListener("ended", onEnded);
      audio.removeEventListener("error", onError);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fileId]);

  useEffect(() => {
    if (!playing) return;
    const id = window.setInterval(() => {
      const audio = audioRef.current;
      if (audio) reportRef.current(audio.currentTime);
    }, REPORT_INTERVAL_MS);
    return () => window.clearInterval(id);
  }, [playing]);

  useEffect(() => {
    return () => {
      const audio = audioRef.current;
      if (audio && !audio.paused) {
        audio.pause();
        reportRef.current(audio.currentTime);
      }
    };
  }, []);

  const togglePlay = useCallback(() => {
    const audio = audioRef.current;
    if (!audio) return;
    if (audio.paused) {
      audio.play().catch((err) => console.error("audiobook play failed", err));
    } else {
      audio.pause();
    }
  }, []);

  const seekTo = useCallback((seconds: number) => {
    const audio = audioRef.current;
    if (!audio) return;
    const max = Number.isFinite(audio.duration) ? audio.duration - 1 : seconds;
    const clamped = Math.max(0, Math.min(seconds, max));
    audio.currentTime = clamped;
    setCurrentTime(safeNumber(clamped));
  }, []);

  const skip = useCallback(
    (delta: number) => {
      const audio = audioRef.current;
      if (!audio) return;
      seekTo(audio.currentTime + delta);
    },
    [seekTo],
  );

  const setRate = useCallback((r: number) => {
    setRateState(r);
    if (audioRef.current) audioRef.current.playbackRate = r;
  }, []);

  const currentChapter = useMemo(() => {
    if (chapters.length === 0) return null;
    return (
      chapters.find((c) => currentTime >= c.start_seconds && currentTime < c.end_seconds) ?? null
    );
  }, [chapters, currentTime]);

  const [sleepSetting, setSleepSetting] = useState<SleepSetting>({ kind: "off" });
  const [sleepTargetMs, setSleepTargetMs] = useState<number | null>(null);
  const [sleepNowMs, setSleepNowMs] = useState<number>(() => Date.now());

  useEffect(() => {
    if (sleepSetting.kind !== "duration") {
      setSleepTargetMs(null);
      return;
    }
    setSleepTargetMs(Date.now() + sleepSetting.seconds * 1000);
  }, [sleepSetting]);

  useEffect(() => {
    if (sleepTargetMs == null) return;
    const id = window.setInterval(() => setSleepNowMs(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [sleepTargetMs]);

  useEffect(() => {
    if (sleepTargetMs == null) return;
    if (sleepNowMs < sleepTargetMs) return;
    const audio = audioRef.current;
    if (audio && !audio.paused) audio.pause();
    setSleepSetting({ kind: "off" });
    setSleepTargetMs(null);
  }, [sleepNowMs, sleepTargetMs]);

  useEffect(() => {
    if (sleepSetting.kind !== "end-of-chapter" || !currentChapter) return;
    if (currentTime < currentChapter.end_seconds) return;
    const audio = audioRef.current;
    if (audio && !audio.paused) audio.pause();
    setSleepSetting({ kind: "off" });
  }, [sleepSetting, currentChapter, currentTime]);

  const setSleep = useCallback((next: SleepSetting) => setSleepSetting(next), []);
  const sleepRemainingMs = sleepTargetMs == null ? null : Math.max(0, sleepTargetMs - sleepNowMs);

  return {
    audioRef,
    streamUrl,
    hasFile: Boolean(file),
    playing,
    currentTime,
    duration,
    buffered,
    rate,
    chapters,
    currentChapter,
    togglePlay,
    seekTo,
    skip,
    setRate,
    sleep: { setting: sleepSetting, remainingMs: sleepRemainingMs },
    setSleep,
  };
}
