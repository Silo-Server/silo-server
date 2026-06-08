import type { MarkerKind } from "@/api/types";

/**
 * Canonical marker-kind metadata and timecode helpers shared by app-side
 * marker UI. NOTE: the player module (`web/src/player/`) intentionally keeps
 * its own copies of the kinds/labels because it must not import app code
 * (`@/...`); keep the two in sync if a kind or label changes.
 */

/** The four editable marker kinds, in display order. */
export const MARKER_KINDS: MarkerKind[] = ["intro", "recap", "credits", "preview"];

/** Human labels. "credits" doubles as the outro, so we say so. */
export const MARKER_LABELS: Record<MarkerKind, string> = {
  intro: "Intro",
  recap: "Recap",
  credits: "Credits / Outro",
  preview: "Preview",
};

/** Formats seconds as h:mm:ss / m:ss for display. Empty string for null. */
export function formatClock(seconds: number | null | undefined): string {
  if (seconds == null) return "";
  const total = Math.max(0, Math.round(seconds));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const mm = h > 0 ? String(m).padStart(2, "0") : String(m);
  const base = `${mm}:${String(s).padStart(2, "0")}`;
  return h > 0 ? `${h}:${base}` : base;
}

/** Parses "m:ss", "h:mm:ss", or raw seconds. Returns null for empty, NaN for invalid. */
export function parseClock(value: string): number | null {
  const trimmed = value.trim();
  if (trimmed === "") return null;
  const parts = trimmed.split(":");
  if (parts.length > 3) return NaN;
  let total = 0;
  for (const part of parts) {
    const n = Number(part);
    if (part.trim() === "" || !Number.isFinite(n) || n < 0) return NaN;
    total = total * 60 + n;
  }
  return total;
}
