import { V2ProblemError } from "@/api/v2/request";
import { t } from "@/i18n";

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
    return t("web.profiles.pin.lockedLater");
  }
  const minutes = Math.max(1, Math.ceil(seconds / 60));
  return t("web.profiles.pin.lockedMinutes", { count: minutes, count_text: minutes });
}
