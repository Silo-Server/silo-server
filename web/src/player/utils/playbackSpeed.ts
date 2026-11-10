import { t } from "@/i18n";

/** The speeds the iOS, macOS, and Android phone players offer. */
export const PLAYBACK_SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 1.75, 2, 2.5, 3] as const;

export function formatPlaybackSpeed(rate: number): string {
  return t("web.player.speed.rate", { rate: String(rate) });
}
