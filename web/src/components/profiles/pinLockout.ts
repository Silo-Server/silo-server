import { V2ProblemError } from "@/api/v2/request";

/**
 * The message for a PIN check the server refused with 429 because the profile
 * is locked after too many wrong PINs, using `Retry-After` for the wait; null
 * for any other outcome.
 */
export function pinLockoutMessage(error: unknown): string | null {
  if (!(error instanceof V2ProblemError) || error.status !== 429) {
    return null;
  }
  const seconds = error.retryAfterSeconds;
  if (seconds === null) {
    return "Too many incorrect PINs. Try again later.";
  }
  const minutes = Math.max(1, Math.ceil(seconds / 60));
  return `Too many incorrect PINs. Try again in ${minutes} ${minutes === 1 ? "minute" : "minutes"}.`;
}
