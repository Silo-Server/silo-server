/**
 * Device sign-in user codes: eight characters, shown grouped 4+4 the way
 * the TV, the /login device panel and /activate show them.
 */

/** Keeps the code's characters and groups them 4+4. */
export function formatDeviceCode(value: string) {
  const clean = value
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, "")
    .slice(0, 8);
  return clean.length <= 4 ? clean : `${clean.slice(0, 4)} ${clean.slice(4)}`;
}

/** The code without its grouping space, as the API takes it. */
export function compactDeviceCode(value: string) {
  return formatDeviceCode(value).replace(/ /g, "");
}

/** Reads the code one character at a time, as it is compared. */
export function spokenDeviceCode(value: string) {
  return compactDeviceCode(value).split("").join(" ");
}
