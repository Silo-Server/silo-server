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
