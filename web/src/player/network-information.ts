/**
 * The slice of the Network Information API we use. It is not in the DOM lib
 * and is absent in Safari and Firefox, and desktop Chromium has no `type`, so
 * everything here is best effort and every reader tolerates `undefined`.
 */
export interface NetworkInformationLike {
  saveData?: boolean;
  type?: string;
}

export function networkInformation(): NetworkInformationLike | undefined {
  if (typeof navigator === "undefined") return undefined;
  return (navigator as Navigator & { connection?: NetworkInformationLike }).connection;
}

/** The browser's connection type, or "unknown" where it cannot report one. */
export function currentNetwork(): string {
  return networkInformation()?.type ?? "unknown";
}
