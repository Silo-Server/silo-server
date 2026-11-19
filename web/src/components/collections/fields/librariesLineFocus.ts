import { motionScrollBehavior } from "@/hooks/usePrefersReducedMotion";

export const LIBRARIES_LINE_ID = "collection-libraries";

/** Moves focus to the Libraries line, for "Change" in Where it shows. */
export function focusLibrariesLine() {
  const line = document.getElementById(LIBRARIES_LINE_ID);
  line?.scrollIntoView({ block: "center", behavior: motionScrollBehavior() });
  line?.querySelector("button")?.focus();
}
