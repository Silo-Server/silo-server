/**
 * Deep links into the native Silo apps via the silo:// custom scheme.
 *
 * Silo is self-hosted, so the store apps cannot pre-verify every server's
 * domain for App Links / Universal Links; a custom scheme is the only
 * universal way in. The Android app already registers
 * `silo://invite?server=<url>&token=<token>` (see silo-android
 * InviteClaimRouteParser.kt and its navDeepLink) — this module emits that
 * exact contract, with `server` carrying the full origin so non-443 ports
 * and plain-http LAN servers need no extra convention.
 *
 * Custom-scheme URLs don't linkify in email or SMS and error when the app
 * is missing, so they are never sent anywhere: they only back an explicit
 * in-page button, rendered on platforms with a native app.
 */

export type MobilePlatform = "android" | "ios";

/** Detects a platform with a native Silo app from the user agent. */
export function detectMobilePlatform(ua: string): MobilePlatform | null {
  // iPadOS 13+ Safari masquerades as macOS; maxTouchPoints tells it apart,
  // but that's a live-DOM concern — callers pass a UA and we keep this pure.
  if (/android/i.test(ua)) return "android";
  if (/iphone|ipad|ipod/i.test(ua)) return "ios";
  return null;
}

/**
 * Builds the silo:// deep link that opens the native invite claim flow.
 * Returns null for origins the apps can't talk to (non-http(s), userinfo).
 */
export function buildInviteDeepLink(pageOrigin: string, token: string): string | null {
  let origin: URL;
  try {
    origin = new URL(pageOrigin);
  } catch {
    return null;
  }
  if (origin.username || origin.password) return null;
  if (origin.protocol !== "https:" && origin.protocol !== "http:") return null;
  const server = encodeURIComponent(origin.origin);
  return `silo://invite?server=${server}&token=${encodeURIComponent(token)}`;
}
