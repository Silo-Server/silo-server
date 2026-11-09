import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";
import { storage } from "@/utils/storage";

const DEFINITION = SETTING_DEFINITIONS[SETTING_KEYS.PLAYER_PASSOUT_THRESHOLD];
const DEFAULT_THRESHOLD = DEFINITION.defaultValue as number;
const MIN_THRESHOLD = DEFINITION.minimum as number;
export const STILL_WATCHING_MAX = DEFINITION.maximum as number;
export const STILL_WATCHING_LABEL = DEFINITION.label;

/**
 * Pass-out protection: counts consecutive unattended episode advances in this tab.
 *
 * With threshold N, N automatic advances in a row are allowed and the next one
 * waits for the viewer. Any start the viewer made clears the count. The count
 * lives in memory because it describes this one session, which no other tab or
 * device can observe.
 */
let automaticStarts = 0;

export function recordAutoAdvance(): void {
  automaticStarts += 1;
}

export function resetStillWatchingStreak(): void {
  automaticStarts = 0;
}

/** The limit is per browser: the contract classes it as client-local. */
export function getStillWatchingThreshold(): number {
  const stored = storage.get(storage.KEYS.STILL_WATCHING_THRESHOLD);
  if (stored === null) return DEFAULT_THRESHOLD;
  const value = Number(stored);
  return Number.isInteger(value) && value >= MIN_THRESHOLD && value <= STILL_WATCHING_MAX
    ? value
    : DEFAULT_THRESHOLD;
}

export function setStillWatchingThreshold(threshold: number): void {
  storage.set(storage.KEYS.STILL_WATCHING_THRESHOLD, String(threshold));
}

/** Whether the next automatic start has to wait for the viewer. */
export function isStillWatchingGated(): boolean {
  const threshold = getStillWatchingThreshold();
  return threshold > 0 && automaticStarts >= threshold;
}
