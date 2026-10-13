import { useState } from "react";

/**
 * Parses a typed percentage and clamps it to `min`-100, or returns null when
 * the text is not a number.
 *
 * An emptied field is null rather than `min`: `Number("")` is `0`, a
 * valid-looking parse that isn't a value the user actually typed, and silently
 * saving `min` on a stray clear is how a subtitle opacity field would jump to
 * nearly invisible with no confirmation.
 */
export function parsePercent(raw: string, min: number): number | null {
  const trimmed = raw.trim().replace(/%$/, "").trim();
  if (trimmed === "") return null;
  const parsed = Math.round(Number(trimmed));
  if (!Number.isFinite(parsed)) return null;
  return Math.min(100, Math.max(min, parsed));
}

/**
 * Draft-and-commit state for a typed percentage input (min-100): free typing
 * while focused, clamped and committed on blur/Enter.
 *
 * The draft exists only while the user is editing. Otherwise the field shows
 * `value`, so a save that fails upstream, or a value synced from another
 * client, is never masked by stale typed text. A commit that lands on the
 * current value (focus and blur without typing, or "999" at 100) calls nothing.
 */
export function usePercentDraft(value: number, min: number, onChange: (v: number) => void) {
  const [editing, setEditing] = useState<string | null>(null);

  function commit(raw: string) {
    setEditing(null);
    const next = parsePercent(raw, min);
    if (next !== null && next !== value) onChange(next);
  }

  return { draft: editing ?? String(value), setDraft: setEditing, commit };
}
