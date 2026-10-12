/**
 * The slice of the Network Information API we use. It is not in the DOM lib
 * and is absent in Safari and Firefox, so everything here is best effort and
 * every reader tolerates `undefined`.
 */
interface NetworkInformationLike {
  saveData?: boolean;
  downlink?: number;
}

export function networkInformation(): NetworkInformationLike | undefined {
  if (typeof navigator === "undefined") return undefined;
  return (navigator as Navigator & { connection?: NetworkInformationLike }).connection;
}

/**
 * Whether the browser asks pages to save data (Chromium's Data Saver, the
 * `Save-Data` request hint). Answers false where the API is absent.
 */
export function prefersReducedData(): boolean {
  return networkInformation()?.saveData === true;
}
