import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";
import { storage } from "@/utils/storage";

export type SeekMedia = "video" | "audiobook";
export type SeekDirection = "back" | "forward";
export const SEEK_KEYS = {
  video: {
    back: SETTING_KEYS.PLAYER_VIDEO_SKIP_BACK_SECONDS,
    forward: SETTING_KEYS.PLAYER_VIDEO_SKIP_FORWARD_SECONDS,
  },
  audiobook: {
    back: SETTING_KEYS.PLAYER_AUDIOBOOK_SKIP_BACK_SECONDS,
    forward: SETTING_KEYS.PLAYER_AUDIOBOOK_SKIP_FORWARD_SECONDS,
  },
} as const;

function choicesFor(media: SeekMedia, direction: SeekDirection): number[] {
  return SETTING_DEFINITIONS[SEEK_KEYS[media][direction]].values!.map(({ value }) => Number(value));
}

/** Built once from the static contract so consumers get a stable array per render. */
export const SEEK_CHOICES: Record<SeekMedia, Record<SeekDirection, number[]>> = {
  video: { back: choicesFor("video", "back"), forward: choicesFor("video", "forward") },
  audiobook: {
    back: choicesFor("audiobook", "back"),
    forward: choicesFor("audiobook", "forward"),
  },
};

export function seekChoices(media: SeekMedia, direction: SeekDirection): number[] {
  return SEEK_CHOICES[media][direction];
}

export function seekDefault(media: SeekMedia, direction: SeekDirection): number {
  return Number(SETTING_DEFINITIONS[SEEK_KEYS[media][direction]].defaultValue);
}

export function validSeekInterval(
  media: SeekMedia,
  direction: SeekDirection,
  value: unknown,
): value is number {
  return typeof value === "number" && seekChoices(media, direction).includes(value);
}

/** Legacy values have no profile identity: only an explicit user action may import them. */
export function legacyAudiobookIntervals(): Partial<Record<SeekDirection, number>> {
  const result: Partial<Record<SeekDirection, number>> = {};
  for (const direction of ["back", "forward"] as const) {
    const raw = storage.get(
      direction === "back" ? storage.KEYS.AUDIOBOOK_SKIP_BACK : storage.KEYS.AUDIOBOOK_SKIP_FORWARD,
    );
    const value = raw == null ? NaN : Number(raw);
    if (validSeekInterval("audiobook", direction, value)) result[direction] = value;
  }
  return result;
}
