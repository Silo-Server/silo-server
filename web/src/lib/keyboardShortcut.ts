/**
 * How the search shortcut is spelled on this machine: the ⌘ glyph on Apple
 * keyboards, "Ctrl" everywhere else. Every surface that advertises the
 * ⌘K / Ctrl-K search shortcut reads it from here, so a Windows or Linux admin
 * is never told to press a key their keyboard does not have.
 *
 * The handlers themselves accept either modifier — this is a label, not the
 * binding.
 */
export function searchShortcutLabel(): string {
  const isApple =
    typeof navigator !== "undefined" && /Mac|iPhone|iPad|iPod/.test(navigator.userAgent);
  return isApple ? "⌘ K" : "Ctrl K";
}
