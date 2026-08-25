import { useEffect, useState } from "react";

import { HERO_TRAILER_MIN_WIDTH_PX, shouldPlayHeroTrailer } from "./heroTrailer";

/**
 * Hero trailer autoplay is opt-in by environment: skip reduced-motion,
 * skip narrow viewports, and tear the iframe down when the tab is hidden
 * so YouTube stops fetching in the background.
 */
export function useHeroTrailerBackdrop(): boolean {
  const [enabled, setEnabled] = useState(false);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") {
      return;
    }

    const motion = window.matchMedia("(prefers-reduced-motion: reduce)");
    const wide = window.matchMedia(`(min-width: ${HERO_TRAILER_MIN_WIDTH_PX}px)`);

    const sync = () => {
      setEnabled(
        shouldPlayHeroTrailer({
          reducedMotion: motion.matches,
          viewportWideEnough: wide.matches,
          documentVisible: document.visibilityState === "visible",
        }),
      );
    };

    sync();
    motion.addEventListener("change", sync);
    wide.addEventListener("change", sync);
    document.addEventListener("visibilitychange", sync);
    return () => {
      motion.removeEventListener("change", sync);
      wide.removeEventListener("change", sync);
      document.removeEventListener("visibilitychange", sync);
    };
  }, []);

  return enabled;
}
