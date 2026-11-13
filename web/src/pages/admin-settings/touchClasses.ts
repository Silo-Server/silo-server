/*
 * Phone and tablet adjustments shared by the Invitations and Invite Codes tabs.
 * Every class here applies below a breakpoint only, so the desktop layout keeps
 * its original look.
 */

/**
 * Grows a centered dialog's built-in close button to a 44px target below `lg`
 * without moving its icon: the padding adds the area, the offset cancels it.
 */
export const TOUCH_DIALOG_CLOSE_CLASS =
  "max-lg:[&>[data-slot=dialog-close]]:end-0.5 max-lg:[&>[data-slot=dialog-close]]:top-0.5 max-lg:[&>[data-slot=dialog-close]]:p-3.5 max-lg:[&>[data-slot=dialog-close]]:leading-[0]";

/**
 * Below `sm` the dialog fills the screen and scrolls as one page, with its
 * built-in close button hidden; pair it with `FullScreenDialogHeader` for a
 * 44px close button and `PINNED_FOOTER_CLASS` for the action row. From `sm` up
 * it stays a centered box. Combine with `TOUCH_DIALOG_CLOSE_CLASS`.
 */
export const FULL_SCREEN_DIALOG_CLASS =
  "max-sm:inset-0 max-sm:h-dvh max-sm:max-h-none max-sm:w-dvw max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:content-start max-sm:rounded-none max-sm:border-0 max-sm:px-4 max-sm:pt-[calc(1rem+env(safe-area-inset-top))] max-sm:pb-0 max-sm:[&>[data-slot=dialog-close]]:hidden";

/**
 * The action row of a full-screen dialog, kept at the bottom of the screen
 * while the form scrolls, clear of the home indicator.
 */
export const PINNED_FOOTER_CLASS =
  "max-sm:border-border max-sm:bg-background max-sm:sticky max-sm:bottom-0 max-sm:z-10 max-sm:-mx-4 max-sm:border-t max-sm:px-4 max-sm:pt-3 max-sm:pb-[calc(0.75rem+env(safe-area-inset-bottom))]";

/** Bottom spacing for full-screen dialog content without a pinned footer. */
export const FULL_SCREEN_BOTTOM_CLASS = "max-sm:pb-[calc(1rem+env(safe-area-inset-bottom))]";

/** A Switch's tap area grown to about 44px below `lg`, without changing its size. */
export const TOUCH_SWITCH_CLASS =
  "max-lg:relative max-lg:after:absolute max-lg:after:-inset-x-2 max-lg:after:-inset-y-3.5";

/** Buttons and inputs: 44px tall below `lg`. */
export const TOUCH_FIELD_CLASS = "max-lg:h-11";

/** Select triggers in a form: 44px tall and full width below `lg`. */
export const TOUCH_SELECT_CLASS = "max-lg:h-11! max-lg:w-full";

/** Select options tall enough to tap below `lg`. */
export const TOUCH_SELECT_ITEM_CLASS = "max-lg:min-h-11";

/** A primary action that becomes a full-width 44px button below `lg`. */
export const TOUCH_PRIMARY_ACTION_CLASS = "max-lg:h-11 max-lg:w-full";

/** Menu rows tall enough to tap, as in the libraries card menu. */
export const MENU_ITEM_CLASS = "min-h-11";
