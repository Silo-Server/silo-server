/** Detects Firefox while excluding SeaMonkey's compatibility user-agent token. */
export function isFirefoxUserAgent(userAgent: string): boolean {
  return /firefox/i.test(userAgent) && !/seamonkey/i.test(userAgent);
}
