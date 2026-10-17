/**
 * Project the bridge's redisplayable callback onto the accepted v2 delivery
 * route. Keep the configured host, deployment prefix and existing token.
 * This changes only the displayed/copied URL, never provider configuration.
 */
export function autoscanWebhookURL(value: string, origin: string): string {
  const url = new URL(value, origin);
  url.pathname = url.pathname.replace(
    /\/api\/v1\/autoscan\/webhooks\/([^/]+)$/,
    "/api/v2/autoscan/webhooks/$1",
  );
  return url.toString();
}

/** Resolve a possibly relative webhook_url against the admin UI's own origin. */
export function absoluteWebhookURL(url: string): string {
  return autoscanWebhookURL(url, window.location.origin);
}

const LOCAL_BASE_KEY = "autoscan_webhook_local_base";
const DELIVERY_PATH = /\/api\/v[12]\/autoscan\/webhooks\/[^/?#]+$/;

/**
 * Normalize an operator-typed local base ("192.168.1.10:8080", optionally with
 * a scheme or a deployment prefix) to an origin plus prefix without a trailing
 * slash. Returns "" when it is not a usable http(s) address.
 */
export function normalizeLocalWebhookBase(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "";
  try {
    const url = new URL(/^[a-z][a-z\d+.-]*:\/\//i.test(trimmed) ? trimmed : `http://${trimmed}`);
    if ((url.protocol !== "http:" && url.protocol !== "https:") || !url.hostname) return "";
    return `${url.origin}${url.pathname.replace(/\/+$/, "")}`;
  } catch {
    return "";
  }
}

/**
 * The same delivery endpoint addressed through a LAN base instead of the
 * server's public URL, for an arr that runs next to Silo and should not go out
 * through the reverse proxy. Only the base changes; the v2 route and the
 * secret are kept.
 */
export function localWebhookURL(url: string, base: string): string {
  const normalized = normalizeLocalWebhookBase(base);
  if (!url || !normalized) return "";
  const delivery = new URL(autoscanWebhookURL(url, "http://placeholder.invalid"));
  const match = DELIVERY_PATH.exec(delivery.pathname);
  if (!match) return "";
  return `${normalized}${match[0]}${delivery.search}`;
}

/** The local base this browser last used; the admin UI's own origin until edited. */
export function loadLocalWebhookBase(): string {
  try {
    const stored = localStorage.getItem(LOCAL_BASE_KEY);
    if (stored !== null) return stored;
  } catch {
    // localStorage can throw in private browsing — fall back to the origin.
  }
  return window.location.origin;
}

/** Remember the local base for this browser; a storage failure keeps it for this visit only. */
export function saveLocalWebhookBase(value: string): void {
  try {
    localStorage.setItem(LOCAL_BASE_KEY, value);
  } catch {
    // Ignore: the field still works for this visit.
  }
}
