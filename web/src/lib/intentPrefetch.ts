/**
 * Shared state behind intent prefetching (see `useIntentPrefetch`): where the
 * pointer is and how it got there, whether a speculative prefetch is already
 * running, and whether the connection should skip speculation altogether.
 *
 * A speculative prefetch is one the user has not committed to: the pointer
 * settling on a card, keyboard focus resting on a link, a finger held still.
 * A press (mouse button down, a tap) is near-certain and skips the limits.
 *
 * Each prefetch adds a `silo:prefetch` User Timing mark (see
 * itemNavigationTiming.ts for how navigations report which one warmed them).
 */

export type IntentTrigger = "settle" | "focus" | "touch-hold" | "press" | "tap";

/** The pointer counts as settled once it moves less than this... */
export const SETTLE_DISTANCE_PX = 8;
/** ...over this trailing window. hoverIntent's classic rule is 6 px per 100 ms. */
export const SETTLE_WINDOW_MS = 100;
/** A touch that travels further than this is a swipe, not a tap. */
export const TOUCH_SLOP_PX = 10;
/** A finger held for a tap barely moves; a slow scroll moves more than this during the hold. */
export const TOUCH_HOLD_SLOP_PX = 4;
/** The move that carries the pointer onto an element can arrive just before its pointerenter. */
const ENTRY_GRACE_MS = 20;

interface PointerSample {
  t: number;
  x: number;
  y: number;
}

const SAMPLE_LIMIT = 32;
const samples: PointerSample[] = [];
// Last real mouse or pen movement, and the last wheel, scroll, drag, or press.
// A card only counts as aimed at when the pointer moved to it after anything
// that could have slid content underneath a still pointer.
let lastMoveAt = Number.NEGATIVE_INFINITY;
let lastDisturbedAt = Number.NEGATIVE_INFINITY;
let touch: { id: number; x: number; y: number; travel: number } | null = null;
let tracking = false;

function now(): number {
  return performance.now();
}

function onPointerMove(event: PointerEvent) {
  if (event.pointerType === "touch") {
    if (touch?.id === event.pointerId) {
      const travel = Math.hypot(event.clientX - touch.x, event.clientY - touch.y);
      touch.travel = Math.max(touch.travel, travel);
    }
    return;
  }
  if (event.buttons !== 0) {
    lastDisturbedAt = now();
    return;
  }
  const last = samples[samples.length - 1];
  // Browsers replay a move at the same coordinates after layout changes;
  // only a changed position is the user moving the pointer.
  if (last && last.x === event.clientX && last.y === event.clientY) return;
  const t = now();
  samples.push({ t, x: event.clientX, y: event.clientY });
  if (samples.length > SAMPLE_LIMIT) samples.shift();
  lastMoveAt = t;
}

function onPointerDown(event: PointerEvent) {
  if (event.pointerType === "touch") {
    touch = { id: event.pointerId, x: event.clientX, y: event.clientY, travel: 0 };
    return;
  }
  lastDisturbedAt = now();
}

function onDisturb() {
  lastDisturbedAt = now();
}

/** Installs the window listeners once. Every listener is passive. */
export function ensurePointerTracking() {
  if (tracking || typeof window === "undefined") return;
  tracking = true;
  const options = { capture: true, passive: true } as const;
  window.addEventListener("pointermove", onPointerMove, options);
  window.addEventListener("pointerdown", onPointerDown, options);
  window.addEventListener("wheel", onDisturb, options);
  window.addEventListener("scroll", onDisturb, options);
}

function positionAt(t: number): PointerSample {
  for (let i = samples.length - 1; i >= 0; i--) {
    if (samples[i]!.t <= t) return samples[i]!;
  }
  return samples[0]!;
}

/**
 * Whether the pointer moved onto `element` (entered at `enteredAt`) by itself
 * and has since slowed to a stop there: under `SETTLE_DISTANCE_PX` over the
 * last `SETTLE_WINDOW_MS`. An element that appeared or slid under a still
 * pointer does not count until the pointer moves.
 */
