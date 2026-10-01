import { V2ProblemError } from "@/api/v2/request";

/** The reason a new password cannot be submitted, or null. Mirrors the
 * server's limits: at least 8 characters, at most 72 UTF-8 bytes. */
export function newPasswordProblem(password: string, confirmation: string): string | null {
  if (password !== confirmation) return "Passwords do not match.";
  if ([...password].length < 8) return "Use at least 8 characters.";
  if (new TextEncoder().encode(password).length > 72) return "Use no more than 72 bytes.";
  return null;
}

/** A readable message for a failed password write: a validation problem's
 * first field detail rather than its generic summary. */
export function passwordErrorMessage(err: unknown, fallback: string): string {
  if (err instanceof V2ProblemError) return err.problem.errors?.[0]?.detail ?? err.message;
  return err instanceof Error ? err.message : fallback;
}
