/**
 * Settings shared by the favorite, watchlist and rating toggles.
 *
 * A toggle either reaches the server or fails visibly; it is never held back
 * while the page shows it as saved. TanStack Query pauses a mutation while the
 * browser reports itself offline, which kept the optimistic state on screen
 * with nothing sent and dropped the change if the tab closed. These mutations
 * run whatever the reported network state, so an offline request fails at
 * once and rolls back with an error.
 */
export const personalStateMutationOptions = { networkMode: "always" } as const;

/**
 * How long a toggle waits for the server before failing. Writes get no
 * deadline by default because abandoning one leaves its outcome unknown, but
 * these are idempotent PUT and DELETE calls. A timed-out write can still land
 * late; the open page then keeps the rolled-back state until it is reloaded,
 * so it can say "failed" when the server saved but never "saved" when it did
 * not.
 */
export const PERSONAL_STATE_WRITE_TIMEOUT_MS = 10_000;
