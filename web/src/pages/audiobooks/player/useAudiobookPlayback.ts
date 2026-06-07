import { useEffect, useMemo, useRef, useState, useCallback, type RefObject } from "react";
import { buildDirectDownloadUrl } from "@/hooks/queries/downloads";
import { useReportMediaProgress } from "@/hooks/queries/progress";
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
  audioRef: RefObject<HTMLAudioElement | null>;
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

interface AudiobookPart {
  file: AudiobookFile;
  start: number;
  end: number;
}

function safeNumber(value: number): number {
  return Number.isFinite(value) && value >= 0 ? value : 0;
}

function buildParts(files: AudiobookFile[]): AudiobookPart[] {
  const parts: AudiobookPart[] = [];
  let offset = 0;
  for (const file of files) {
    const duration = safeNumber(file.duration_seconds ?? 0);
    parts.push({ file, start: offset, end: offset + duration });
    offset += duration;
  }
  return parts;
}

function totalDuration(parts: AudiobookPart[]): number {
  return parts.reduce((max, part) => Math.max(max, part.end), 0);
}

function clampedBookTime(seconds: number, duration: number): number {
  const value = safeNumber(seconds);
  if (duration <= 0) {
    return value;
  }
  return Math.max(0, Math.min(value, Math.max(0, duration - 1)));
}

function findPartIndex(parts: AudiobookPart[], seconds: number): number {
  if (parts.length === 0) {
    return -1;
  }
  const time = safeNumber(seconds);
  const index = parts.findIndex(
    (part) => time >= part.start && time < Math.max(part.end, part.start + 1),
  );
  if (index >= 0) {
    return index;
  }
  return time >= parts[parts.length - 1]!.end ? parts.length - 1 : 0;
}

function localTimeForPart(part: AudiobookPart | undefined, absoluteSeconds: number): number {
  if (!part) {
    return 0;
  }
  const duration = safeNumber(part.file.duration_seconds ?? 0);
  const local = safeNumber(absoluteSeconds) - part.start;
  if (duration <= 0) {
    return Math.max(0, local);
  }
  return Math.max(0, Math.min(local, Math.max(0, duration - 1)));
}

function absoluteBufferedRanges(
  ranges: TimeRanges,
  part: AudiobookPart | undefined,
  bookDuration: number,
): TimeRanges {
  if (!part) {
    return { length: 0, start: () => 0, end: () => 0 } as TimeRanges;
  }
  const out: Array<{ start: number; end: number }> = [];
  for (let i = 0; i < ranges.length; i++) {
    const start = Math.min(bookDuration, part.start + safeNumber(ranges.start(i)));
    const end = Math.min(bookDuration, part.start + safeNumber(ranges.end(i)));
    if (end > start) {
      out.push({ start, end });
    }
  }
  return {
    length: out.length,
    start(index: number) {
      const range = out[index];
      if (!range) throw new Error("TimeRanges index out of bounds");
      return range.start;
    },
    end(index: number) {
      const range = out[index];
      if (!range) throw new Error("TimeRanges index out of bounds");
      return range.end;
    },
  } as TimeRanges;
}

