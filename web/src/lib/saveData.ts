/**
 * Whether the browser asks pages to save data (Chromium's Data Saver, the
 * `Save-Data` request hint). The Network Information API behind it is absent
 * in Safari and Firefox, where this answers false.
 */
export function prefersReducedData(): boolean {
  if (typeof navigator === "undefined") return false;
  const connection = (navigator as Navigator & { connection?: { saveData?: boolean } }).connection;
  return connection?.saveData === true;
}
