import { problemFieldDetail, V2ProblemError } from "@/api/v2/request";
import { t } from "@/i18n";

/** The reason a new password cannot be submitted, or null. Mirrors the
 * server's limits: at least 8 characters, at most 72 UTF-8 bytes. */
export function newPasswordProblem(password: string, confirmation: string): string | null {
  if (password !== confirmation) return t("web.auth.newPassword.mismatch");
  if ([...password].length < 8) return t("web.auth.newPassword.tooShort");
  if (new TextEncoder().encode(password).length > 72) return t("web.auth.newPassword.tooLong");
  return null;
}

/** A readable message for a failed password write: a validation problem's
 * first field detail rather than its generic summary. */
export function passwordErrorMessage(err: unknown, fallback: string): string {
  if (err instanceof V2ProblemError) {
    const field = err.problem.errors?.[0];
    return field ? problemFieldDetail(field) : err.message;
  }
  return err instanceof Error ? err.message : fallback;
}
