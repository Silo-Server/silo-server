/** Where a letterboxed picture sits when a touch player is held upright. */
export interface PortraitDock {
  /** Distance from the player's top edge to the picture's top edge, px. */
  pictureTop: number;
  /** Distance from the player's top edge to the picture's bottom edge, px. */
  pictureBottom: number;
}

/** Room the docked controls need under the picture, in rem. */
const DOCK_MIN_REM = 15;

/**
 * A phone in portrait shows a film as a band across the middle of the
 * screen. The HUD then gathers around that band, top bar just above it and
 * controls just below, instead of spreading to the screen edges. Returns
 * null when the picture fills the screen or leaves too little room below it
 * for the controls, in which case the HUD keeps its normal edge layout.
 */
export function portraitDock(
  width: number,
  height: number,
  videoAspect: number,
  rem: number,
): PortraitDock | null {
  if (width <= 0 || height <= width || videoAspect <= 0) return null;
  const pictureHeight = Math.min(height, width / videoAspect);
  const letterbox = (height - pictureHeight) / 2;
  if (letterbox < DOCK_MIN_REM * rem) return null;
  return { pictureTop: letterbox, pictureBottom: letterbox + pictureHeight };
}
