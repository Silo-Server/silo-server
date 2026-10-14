import { storage } from "@/utils/storage";

/**
 * Whether this browser decodes media itself when the browser's own player
 * cannot (Dolby TrueHD and DTS audio, HDR on SDR screens, Matroska in Firefox).
 *
 * The choice is per browser, like volume: it describes what this browser and
 * GPU can do, not the viewer's account. Off by default while the engine is in
 * beta.
 */
export function isBrowserDecodingEnabled(): boolean {
  return storage.get(storage.KEYS.BROWSER_DECODING) === "true";
}

export function setBrowserDecodingEnabled(enabled: boolean): void {
  storage.set(storage.KEYS.BROWSER_DECODING, enabled ? "true" : "false");
}
