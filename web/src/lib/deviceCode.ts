/**
 * Device sign-in user codes: eight characters, shown grouped 4+4 the way
 * the TV, the /login device panel and /activate show them. Jellyfin Quick
 * Connect codes are six digits, shown ungrouped as the Jellyfin app shows
 * them.
 */

export const DEVICE_CODE_LENGTH = 8;
export const QUICK_CONNECT_CODE_LENGTH = 6;

/** Keeps the code's characters and groups them 4+4. */
export function formatDeviceCode(value: string) {
  const clean = value
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, "")
    .slice(0, DEVICE_CODE_LENGTH);
  return clean.length <= 4 ? clean : `${clean.slice(0, 4)} ${clean.slice(4)}`;
}

/** The code without its grouping space, as the API takes it. */
export function compactDeviceCode(value: string) {
  return formatDeviceCode(value).replace(/ /g, "");
}

/** Whether a compact code is whole: a device sign-in or a Quick Connect code. */
export function isCompleteDeviceCode(compact: string) {
  return compact.length === DEVICE_CODE_LENGTH || compact.length === QUICK_CONNECT_CODE_LENGTH;
}

/** A request's code as the card shows it, matching the device's screen. */
export function displayDeviceCode(value: string) {
  const compact = compactDeviceCode(value);
  return compact.length === QUICK_CONNECT_CODE_LENGTH ? compact : formatDeviceCode(compact);
}

/** Reads the code one character at a time, as it is compared. */
export function spokenDeviceCode(value: string) {
  return compactDeviceCode(value).split("").join(" ");
}

/** What to tell a person whose account has tried too many Quick Connect codes. */
export function tooManyCodesMessage(retryAfterSeconds: number | null) {
  const minutes = retryAfterSeconds ? Math.max(1, Math.ceil(retryAfterSeconds / 60)) : null;
  return minutes
    ? `You've tried too many codes. Try again in ${minutes} minute${minutes === 1 ? "" : "s"}.`
    : "You've tried too many codes. Try again later.";
}
