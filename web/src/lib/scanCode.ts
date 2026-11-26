/**
 * The scan code: an app code and the server address as text that only the
 * Silo app's own scanner reads, drawn as a QR code.
 *
 *   SILOSCANA <app code: 64 letters> <server address: two letters a byte>
 *
 * Each byte is written as two letters from A to P (0 to 15, the high half
 * first), so the text has no ":", ".", "/", "@" or digit and nothing takes it
 * for a URL, an email address or a phone number. A scan code is not a link:
 * no app opens it, and a camera or other reader shows plain text. That is why
 * it may carry a code whose answer said link_allowed: false, which a link
 * never may.
 */
const PREFIX = "SILOSCANA";

/** The longest server address a scan code carries, in UTF-8 bytes. */
export const MAX_SCAN_CODE_ADDRESS_BYTES = 128;

function letters(bytes: Uint8Array): string {
  let text = "";
  for (const byte of bytes) {
    text += String.fromCharCode(65 + (byte >> 4), 65 + (byte & 15));
  }
  return text;
}

/** An app code (43 characters of unpadded base64url) as its 32 bytes, or null. */
function appCodeBytes(code: string): Uint8Array | null {
  if (!/^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/.test(code)) return null;
  const binary = atob(code.replace(/-/g, "+").replace(/_/g, "/") + "=");
  return Uint8Array.from(binary, (c) => c.charCodeAt(0));
}

/**
 * Whether address is a server address in its one spelling, which the app's
 * scanner insists on: http or https, a lower-case host with no trailing dot,
 * no default port, no user, query, fragment or trailing slash. The server
 * answers in this spelling; anything else would draw a code the app refuses.
 */
function isServerAddress(address: string): boolean {
  let url: URL;
  try {
    url = new URL(address);
  } catch {
    return false;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return false;
  if (url.username || url.password || url.search || url.hash || url.hostname.endsWith(".")) {
    return false;
  }
  const path = url.pathname === "/" ? "" : url.pathname;
  return !path.endsWith("/") && `${url.origin}${path}` === address;
}

/**
 * The scan code for an app code and the server address the server answered
 * with it, or null when there is none: an address longer than 128 bytes (or
 * one not in its one spelling) has no scan code, and the page shows the
 * address and the code to type or copy instead.
 */
export function scanCodeText(code: string, serverAddress: string): string | null {
  const codeBytes = appCodeBytes(code);
  const addressBytes = new TextEncoder().encode(serverAddress);
  if (
    !codeBytes ||
    addressBytes.length > MAX_SCAN_CODE_ADDRESS_BYTES ||
    !isServerAddress(serverAddress)
  ) {
    return null;
  }
  return `${PREFIX} ${letters(codeBytes)} ${letters(addressBytes)}`;
}
