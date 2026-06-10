import { useEffect, useMemo, useRef, useState, useCallback, type RefObject } from "react";
import { toast } from "sonner";
import { useReportMediaProgress } from "@/hooks/queries/progress";
import { buildPlayerChapters, nextChapterStart, prevChapterStart } from "@/lib/audiobooks/chapters";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { getPersistedVolume, persistVolume } from "@/player/components/VolumeControl";
import { usePlayerConfig } from "@/player/context/PlayerConfigContext";
import { playerFetch } from "@/player/player-fetch";
import type { PlaybackRealtimeCommandEnvelope } from "@/player/realtime-protocol";
import { buildPlayerStreamUrl } from "@/player/stream-url";
import type { PlaybackSessionResponse, PlayerChapter } from "@/player/types";
import { usePlaybackRealtime } from "@/player/hooks/usePlaybackRealtime";
import type { SleepSetting } from "@/player/components/SleepTimerMenu";
import { smartRewindSeconds } from "./smartRewind";
import { clampAudiobookRate, getBookRate, setBookRate } from "./useAudiobookPrefs";

const REPORT_INTERVAL_MS = 10_000;

export interface UseAudiobookPlaybackOptions {
  contentId: string;
  files: AudiobookFile[];
  initialPositionSeconds: number;
  autoPlay?: boolean;
  smartRewindEnabled?: boolean;
  onStopRequested?: () => void;
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
  volume: number;
  muted: boolean;
  togglePlay: () => void;
  seekTo: (seconds: number) => void;
  skip: (delta: number) => void;
  setRate: (r: number) => void;
  setVolume: (volume: number) => void;
  setMuted: (muted: boolean) => void;
  nextChapter: () => void;
  prevChapter: () => void;
  sleep: { setting: SleepSetting; remainingMs: number | null };
  setSleep: (next: SleepSetting) => void;
}

interface AudiobookSessionState {
  sessionId: string | null;
  streamUrl: string;
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

function readNumericPayload(
  payload: Record<string, unknown> | undefined,
  ...keys: string[]
): number | null {
  if (!payload) {
    return null;
  }
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === "number" && Number.isFinite(value)) {
      return value;
    }
  }
  return null;
}

function readStringPayload(
  payload: Record<string, unknown> | undefined,
  key: string,
): string | null {
  const value = payload?.[key];
  return typeof value === "string" && value.trim().length > 0 ? value.trim() : null;
}

