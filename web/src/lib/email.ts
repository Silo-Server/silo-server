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
  if (domain.startsWith("[") && domain.endsWith("]")) return domain.length > 2;
  const dot = domain.lastIndexOf(".");
  return dot > 0 && dot < domain.length - 1;
}

/** The one message every email field shows for a malformed address. */
export const INVALID_EMAIL_MESSAGE = "Enter a valid email address, like name@example.com";
