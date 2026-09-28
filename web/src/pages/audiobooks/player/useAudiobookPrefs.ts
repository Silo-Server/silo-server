import { useCallback, useState } from "react";
import { useSeekPreferences } from "@/hooks/queries/seekPreferences";
import {
  legacyAudiobookIntervals,
  SEEK_CHOICES,
  seekDefault,
  type SeekDirection,
} from "@/lib/seekIntervals";
import { toast } from "sonner";
import { storage } from "@/utils/storage";

export const DEFAULT_SKIP_BACK_SECONDS = seekDefault("audiobook", "back");
export const DEFAULT_SKIP_FORWARD_SECONDS = seekDefault("audiobook", "forward");

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

const LEGACY_SKIP_KEYS = {
  back: storage.KEYS.AUDIOBOOK_SKIP_BACK,
  forward: storage.KEYS.AUDIOBOOK_SKIP_FORWARD,
} as const;

/** Smart rewind defaults to on; only an explicit "false" disables it. */
export function getAudiobookSmartRewind(): boolean {
  return storage.get(storage.KEYS.AUDIOBOOK_SMART_REWIND) !== "false";
}

export interface AudiobookPrefs {
  skipBack: number;
  skipForward: number;
  smartRewind: boolean;
  /** Selectable intervals per direction, from the settings contract. */
  choices: Record<SeekDirection, number[]>;
  canEditSkipIntervals: boolean;
  hasSharedSkipIntervals: boolean;
  /** The server supports shared intervals but the profile's values could not be read. */
  sharedSkipIntervalsError: boolean;
  isSavingSkipIntervals: boolean;
  setSkipBack: (seconds: number) => void;
  setSkipForward: (seconds: number) => void;
  setSmartRewind: (enabled: boolean) => void;
}

/**
 * Profile-wide intervals with device-local speed and smart rewind preferences. Instantiate once per player
 * (in AudiobookPlayer) and pass down — multiple instances do not observe each
 * other's changes within a render lifetime.
 */
export function useAudiobookPrefs(): AudiobookPrefs {
  const shared = useSeekPreferences("audiobook");
  // Browser-local intervals: the value store on servers without shared
  // settings, and what keeps playing while a supporting server's values load.
  const [local, setLocal] = useState(legacyAudiobookIntervals);
  const [smartRewind, setSmartRewindState] = useState(getAudiobookSmartRewind);

  const useShared = shared.supported && !shared.isLoading;
  const resolve = (direction: SeekDirection) =>
    useShared
      ? direction === "back"
        ? shared.skipBack
        : shared.skipForward
      : (local[direction] ?? seekDefault("audiobook", direction));

  const { supported, legacyAvailable, save } = shared;
  const setSkip = useCallback(
    (direction: SeekDirection, seconds: number) => {
      if (supported) {
        void save(direction, seconds).catch(() =>
          toast.error(
            direction === "back"
              ? "Failed to save rewind interval"
              : "Failed to save fast-forward interval",
          ),
        );
        return;
      }
      if (!legacyAvailable) return;
      setLocal((prev) => ({ ...prev, [direction]: seconds }));
      storage.set(LEGACY_SKIP_KEYS[direction], String(seconds));
    },
    [legacyAvailable, save, supported],
  );
  const setSkipBack = useCallback((seconds: number) => setSkip("back", seconds), [setSkip]);
  const setSkipForward = useCallback((seconds: number) => setSkip("forward", seconds), [setSkip]);

  const setSmartRewind = useCallback((enabled: boolean) => {
    setSmartRewindState(enabled);
    storage.set(storage.KEYS.AUDIOBOOK_SMART_REWIND, String(enabled));
  }, []);

  return {
    skipBack: resolve("back"),
    skipForward: resolve("forward"),
    smartRewind,
    choices: SEEK_CHOICES.audiobook,
    canEditSkipIntervals: shared.supported || shared.legacyAvailable,
    hasSharedSkipIntervals: shared.supported,
    sharedSkipIntervalsError: shared.supported && shared.error != null,
    isSavingSkipIntervals: shared.isSaving,
    setSkipBack,
    setSkipForward,
    setSmartRewind,
  };
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
