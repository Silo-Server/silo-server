import { networkInformation } from "@/player/network-information";

/**
 * Whether the browser asks pages to save data (Chromium's Data Saver, the
 * `Save-Data` request hint). Answers false where the API is absent.
 */
export function prefersReducedData(): boolean {
  return networkInformation()?.saveData === true;
}
