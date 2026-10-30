/**
 * The hero trailer sequence: hold the backdrop still, fade it out to black,
 * then fade the muted clip in — and whenever the clip cannot play, the still
 * simply stays. No spinner, no black screen.
 *
 * Pure: no React import, so the two decisions worth pinning down — whether a
 * slide may play its clip at all, and which stage the sequence is in — are
 * tested on their own in `heroTrailer.test.ts`. `HeroBanner` owns the timers
 * and the `<video>` element, and reads its numbers from here.
 */

/** How long the still is held on screen before the clip may be revealed. */
export const HERO_TRAILER_STILL_HOLD_MS = 2500;
/** How long the still takes to fade out to black. */
export const HERO_TRAILER_BLACK_MS = 400;
/** How long the clip takes to fade in from black. */
export const HERO_TRAILER_FADE_IN_MS = 600;

/**
 * - `still` — the backdrop is on screen (the clip may already be downloading);
 * - `black` — the backdrop is fading out and the clip is not visible yet;
 * - `video` — the clip fades in and then plays.
 */
export type HeroTrailerStage = "still" | "black" | "video";

export interface HeroTrailerGateInput {
  /** The slide has a clip URL, and it has not failed to load. */
  hasClip: boolean;
  /** `prefers-reduced-motion: reduce` asks for the backdrop alone. */
  reducedMotion: boolean;
  /** The browser's data-saver setting asks for the backdrop alone. */
  saveData: boolean;
}

/** May this slide reveal and play its clip at all? */
export function heroTrailerEnabled({
  hasClip,
  reducedMotion,
  saveData,
}: HeroTrailerGateInput): boolean {
  return hasClip && !reducedMotion && !saveData;
}

export interface HeroTrailerStageInput {
  /** A clip exists, has not failed, and no gate asked for the backdrop alone. */
  enabled: boolean;
  /** The still has been on screen for `HERO_TRAILER_STILL_HOLD_MS`. */
  held: boolean;
  /** The clip answered `canplay` — without it a fade would land on black. */
  ready: boolean;
  /** The fade to black has run its course. */
  faded: boolean;
}

/**
 * Which layer is on screen, derived from the sequence's inputs rather than
 * moved between states by effects: an unavailable clip can never leave `still`,
 * and the fade can never start before the clip is ready, both by construction.
 */
export function heroTrailerStage({
  enabled,
  held,
  ready,
  faded,
}: HeroTrailerStageInput): HeroTrailerStage {
  if (!enabled || !held || !ready) {
    return "still";
  }

  return faded ? "video" : "black";
}
