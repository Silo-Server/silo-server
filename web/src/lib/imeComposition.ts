import type { KeyboardEvent as ReactKeyboardEvent } from "react";

/**
 * Whether a keydown belongs to an input method editor (IME) rather than to the
 * field. Japanese and Chinese typists press Enter to confirm a conversion and
 * the arrow keys to pick a candidate, so a custom key handler on a text field
 * must ignore those keystrokes.
 *
 * `isComposing` alone is not enough: Safari delivers `compositionend` before
 * the confirming Enter keydown, so that keydown arrives with `isComposing`
 * false. Browsers still report it with `keyCode` 229, which catches it.
 *
 * Native implicit form submission is already IME-safe; this is only for
 * handlers that act on keys themselves.
 */
export function isImeComposing(event: KeyboardEvent | ReactKeyboardEvent): boolean {
  const native = "nativeEvent" in event ? event.nativeEvent : event;
  return native.isComposing || native.keyCode === 229;
}
