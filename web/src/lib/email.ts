/**
 * Whether `value` looks like one real mailbox: something before a single "@",
 * and a domain with a dot that has text on both sides. This mirrors the
 * server's rule (internal/auth.ValidateEmail) closely enough to catch the
 * mistakes people actually make, like "admin@siloserver", before a round
 * trip; the server remains the authority.
 */
export function isValidEmail(value: string): boolean {
  const trimmed = value.trim();
  const at = trimmed.lastIndexOf("@");
  if (at <= 0 || at !== trimmed.indexOf("@") || /\s/.test(trimmed)) return false;
  const domain = trimmed.slice(at + 1);
  if (domain.startsWith("[")) return isDomainLiteral(domain);
  const dot = domain.lastIndexOf(".");
  return dot > 0 && dot < domain.length - 1;
}

const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
const IPV6 = /^[0-9a-fA-F:]*:[0-9a-fA-F:.]*$/;

/**
 * A bracketed address literal as Go's net/mail accepts it: a dotted IPv4 or a
 * bare IPv6 address. Anything else in brackets (including the RFC "IPv6:" tag,
 * which net/mail refuses) is rejected so the client never passes an address
 * the server will bounce.
 */
function isDomainLiteral(domain: string): boolean {
  if (!domain.endsWith("]")) return false;
  const inner = domain.slice(1, -1);
  return IPV4.test(inner) || (inner.length > 1 && IPV6.test(inner));
}

/** The one message every email field shows for a malformed address. */
export const INVALID_EMAIL_MESSAGE = "Enter a valid email address, like name@example.com";