export function pointerSettledIn(element: Element, enteredAt: number): boolean {
  const current = samples[samples.length - 1];
  if (!current || lastMoveAt <= lastDisturbedAt || lastMoveAt < enteredAt - ENTRY_GRACE_MS) {
    return false;
  }
  const rect = element.getBoundingClientRect();
  if (
    current.x < rect.left ||
    current.x > rect.right ||
    current.y < rect.top ||
    current.y > rect.bottom
  ) {
    return false;
  }
  const anchor = positionAt(now() - SETTLE_WINDOW_MS);
  return Math.hypot(current.x - anchor.x, current.y - anchor.y) < SETTLE_DISTANCE_PX;
}

/** How far the touch with `pointerId` has travelled since it went down. */
export function touchTravel(pointerId: number): number {
  return touch?.id === pointerId ? touch.travel : Number.POSITIVE_INFINITY;
}

interface NetworkInformationLike {
  saveData?: boolean;
  effectiveType?: string;
}

/** False under Save-Data or on a 2G connection, where only presses prefetch. */
export function speculationAllowed(): boolean {
  const connection = (navigator as Navigator & { connection?: NetworkInformationLike }).connection;
  return !(
    connection?.saveData === true ||
    connection?.effectiveType === "slow-2g" ||
    connection?.effectiveType === "2g"
  );
}

const PREFETCH_RECORD_LIMIT = 50;
/** The query client's staleTime: an older prefetch has gone stale or been replaced. */
const PREFETCH_ATTRIBUTION_MS = 2 * 60_000;
/** Marks are cleared past this count so a long session does not keep them all. */
const TIMING_MARK_LIMIT = 200;
let timingMarks = 0;
const prefetchRecords = new Map<string, { trigger: IntentTrigger; at: number }>();
let speculativeInFlight = false;

/**
 * Starts `prefetch` for `target` unless it is speculative and either another
 * speculative prefetch is still running or the connection asks to save data.
 * Running prefetches are never cancelled: a click dedupes onto the one in
 * flight, and a finished one is cache the next visit can use.
 */
export function startIntentPrefetch(
  target: string,
  trigger: IntentTrigger,
  prefetch: () => Promise<unknown>,
): boolean {
  const speculative = trigger !== "press" && trigger !== "tap";
  if (speculative && (speculativeInFlight || !speculationAllowed())) return false;

  const at = now();
  // Keep the earliest recent prefetch: a press after a settle dedupes onto
  // the settle's request, so the settle is what warmed the cache.
  const earlier = prefetchRecords.get(target);
  if (!earlier || at - earlier.at > PREFETCH_ATTRIBUTION_MS) {
    prefetchRecords.delete(target);
    prefetchRecords.set(target, { trigger, at });
    if (prefetchRecords.size > PREFETCH_RECORD_LIMIT) {
      prefetchRecords.delete(prefetchRecords.keys().next().value!);
    }
  }
  try {
    if (++timingMarks > TIMING_MARK_LIMIT) {
      performance.clearMarks("silo:prefetch");
      timingMarks = 1;
    }
    performance.mark("silo:prefetch", { detail: { target, trigger } });
  } catch {
    // An engine without User Timing options loses the mark, nothing else.
  }

  const pending = prefetch();
  if (speculative) {
    speculativeInFlight = true;
    const release = () => {
      speculativeInFlight = false;
    };
    void pending.then(release, release);
  }
  return true;
}

/** The first prefetch started for `target` in the last two minutes, if any. */
export function recentIntentPrefetch(target: string) {
  const record = prefetchRecords.get(target);
  return record && now() - record.at <= PREFETCH_ATTRIBUTION_MS ? record : undefined;
}

/** Test seam: forgets all pointer history and prefetch state. */
export function resetIntentPrefetchState() {
  samples.length = 0;
  lastMoveAt = Number.NEGATIVE_INFINITY;
  lastDisturbedAt = Number.NEGATIVE_INFINITY;
  touch = null;
  prefetchRecords.clear();
  speculativeInFlight = false;
}
