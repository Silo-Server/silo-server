/** Detects Firefox while excluding SeaMonkey's compatibility user-agent token. */
export function isFirefoxUserAgent(userAgent: string): boolean {
  return /firefox/i.test(userAgent) && !/seamonkey/i.test(userAgent);
}

/**
 * Detects a Chromium media stack: Chrome, Edge, Opera and other Chromium
 * browsers on desktop and Android. Their iOS builds (CriOS, EdgiOS, OPiOS) run
 * WebKit's media loader, so they are excluded.
 */
export function isChromiumUserAgent(userAgent: string): boolean {
  return /(?:Chrome|Chromium)\//.test(userAgent) && !/(?:CriOS|EdgiOS|OPiOS)\//.test(userAgent);
}