function buildPlayerChapters(files: AudiobookFile[]): PlayerChapter[] {
  const out: PlayerChapter[] = [];
  let offset = 0;
  let nextIndex = 0;
  for (const file of files) {
    for (const chapter of file.chapters ?? []) {
      const start = offset + chapter.start_seconds;
      const end = offset + (chapter.end_seconds || chapter.start_seconds);
      out.push({
        index: nextIndex++,
        title: chapter.title || `Chapter ${chapter.index + 1}`,
        start_seconds: start,
        end_seconds: end > start ? end : start + 1,
        source: chapter.source || "embedded",
      });
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
  const parts = useMemo(() => buildParts(files), [files]);
  const duration = useMemo(() => totalDuration(parts), [parts]);
  const chapters = useMemo(() => buildPlayerChapters(files), [files]);
  const [activeFileIndex, setActiveFileIndex] = useState(() =>
    findPartIndex(parts, initialPositionSeconds),
  );
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(() =>
    clampedBookTime(initialPositionSeconds, duration),
  );
  const [buffered, setBuffered] = useState<TimeRanges | null>(null);
  const [rate, setRateState] = useState(1);

  const { mutate: reportProgress } = useReportMediaProgress();
  const activePart = activeFileIndex >= 0 ? parts[activeFileIndex] : undefined;
  const fileId = activePart?.file.id;
  const streamUrl = fileId ? buildDirectDownloadUrl(fileId) : "";
  const currentTimeRef = useRef(currentTime);
  const reportRef = useRef<(pos: number) => void>(() => {});
  const pendingLocalSeekRef = useRef<number | null>(null);
  const playAfterSourceSwitchRef = useRef(false);
  const autoPlayPendingRef = useRef(autoPlay);

  const setAbsoluteTime = useCallback(
    (seconds: number) => {
      const next = clampedBookTime(seconds, duration);
      currentTimeRef.current = next;
      setCurrentTime(next);
    },
    [duration],
  );

  useEffect(() => {
    reportRef.current = (posSeconds: number) => {
      reportProgress({
        contentId,
        positionSeconds: Math.floor(safeNumber(posSeconds)),
        durationSeconds: Math.floor(safeNumber(duration)),
      });
    };
  }, [contentId, duration, reportProgress]);

  useEffect(() => {
    const target = clampedBookTime(initialPositionSeconds, duration);
    const index = findPartIndex(parts, target);
    pendingLocalSeekRef.current = localTimeForPart(parts[index], target);
    autoPlayPendingRef.current = autoPlay;
    setBuffered(null);
    setActiveFileIndex(index);
    currentTimeRef.current = target;
    setCurrentTime(target);
  }, [autoPlay, contentId, duration, initialPositionSeconds, parts]);

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio || !fileId || !activePart) return;

    const absoluteFromAudio = () => activePart.start + safeNumber(audio.currentTime);

    const onTimeUpdate = () => setAbsoluteTime(absoluteFromAudio());
    const onProgress = () =>
      setBuffered(absoluteBufferedRanges(audio.buffered, activePart, duration));
    const onDurationChange = () =>
      setBuffered(absoluteBufferedRanges(audio.buffered, activePart, duration));
    const onLoadedMetadata = () => {
      audio.playbackRate = rate;
      const pending = pendingLocalSeekRef.current;
      const local = pending ?? localTimeForPart(activePart, currentTimeRef.current);
      if (local > 0) {
        const max = Number.isFinite(audio.duration) ? Math.max(0, audio.duration - 1) : local;
        audio.currentTime = Math.min(local, max);
      }
      pendingLocalSeekRef.current = null;
      const shouldPlay = autoPlayPendingRef.current || playAfterSourceSwitchRef.current;
      autoPlayPendingRef.current = false;
      playAfterSourceSwitchRef.current = false;
      if (shouldPlay) {
        audio.play().catch((err) => {
          console.warn("audiobook autoplay blocked", err);
        });
      }
    };
    const onPlay = () => setPlaying(true);
    const onPause = () => {
      setPlaying(false);
      reportRef.current(absoluteFromAudio());
    };
    const onSeeked = () => {
      const absolute = absoluteFromAudio();
      setAbsoluteTime(absolute);
      reportRef.current(absolute);
    };
    const onEnded = () => {
      const nextIndex = activeFileIndex + 1;
      if (nextIndex < parts.length) {
        const nextPart = parts[nextIndex];
        if (nextPart) {
          pendingLocalSeekRef.current = 0;
          playAfterSourceSwitchRef.current = true;
          setBuffered(null);
          setAbsoluteTime(nextPart.start);
          setActiveFileIndex(nextIndex);
          return;
        }
      }
      currentTimeRef.current = duration;
      setCurrentTime(duration);
      setPlaying(false);
      reportRef.current(duration);
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
  }, [activeFileIndex, activePart, duration, fileId, parts, rate, setAbsoluteTime]);

  useEffect(() => {
    if (!playing) return;
    const id = window.setInterval(() => {
      reportRef.current(currentTimeRef.current);
    }, REPORT_INTERVAL_MS);
    return () => window.clearInterval(id);
  }, [playing]);

  useEffect(() => {
    const audio = audioRef.current;
    return () => {
      if (audio && !audio.paused) {
        audio.pause();
        reportRef.current(currentTimeRef.current);
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

  const seekTo = useCallback(
    (seconds: number) => {
      const target = clampedBookTime(seconds, duration);
      const nextIndex = findPartIndex(parts, target);
      const nextPart = parts[nextIndex];
      const audio = audioRef.current;
      const shouldContinuePlaying = audio ? !audio.paused : playing;
      const local = localTimeForPart(nextPart, target);

      if (nextIndex !== activeFileIndex) {
        pendingLocalSeekRef.current = local;
        playAfterSourceSwitchRef.current = shouldContinuePlaying;
        setBuffered(null);
        setActiveFileIndex(nextIndex);
      } else if (audio) {
        audio.currentTime = local;
      }

      currentTimeRef.current = target;
      setCurrentTime(target);
      reportRef.current(target);
    },
    [activeFileIndex, duration, parts, playing],
  );

  const skip = useCallback(
    (delta: number) => {
      seekTo(currentTimeRef.current + delta);
    },
    [seekTo],
  );

  const setRate = useCallback((nextRate: number) => {
    setRateState(nextRate);
    if (audioRef.current) audioRef.current.playbackRate = nextRate;
  }, []);

  const currentChapter = useMemo(() => {
    if (chapters.length === 0) return null;
    for (let i = chapters.length - 1; i >= 0; i--) {
      const chapter = chapters[i];
      if (chapter && currentTime >= chapter.start_seconds) {
        return chapter;
      }
    }
    return chapters[0] ?? null;
  }, [chapters, currentTime]);

  const [sleepSetting, setSleepSetting] = useState<SleepSetting>({ kind: "off" });
  const [sleepTargetMs, setSleepTargetMs] = useState<number | null>(null);
  const [sleepChapterEndSeconds, setSleepChapterEndSeconds] = useState<number | null>(null);
  const [sleepNowMs, setSleepNowMs] = useState<number>(() => Date.now());

  useEffect(() => {
    if (sleepSetting.kind !== "duration") {
      setSleepTargetMs(null);
      return;
    }
    setSleepChapterEndSeconds(null);
    setSleepTargetMs(Date.now() + sleepSetting.seconds * 1000);
  }, [sleepSetting]);

  useEffect(() => {
    if (sleepSetting.kind !== "end-of-chapter") {
      setSleepChapterEndSeconds(null);
      return;
    }
    setSleepTargetMs(null);
    setSleepChapterEndSeconds((current) => {
      if (current != null && current > currentTime) return current;
      return currentChapter?.end_seconds ?? null;
    });
  }, [currentChapter, currentTime, sleepSetting]);

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
    if (sleepSetting.kind !== "end-of-chapter" || sleepChapterEndSeconds == null) return;
    if (currentTime < sleepChapterEndSeconds) return;
    const audio = audioRef.current;
    if (audio && !audio.paused) audio.pause();
    setSleepSetting({ kind: "off" });
    setSleepChapterEndSeconds(null);
  }, [sleepSetting, sleepChapterEndSeconds, currentTime]);

  const setSleep = useCallback((next: SleepSetting) => setSleepSetting(next), []);
  const sleepRemainingMs = sleepTargetMs == null ? null : Math.max(0, sleepTargetMs - sleepNowMs);

  return {
    audioRef,
    streamUrl,
    hasFile: Boolean(activePart),
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
