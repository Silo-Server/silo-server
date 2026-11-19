import { numberText } from "@/i18n";

/**
 * Formats a playback position for display as m:ss, or h:mm:ss from one hour on,
 * in the screen's digits. Never send, store or seek with the result.
 */
export function formatTime(seconds: number): string {
  const s = Math.floor(seconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) {
    return numberText(`${h}:${m.toString().padStart(2, "0")}:${sec.toString().padStart(2, "0")}`);
  }
  return numberText(`${m}:${sec.toString().padStart(2, "0")}`);
}
