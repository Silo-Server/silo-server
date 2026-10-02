import { formatDate } from "@/lib/datetime";

export interface RelativeTimeOptions {
  /** Unit arithmetic: "round" (default) or "floor". */
  rounding?: "round" | "floor";
  /** Label for deltas under one minute (default "just now"). */
  justNowLabel?: string;
  /** Switch to an absolute date once the delta reaches this many days. */
  absoluteAfterDays?: number;
  /** Absolute-date renderer (default: preference-aware numeric formatDate). */
  absolute?: (date: Date) => string;
}

/**
 * Compact relative-time label ("just now", "5m ago", "3h ago", "2d ago"),
 * optionally switching to an absolute date after `absoluteAfterDays`.
 * Returns null for missing or unparseable values so call sites can pick
 * their own fallback ("—", "Never", the raw value, ...).
 */
export function formatRelativeTime(
  value: string | null | undefined,
  options?: RelativeTimeOptions,
): string | null {
  if (!value) {
    return null;
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return null;
  }
  const toUnit = options?.rounding === "floor" ? Math.floor : Math.round;
  // Each tier is derived from the raw millisecond delta — deriving hours from
  // the already-rounded minute count would compound rounding near boundaries
  // (e.g. 89.5 minutes showing "2h ago").
  const diffMs = Date.now() - date.getTime();
  const diffMinutes = toUnit(diffMs / 60_000);
  if (diffMinutes < 1) {
    return options?.justNowLabel ?? "just now";
  }
  if (diffMinutes < 60) {
    return `${diffMinutes}m ago`;
  }
  const diffHours = toUnit(diffMs / 3_600_000);
  if (diffHours < 24) {
    return `${diffHours}h ago`;
  }
  const diffDays = toUnit(diffMs / 86_400_000);
  if (diffDays < (options?.absoluteAfterDays ?? Number.POSITIVE_INFINITY)) {
    return `${diffDays}d ago`;
  }
  return (options?.absolute ?? formatDate)(date);
}

export function formatBirthDate(dateStr: string): string {
  const date = new Date(dateStr + "T00:00:00");
  return formatDate(date, "medium");
}

export function computeAge(birthStr: string, deathStr?: string): number {
  const birth = new Date(birthStr + "T00:00:00");
  const ref = deathStr ? new Date(deathStr + "T00:00:00") : new Date();
  let age = ref.getFullYear() - birth.getFullYear();
  const monthDiff = ref.getMonth() - birth.getMonth();
  if (monthDiff < 0 || (monthDiff === 0 && ref.getDate() < birth.getDate())) {
    age--;
  }
  return age;
}
