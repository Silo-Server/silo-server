/**
 * Coarse form factor from the user agent: the server uses it for quirk
 * lookups, and the speed test schedule uses it to tell a phone or tablet,
 * which may be on mobile data, from a desktop.
 */
export function detectFormFactor(): "tv" | "tablet" | "phone" | "desktop" {
  if (typeof navigator === "undefined") return "desktop";
  const ua = (navigator.userAgent ?? "").toLowerCase();
  if (/\b(smart-?tv|smarttv|googletv|appletv|hbbtv|netcast|webos|tizen)\b/.test(ua)) return "tv";
  if (/\b(ipad|tablet)\b/.test(ua)) return "tablet";
  if (/\b(iphone|ipod|android.*mobile|mobile)\b/.test(ua)) return "phone";
  return "desktop";
}
