export const LIBRARIES_LINE_ID = "collection-libraries";

/** Moves focus to the Libraries line, for "Change" in Where it shows. */
export function focusLibrariesLine() {
  const line = document.getElementById(LIBRARIES_LINE_ID);
  line?.scrollIntoView({ block: "center", behavior: "smooth" });
  line?.querySelector("button")?.focus();
}
