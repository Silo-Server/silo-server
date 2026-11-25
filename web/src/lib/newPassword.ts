import { problemFieldDetail, V2ProblemError } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";
import { t } from "@/i18n";

/** The server's rules for a new password, as getPasswordPolicy reports them. */
export type PasswordPolicy = components["schemas"]["PasswordPolicy"];

/** The policy of a server with no policy settings, used until the real one loads. */
export const DEFAULT_PASSWORD_POLICY: PasswordPolicy = {
  minimum_length: 8,
  maximum_bytes: 72,
  require_uppercase: false,
  require_special: false,
};

/** The reason a new password cannot be submitted, or null. Mirrors the
 * server's checks (auth.PasswordPolicy), which stay the authority. */
export function newPasswordProblem(
  password: string,
  confirmation: string,
  policy: PasswordPolicy = DEFAULT_PASSWORD_POLICY,
): string | null {
  if (password !== confirmation) return t("web.auth.newPassword.mismatch");
  return newPasswordPolicyProblem(password, policy);
}

/** The first rule of the server's password policy a password breaks, or null. */
export function newPasswordPolicyProblem(
  password: string,
  policy: PasswordPolicy = DEFAULT_PASSWORD_POLICY,
): string | null {
  const minimum = policy.minimum_length;
  if ([...password].length < minimum) {
    return t("web.auth.newPassword.tooShortMinimum", { minimum, minimum_text: minimum });
  }
  if (new TextEncoder().encode(password).length > policy.maximum_bytes) {
    return t("web.auth.newPassword.tooLong");
  }
  if (policy.require_uppercase && !/\p{Lu}/u.test(password)) {
    return t("web.auth.newPassword.needsUppercase");
  }
  if (policy.require_special && !/[\p{P}\p{S}]/u.test(password)) {
    return t("web.auth.newPassword.needsSpecial");
  }
  return null;
}

/** The policy's character rules as one hint for under a new-password field,
 * or "" when it has none. Length limits are each form's own hint. */
export function newPasswordRuleHint(policy: PasswordPolicy): string {
  return [
    policy.require_uppercase ? t("web.auth.newPassword.needsUppercase") : null,
    policy.require_special ? t("web.auth.newPassword.needsSpecial") : null,
  ]
    .filter(Boolean)
    .join(" ");
}

/** The hint under a new-password field: the minimum length, then the
 * policy's character rules. */
export function newPasswordHint(policy: PasswordPolicy): string {
  const minimum = policy.minimum_length;
  const length = t("web.auth.newPassword.minLengthHint", { minimum, minimum_text: minimum });
  const rules = newPasswordRuleHint(policy);
  return rules ? `${length} ${rules}` : length;
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
