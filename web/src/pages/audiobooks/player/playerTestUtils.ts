import { vi } from "vitest";
import type { AudiobookPlayback } from "./useAudiobookPlayback";
import { SEEK_CHOICES } from "@/lib/seekIntervals";
import {
  DEFAULT_SKIP_BACK_SECONDS,
  DEFAULT_SKIP_FORWARD_SECONDS,
  type AudiobookPrefs,
} from "./useAudiobookPrefs";

export function makePlayback(over: Partial<AudiobookPlayback> = {}): AudiobookPlayback {
  return {
    audioRef: { current: null },
    streamUrl: "",
    hasFile: true,
    playing: false,
    currentTime: 0,
    duration: 600,
    buffered: null,
    rate: 1,
    chapters: [],
    currentChapter: null,
    volume: 1,
    muted: false,
    togglePlay: vi.fn(),
    seekTo: vi.fn(),
    skip: vi.fn(),
    setRate: vi.fn(),
    setVolume: vi.fn(),
    setMuted: vi.fn(),
    nextChapter: vi.fn(),
    prevChapter: vi.fn(),
    sleep: { setting: { kind: "off" as const }, remainingMs: null },
    setSleep: vi.fn(),
    ...over,
  };
}

export function makePrefs(over: Partial<AudiobookPrefs> = {}): AudiobookPrefs {
  return {
    skipBack: DEFAULT_SKIP_BACK_SECONDS,
    skipForward: DEFAULT_SKIP_FORWARD_SECONDS,
    smartRewind: true,
    choices: SEEK_CHOICES.audiobook,
    canEditSkipIntervals: true,
    hasSharedSkipIntervals: true,
    sharedSkipIntervalsError: false,
    isSavingSkipIntervals: false,
    setSkipBack: vi.fn(),
    setSkipForward: vi.fn(),
    setSmartRewind: vi.fn(),
    ...over,
  };
}
