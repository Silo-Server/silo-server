import { useCallback, useState } from "react";
import { storage } from "@/utils/storage";

export const SKIP_INTERVAL_CHOICES = [5, 10, 15, 30, 45, 60, 90] as const;
export const DEFAULT_SKIP_BACK_SECONDS = 10;
export const DEFAULT_SKIP_FORWARD_SECONDS = 30;

export const AUDIOBOOK_RATE_MIN = 0.5;
export const AUDIOBOOK_RATE_MAX = 3;
export const AUDIOBOOK_RATE_STEP = 0.05;
export const AUDIOBOOK_RATE_PRESETS = [1, 1.25, 1.5, 1.75, 2, 2.5, 3] as const;

export function clampAudiobookRate(rate: number): number {
  if (!Number.isFinite(rate)) return 1;
  const clamped = Math.min(AUDIOBOOK_RATE_MAX, Math.max(AUDIOBOOK_RATE_MIN, rate));
  // Snap to the step grid so repeated +/- stepping never accumulates float drift.
  return Number((Math.round(clamped / AUDIOBOOK_RATE_STEP) * AUDIOBOOK_RATE_STEP).toFixed(2));
}

type SkipKey = typeof storage.KEYS.AUDIOBOOK_SKIP_BACK | typeof storage.KEYS.AUDIOBOOK_SKIP_FORWARD;

function readSkipSeconds(key: SkipKey, fallback: number): number {
  const raw = storage.get(key);
  const value = raw == null ? NaN : Number(raw);
  return (SKIP_INTERVAL_CHOICES as readonly number[]).includes(value) ? value : fallback;
}

export function getAudiobookSkipBack(): number {
  return readSkipSeconds(storage.KEYS.AUDIOBOOK_SKIP_BACK, DEFAULT_SKIP_BACK_SECONDS);
}

export function getAudiobookSkipForward(): number {
  return readSkipSeconds(storage.KEYS.AUDIOBOOK_SKIP_FORWARD, DEFAULT_SKIP_FORWARD_SECONDS);
}

/** Smart rewind defaults to on; only an explicit "false" disables it. */
export function getAudiobookSmartRewind(): boolean {
  return storage.get(storage.KEYS.AUDIOBOOK_SMART_REWIND) !== "false";
}

export interface AudiobookPrefs {
  skipBack: number;
  skipForward: number;
  smartRewind: boolean;
  setSkipBack: (seconds: number) => void;
  setSkipForward: (seconds: number) => void;
  setSmartRewind: (enabled: boolean) => void;
}

/**
 * Device-local audiobook player preferences. Instantiate once per player
 * (in AudiobookPlayer) and pass down — multiple instances do not observe each
 * other's changes within a render lifetime.
 */
export function useAudiobookPrefs(): AudiobookPrefs {
  const [skipBack, setSkipBackState] = useState(getAudiobookSkipBack);
  const [skipForward, setSkipForwardState] = useState(getAudiobookSkipForward);
  const [smartRewind, setSmartRewindState] = useState(getAudiobookSmartRewind);

  const setSkipBack = useCallback((seconds: number) => {
    setSkipBackState(seconds);
    storage.set(storage.KEYS.AUDIOBOOK_SKIP_BACK, String(seconds));
  }, []);

  const setSkipForward = useCallback((seconds: number) => {
    setSkipForwardState(seconds);
    storage.set(storage.KEYS.AUDIOBOOK_SKIP_FORWARD, String(seconds));
  }, []);

  const setSmartRewind = useCallback((enabled: boolean) => {
    setSmartRewindState(enabled);
    storage.set(storage.KEYS.AUDIOBOOK_SMART_REWIND, String(enabled));
  }, []);

  return { skipBack, skipForward, smartRewind, setSkipBack, setSkipForward, setSmartRewind };
}

// --- Per-book playback rate memory -----------------------------------------

const MAX_REMEMBERED_BOOK_RATES = 50;

interface StoredBookRate {
  rate: number;
  at: number;
}

function readBookRates(): Record<string, StoredBookRate> {
  const raw = storage.get(storage.KEYS.AUDIOBOOK_RATES);
  if (!raw) return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return {};
    const out: Record<string, StoredBookRate> = {};
    for (const [key, value] of Object.entries(parsed)) {
      const entry = value as Partial<StoredBookRate> | null;
      if (
        entry &&
        typeof entry.rate === "number" &&
        Number.isFinite(entry.rate) &&
        typeof entry.at === "number"
      ) {
        out[key] = { rate: entry.rate, at: entry.at };
      }
    }
    return out;
  } catch {
    return {};
  }
}

/** Last playback rate used for this book, or null if never adjusted. */
export function getBookRate(contentId: string): number | null {
  const entry = readBookRates()[contentId];
  return entry ? clampAudiobookRate(entry.rate) : null;
}

/** Remember the playback rate for this book (LRU-capped). */
export function setBookRate(contentId: string, rate: number): void {
  const rates = readBookRates();
  rates[contentId] = { rate: clampAudiobookRate(rate), at: Date.now() };
  const entries = Object.entries(rates);
  if (entries.length > MAX_REMEMBERED_BOOK_RATES) {
    entries
      .sort((a, b) => a[1].at - b[1].at)
      .slice(0, entries.length - MAX_REMEMBERED_BOOK_RATES)
      .forEach(([key]) => delete rates[key]);
  }
  storage.set(storage.KEYS.AUDIOBOOK_RATES, JSON.stringify(rates));
}
