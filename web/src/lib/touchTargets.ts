/**
 * Below `lg`, gives the controls inside an element a touch target of at least
 * 44px. Put it on a page's root, and on the content of each dialog, menu and
 * select list the page opens: those render in a portal, outside the root.
 *
 * Buttons, inputs, select triggers, sliders, and menu and select items grow
 * to 44px. Compact controls that sit inline with text (`xs` and link-style buttons,
 * switches, checkboxes, a dialog's close button) keep their look and get an
 * invisible 44px hit area centered on them instead. A button is matched by the
 * `data-variant` and `data-size` attributes it always renders, since a Radix
 * trigger wrapping it replaces its `data-slot`.
 */
export const TOUCH_TARGETS = [
  "max-lg:[&_[data-variant][data-size]:not([data-size=xs]):not([data-variant=link])]:min-h-11",
  "max-lg:[&_[data-variant][data-size^=icon]]:min-w-11",
  "max-lg:[&_:is([data-slot=input],[data-slot=select-trigger],[data-slot=slider],[data-slot=dropdown-menu-item],[data-slot=select-item])]:min-h-11",
  "max-lg:[&_:is([data-variant][data-size=xs],[data-variant=link][data-size],[data-slot=switch],[data-slot=checkbox])]:relative",
  // The close button is positioned already, so it holds its hit area as it is.
  "max-lg:[&_:is([data-variant][data-size=xs],[data-variant=link][data-size],[data-slot=switch],[data-slot=checkbox],[data-slot=dialog-close])]:after:absolute",
  // 44px tall and at least 44px wide, centered on the control.
  "max-lg:[&_:is([data-variant][data-size=xs],[data-variant=link][data-size],[data-slot=switch],[data-slot=checkbox],[data-slot=dialog-close])]:after:inset-[calc(50%-1.375rem)_min(0px,calc(50%-1.375rem))]",
].join(" ");

/**
 * An invisible 44px hit area below `lg` for one small inline control (a text
 * link, a compact toggle) that should keep its look. The control must not be
 * positioned already.
 */
export const TOUCH_HIT_AREA =
  "max-lg:relative max-lg:after:absolute max-lg:after:inset-[calc(50%-1.375rem)_min(0px,calc(50%-1.375rem))]";