export function useAudiobookPlayback({
  contentId,
  files,
  initialPositionSeconds,
  autoPlay = true,
  smartRewindEnabled = true,
  onStopRequested,
}: UseAudiobookPlaybackOptions): AudiobookPlayback {
  const config = usePlayerConfig();
  const audioRef = useRef<HTMLAudioElement>(null);
  const parts = useMemo(() => buildParts(files), [files]);
  const duration = useMemo(() => totalDuration(parts), [parts]);
  const chapters = useMemo(() => buildPlayerChapters(files), [files]);
  const [activeFileIndex, setActiveFileIndex] = useState(() => {
    return findPartIndex(parts, initialPositionSeconds);
  });
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(() =>
    clampedBookTime(initialPositionSeconds, duration),
  );
  const [buffered, setBuffered] = useState<TimeRanges | null>(null);
  const [rate, setRateState] = useState(() => getBookRate(contentId) ?? 1);
  const [volume, setVolumeState] = useState(() => getPersistedVolume().volume);
  const [muted, setMutedState] = useState(() => getPersistedVolume().muted);
  const [sessionState, setSessionState] = useState<AudiobookSessionState>({
    sessionId: null,
    streamUrl: "",
  });

  const { mutate: reportProgress } = useReportMediaProgress();
  const activePart = activeFileIndex >= 0 ? parts[activeFileIndex] : undefined;
  const fileId = activePart?.file.id;
  const currentTimeRef = useRef(currentTime);
  const activePartRef = useRef<AudiobookPart | undefined>(activePart);
  const sessionIdRef = useRef<string | null>(null);
  const reportRef = useRef<(pos: number) => void>(() => {});
  const reportSessionRef = useRef<(pos: number, isPaused: boolean, keepalive?: boolean) => void>(
    () => {},
  );
  const pendingLocalSeekRef = useRef<number | null>(
    localTimeForPart(activePart, clampedBookTime(initialPositionSeconds, duration)),
  );
  const playAfterSourceSwitchRef = useRef(false);
  const autoPlayPendingRef = useRef(autoPlay);
  // Wall-clock time playback last paused, for smart rewind on resume. Cleared
  // by explicit seeks so a hand-picked position is never second-guessed.
  const pausedAtRef = useRef<number | null>(null);

  const setAbsoluteTime = useCallback(
    (seconds: number) => {
      const next = clampedBookTime(seconds, duration);
      currentTimeRef.current = next;
      setCurrentTime(next);
    },
    [duration],
  );

  useEffect(() => {
    activePartRef.current = activePart;
  }, [activePart]);

  const buildKeepaliveHeaders = useCallback(() => {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
    };
    const token = config.getAccessToken();
    if (token) headers["Authorization"] = `Bearer ${token}`;
    const profileId = config.getProfileId();
    if (profileId) headers["X-Profile-Id"] = profileId;
    const profileToken = config.getProfileToken?.();
    if (profileToken) headers["X-Profile-Token"] = profileToken;
    return headers;
  }, [config]);

  const stopSession = useCallback(
    (sessionId: string, keepalive = false) => {
      const request = fetch(`${config.apiBaseUrl}/playback/${sessionId}`, {
        method: "DELETE",
        headers: buildKeepaliveHeaders(),
        keepalive,
      });
      request.catch(() => {
        // Best effort: stale sessions are retired server-side.
      });
    },
    [buildKeepaliveHeaders, config.apiBaseUrl],
  );

  useEffect(() => {
    reportRef.current = (posSeconds: number) => {
      reportProgress({
        contentId,
        positionSeconds: Math.floor(safeNumber(posSeconds)),
        durationSeconds: Math.floor(safeNumber(duration)),
      });
      reportSessionRef.current(posSeconds, audioRef.current?.paused ?? true);
    };
  }, [contentId, duration, reportProgress]);

  useEffect(() => {
    reportSessionRef.current = (posSeconds: number, isPaused: boolean, keepalive = false) => {
      const sessionId = sessionIdRef.current;
      const part = activePartRef.current;
      if (!sessionId || !part) {
        return;
      }
      const body = JSON.stringify({
        position: localTimeForPart(part, posSeconds),
        is_paused: isPaused,
      });
      if (keepalive) {
        fetch(`${config.apiBaseUrl}/playback/${sessionId}/progress`, {
          method: "POST",
          headers: buildKeepaliveHeaders(),
          body,
          keepalive: true,
        }).catch(() => {});
        return;
      }
      playerFetch(config, `/playback/${sessionId}/progress`, {
        method: "POST",
        body,
      }).catch(() => {
        // Progress is best effort and should not interrupt playback.
      });
    };
  }, [buildKeepaliveHeaders, config]);

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
    if (!fileId || !activePart) {
      setSessionState({ sessionId: null, streamUrl: "" });
      sessionIdRef.current = null;
      return;
    }

    let canceled = false;
    let startedSessionId: string | null = null;
    const localStart =
      pendingLocalSeekRef.current ?? localTimeForPart(activePart, currentTimeRef.current);

    setSessionState({ sessionId: null, streamUrl: "" });

    (async () => {
      const profileId = config.getProfileId();
      if (!profileId) {
        throw new Error("Missing active profile");
      }

      const session = await playerFetch<PlaybackSessionResponse>(config, "/playback/start", {
        method: "POST",
        body: JSON.stringify({
          file_id: fileId,
          profile_id: profileId,
          play_method: "direct",
          start_position: localStart,
          disable_progress_persistence: true,
          codecs_video: [],
          codecs_audio: [],
          containers: [],
          max_resolution: "",
          hdr: false,
        }),
      });

      if (canceled) {
        stopSession(session.session_id, true);
        return;
      }

      startedSessionId = session.session_id;
      sessionIdRef.current = session.session_id;
      setSessionState({
        sessionId: session.session_id,
        streamUrl: buildPlayerStreamUrl(
          config.apiBaseUrl,
          session.stream_url,
          config.getAccessToken(),
          session.play_method,
          localStart,
        ),
      });
    })().catch((err) => {
      if (!canceled) {
        console.error("audiobook playback session failed", err);
        toast.error(err instanceof Error ? err.message : "Failed to start audiobook playback");
      }
    });

    return () => {
      canceled = true;
      if (startedSessionId) {
        reportSessionRef.current(currentTimeRef.current, true, true);
        stopSession(startedSessionId, true);
        if (sessionIdRef.current === startedSessionId) {
          sessionIdRef.current = null;
        }
      }
    };
  }, [activePart, config, fileId, stopSession]);

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
    const onPlay = () => {
      setPlaying(true);
      reportSessionRef.current(absoluteFromAudio(), false);
    };
    const onPause = () => {
      pausedAtRef.current = performance.now();
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
    if (!audio) return;
    audio.volume = volume;
    audio.muted = muted;
    // sessionState.streamUrl keeps this in the deps so a freshly mounted
    // element picks the persisted values up before playback starts.
  }, [muted, sessionState.streamUrl, volume]);

  useEffect(() => {
    const audio = audioRef.current;
    return () => {
      if (audio && !audio.paused) {
        audio.pause();
        reportRef.current(currentTimeRef.current);
      }
    };
  }, []);

  const seekTo = useCallback(
    (seconds: number) => {
      pausedAtRef.current = null;
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

  const resumePlayback = useCallback(() => {
    const audio = audioRef.current;
    if (!audio) return;
    const pausedAtMs = pausedAtRef.current;
    pausedAtRef.current = null;
    if (smartRewindEnabled && pausedAtMs != null) {
      const rewind = smartRewindSeconds(performance.now() - pausedAtMs);
      if (rewind > 0) {
        const target = clampedBookTime(currentTimeRef.current - rewind, duration);
        const targetIndex = findPartIndex(parts, target);
        seekTo(target);
        if (targetIndex !== activeFileIndex) {
          // The rewind crossed a file boundary; playback resumes once the new
          // source is ready instead of racing the source switch.
          playAfterSourceSwitchRef.current = true;
          return;
        }
      }
    }
    audio.play().catch((err) => console.error("audiobook play failed", err));
  }, [activeFileIndex, duration, parts, seekTo, smartRewindEnabled]);

  const togglePlay = useCallback(() => {
    const audio = audioRef.current;
    if (!audio) return;
    if (audio.paused) {
      resumePlayback();
    } else {
      audio.pause();
    }
  }, [resumePlayback]);

  const skip = useCallback(
    (delta: number) => {
      seekTo(currentTimeRef.current + delta);
    },
    [seekTo],
  );

  const setRate = useCallback(
    (nextRate: number) => {
      const clamped = clampAudiobookRate(nextRate);
      setRateState(clamped);
      setBookRate(contentId, clamped);
      if (audioRef.current) audioRef.current.playbackRate = clamped;
    },
    [contentId],
  );

  const setVolume = useCallback(
    (next: number) => {
      const clamped = Math.min(1, Math.max(0, Number.isFinite(next) ? next : 1));
      setVolumeState(clamped);
      persistVolume(clamped, muted);
    },
    [muted],
  );

  const setMuted = useCallback(
    (next: boolean) => {
      setMutedState(next);
      persistVolume(volume, next);
    },
    [volume],
  );

  const executeRealtimeCommand = useCallback(
    async (command: PlaybackRealtimeCommandEnvelope) => {
      const audio = audioRef.current;

      switch (command.name) {
        case "pause":
          audio?.pause();
          return;
        case "unpause":
          if (!audio) return;
          resumePlayback();
          return;
        case "play_pause":
          if (!audio) return;
          if (audio.paused) {
            resumePlayback();
          } else {
            audio.pause();
          }
          return;
        case "seek": {
          const position = readNumericPayload(
            command.payload,
            "position",
            "position_seconds",
            "seconds",
          );
          if (position === null) {
            throw new Error("missing_seek_position");
          }
          seekTo(position);
          return;
        }
        case "set_volume": {
          const nextVolume = readNumericPayload(command.payload, "volume", "level");
          if (nextVolume === null || !audio) {
            throw new Error("missing_volume");
          }
          setVolume(nextVolume);
          if (nextVolume > 0) {
            setMuted(false);
          }
          return;
        }
        case "display_message":
          toast.info(
            readStringPayload(command.payload, "message") ?? "A server message was received.",
          );
          return;
        case "server_restarting":
        case "server_shutting_down":
          toast.warning(
            readStringPayload(command.payload, "message") ??
              (command.name === "server_restarting"
                ? "Playback may end shortly while the server restarts."
                : "Playback may end shortly while the server shuts down."),
          );
          return;
        case "stop":
        case "terminate":
          audio?.pause();
          if (audio) {
            audio.removeAttribute("src");
            audio.load();
          }
          window.setTimeout(() => onStopRequested?.(), 0);
          return;
        default:
          throw new Error("unsupported");
      }
    },
    [onStopRequested, resumePlayback, seekTo, setMuted, setVolume],
  );

  usePlaybackRealtime({
    sessionId: sessionState.sessionId,
    onCommand: executeRealtimeCommand,
  });

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

  const nextChapter = useCallback(() => {
    const target = nextChapterStart(chapters, currentChapter);
    if (target != null) seekTo(target);
  }, [chapters, currentChapter, seekTo]);

  const prevChapter = useCallback(() => {
    const target = prevChapterStart(chapters, currentChapter, currentTimeRef.current);
    if (target != null) seekTo(target);
  }, [chapters, currentChapter, seekTo]);

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
    streamUrl: sessionState.streamUrl,
    hasFile: Boolean(activePart),
    playing,
    currentTime,
    duration,
    buffered,
    rate,
    chapters,
    currentChapter,
    volume,
    muted,
    togglePlay,
    seekTo,
    skip,
    setRate,
    setVolume,
    setMuted,
    nextChapter,
    prevChapter,
    sleep: { setting: sleepSetting, remainingMs: sleepRemainingMs },
    setSleep,
  };
}
