/**
 * The scan code: an app code and the server address as text, drawn as a QR
 * code. Any QR reader can read it; only the Silo app acts on it.
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

/** The scan code text, or why there is none. */
export type ScanCode =
  | { text: string }
  | {
      text: null;
      /**
       * too_long: the server address is over 128 bytes. not_drawable: the code
       * or the address isn't in the form the app's scanner reads, so a scan
       * code would be refused.
       */
      reason: "too_long" | "not_drawable";
    };

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

// The server address rule the app's scanner applies, checked by hand rather
// than with the URL parser, which rewrites hosts and paths instead of refusing
// them. The server builds its addresses to a stricter rule, so every address
// it answers with passes.
const URL_PARTS = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/?#]*)([^?#]*)(\?[^#]*)?(#.*)?$/su;
/** A host label: 1 to 63 of a-z, 0-9, "-" and "_", in any position. */
const HOST_LABEL = /^[a-z0-9_-]{1,63}$/;
/** A last label a URL parser reads as a number: digits, or 0x and hex digits. */
const NUMERIC_LABEL = /^(?:[0-9]+|0x[0-9a-f]*)$/;
const DEC_OCTET = /^(?:0|[1-9][0-9]{0,2})$/;
const PORT = /^[1-9][0-9]{0,4}$/;
/** A path segment: unreserved, sub-delims, ":" and "@", or an upper-case escape. */
const PATH_SEGMENT = /^(?:[A-Za-z0-9\-._~!$&'()*+,;=:@]|%[0-9A-F]{2})+$/;
const DEFAULT_PORTS: Record<string, string> = { http: "80", https: "443" };

function isIpv4(host: string): boolean {
  const parts = host.split(".");
  return parts.length === 4 && parts.every((p) => DEC_OCTET.test(p) && Number(p) <= 255);
}

/**
 * Whether text (without brackets) is an IPv6 literal in RFC 5952 form. An
 * IPv4-mapped address, a zone and a dotted IPv4 tail are refused: platforms
 * write them differently.
 */
function isIpv6(text: string): boolean {
  if (!/^[0-9a-f:]+$/.test(text)) return false;
  const halves = text.split("::");
  if (halves.length > 2) return false;
  const groupsOf = (part: string) => (part === "" ? [] : part.split(":"));
  const head = groupsOf(halves[0]!);
  const tail = halves.length === 2 ? groupsOf(halves[1]!) : [];
  if (![...head, ...tail].every((g) => /^[0-9a-f]{1,4}$/.test(g))) return false;
  const missing = 8 - head.length - tail.length;
  if (halves.length === 2 ? missing < 1 : missing !== 0) return false;
  const groups = [...head, ...Array<string>(missing).fill("0"), ...tail].map((g) =>
    Number.parseInt(g, 16),
  );
  if (groups.slice(0, 5).every((g) => g === 0) && groups[5] === 0xffff) return false;
  // RFC 5952: compress the longest run of two or more zero groups, the first on a tie.
  let bestStart = -1;
  let bestLength = 1;
  for (let i = 0; i < 8; ) {
    if (groups[i] !== 0) {
      i++;
      continue;
    }
    let j = i;
    while (j < 8 && groups[j] === 0) j++;
    if (j - i > bestLength) [bestStart, bestLength] = [i, j - i];
    i = j;
  }
  const hex = groups.map((g) => g.toString(16));
  const canonical =
    bestStart < 0
      ? hex.join(":")
      : `${hex.slice(0, bestStart).join(":")}::${hex.slice(bestStart + bestLength).join(":")}`;
  return canonical === text;
}

/** Whether host is a lower-case ASCII host name or IPv4 address in its one spelling. */
function isHostName(host: string): boolean {
  if (host.length === 0 || host.length > 253) return false;
  const labels = host.split(".");
  if (!labels.every((label) => HOST_LABEL.test(label))) return false;
  return !NUMERIC_LABEL.test(labels[labels.length - 1]!) || isIpv4(host);
}

/**
 * Whether address is a server address in its one spelling: http or https in
 * lower case; a lower-case host (labels of a-z, 0-9, "-" and "_", an IPv4
 * address of four decimal numbers, or an IPv6 literal in RFC 5952 form); a
 * port only when it isn't the default; no user, query or fragment; and a path
 * with no empty, "." or ".." segment, no trailing slash and nothing a path
 * can't hold unescaped.
 */
function isServerAddress(address: string): boolean {
  const m = URL_PARTS.exec(address);
  if (!m || m[4] !== undefined || m[5] !== undefined) return false;
  const [, scheme, authority, path] = m as unknown as [string, string, string, string];
  if ((scheme !== "http" && scheme !== "https") || authority.includes("@")) return false;
  let host = authority;
  // Null when no ":" follows the host; "" (an empty port) is refused.
  let port: string | null = null;
  if (authority.startsWith("[")) {
    const close = authority.indexOf("]");
    if (close < 0) return false;
    host = authority.slice(0, close + 1);
    const rest = authority.slice(close + 1);
    if (rest !== "") {
      if (!rest.startsWith(":")) return false;
      port = rest.slice(1);
    }
    if (!isIpv6(host.slice(1, -1))) return false;
  } else {
    const colon = authority.indexOf(":");
    if (colon >= 0) [host, port] = [authority.slice(0, colon), authority.slice(colon + 1)];
    if (!isHostName(host)) return false;
  }
  if (
    port !== null &&
    (!PORT.test(port) || Number(port) > 65535 || port === DEFAULT_PORTS[scheme])
  ) {
    return false;
  }
  if (path === "") return true;
  if (path.endsWith("/")) return false;
  return path
    .split("/")
    .slice(1)
    .every((segment) => PATH_SEGMENT.test(segment) && !/^(?:\.|%2E){1,2}$/i.test(segment));
}

/**
 * The scan code for an app code and the server address the server answered
 * with it, or why there is none. An address over 128 bytes has no scan code;
 * the page then shows the address and the code to type or copy.
 */
export function scanCode(code: string, serverAddress: string): ScanCode {
  const addressBytes = new TextEncoder().encode(serverAddress);
  if (addressBytes.length > MAX_SCAN_CODE_ADDRESS_BYTES) {
    return { text: null, reason: "too_long" };
  }
  const codeBytes = appCodeBytes(code);
  if (!codeBytes || !isServerAddress(serverAddress)) {
    return { text: null, reason: "not_drawable" };
  }
  return { text: `${PREFIX} ${letters(codeBytes)} ${letters(addressBytes)}` };
}

/** Text bytes a QR code holds in byte mode at level L, by version (1 to 12). */
const BYTE_CAPACITY_L = [17, 32, 53, 78, 106, 134, 154, 192, 230, 271, 321, 367];

/** The smallest a drawn QR code is, and the least width of one module, in CSS pixels. */
export const MIN_QR_PIXELS = 176;
export const MIN_MODULE_PIXELS = 3.5;
/** The white quiet zone a QR code needs on every side, in modules. */
export const QUIET_ZONE_MODULES = 4;

/**
 * How to draw a scan code's QR code, in CSS pixels: the code's width, and the
 * white margin on every side. The QR library encodes in byte mode at level L,
 * so a longer address needs a higher version with more modules; the code
 * grows so that no module is under 3.5 px, and the margin is four modules of
 * that size (in dark mode it is the only white around the code). The longest
 * scan code (331 bytes) is version 12, 65 modules: 228 px with a 15 px margin,
 * a 258 px box.
 */
export function scanCodeLayout(text: string): { pixels: number; margin: number } {
  const version = BYTE_CAPACITY_L.findIndex((capacity) => text.length <= capacity) + 1;
  const modules = 17 + 4 * (version || BYTE_CAPACITY_L.length);
  const pixels = Math.max(MIN_QR_PIXELS, Math.ceil(modules * MIN_MODULE_PIXELS));
  return { pixels, margin: Math.ceil((QUIET_ZONE_MODULES * pixels) / modules) };
}
