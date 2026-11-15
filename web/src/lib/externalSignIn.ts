// Shared pieces of the web's external sign-in (OIDC and LDAP) surfaces: the
// login page, the account Sign-in section and the device approval page.
// The rules behind them are in docs/architecture/external-sign-in.md.

import { t, type MessageKey } from "@/i18n";

// Text for the reason= codes an OAuth flow reports: on /login after a failed
// sign-in (error=oauth_failed) and on a linking flow's return path
// (error=oauth_link_failed). See internal/auth/oauth_handler.go.
const OAUTH_FAILURE_TEXT: Record<string, MessageKey> = {
  not_permitted: "web.auth.oauthFailure.notPermitted",
  account_required: "web.auth.oauthFailure.accountRequired",
  email_in_use: "web.auth.oauthFailure.emailInUse",
  identity_linked_elsewhere: "web.auth.oauthFailure.identityLinkedElsewhere",
  account_disabled: "web.auth.oauthFailure.accountDisabled",
  provider_unavailable: "web.auth.oauthFailure.providerUnavailable",
  state_invalid: "web.auth.oauthFailure.stateInvalid",
  session_expired: "web.auth.oauthFailure.sessionExpired",
  already_linked: "web.auth.oauthFailure.alreadyLinked",
};

/** Readable text for an OAuth failure reason code; unknown codes get a generic line. */
export function oauthFailureText(reason: string | null | undefined): string {
  return t((reason && OAUTH_FAILURE_TEXT[reason]) || "web.auth.oauthFailure.fallback");
}

// A linking flow's reasons, where the person was already signed in and
// asked to connect a provider; the rest read as they do on sign-in.
const OAUTH_LINK_FAILURE_TEXT: Record<string, MessageKey> = {
  identity_linked_elsewhere: "web.auth.oauthLinkFailure.identityLinkedElsewhere",
  already_linked: "web.auth.oauthLinkFailure.alreadyLinked",
  state_invalid: "web.auth.oauthLinkFailure.stateInvalid",
  session_expired: "web.auth.oauthLinkFailure.sessionExpired",
};

/**
 * Who a network provider (such as Tailscale) says owns this device, for a
 * "Continue as" label; empty when it named nobody.
 */
export function networkIdentityName(provider: {
  network_identity?: { display_name: string; username: string };
}): string {
  return provider.network_identity?.display_name || provider.network_identity?.username || "";
}

/**
 * Readable text for a refused network identity sign-in or link (a provider
 * such as Tailscale that knows who owns the device); null for a problem the
 * caller words itself.
 */
export function networkSignInRefusalText(problemType: string, providerName: string): string | null {
  switch (problemType) {
    case "network_identity_required":
      return t("web.auth.networkSignIn.addressRequired", { provider: providerName });
    case "not_permitted":
      return t("web.auth.networkSignIn.notPermitted", { provider: providerName });
    case "email_in_use":
      return t("web.auth.networkSignIn.emailInUse", { provider: providerName });
    case "permission_denied":
      return oauthFailureText("account_disabled");
    case "account_required":
    case "identity_linked_elsewhere":
    case "provider_unavailable":
      return oauthFailureText(problemType);
    default:
      return null;
  }
}

/** Readable text for a linking flow's failure reason (error=oauth_link_failed). */
export function oauthLinkFailureText(reason: string | null | undefined): string {
  if (reason && OAUTH_LINK_FAILURE_TEXT[reason]) return t(OAUTH_LINK_FAILURE_TEXT[reason]);
  if (reason && OAUTH_FAILURE_TEXT[reason]) return t(OAUTH_FAILURE_TEXT[reason]);
  return t("web.auth.oauthLinkFailure.fallback");
}

/**
 * The web start of an OAuth sign-in (startOAuthLogin). It is a link, not a
 * form post: the page's CSP has form-action 'self', which browsers apply to
 * the redirect to the provider, so a posted form never leaves Silo.
 * selectAccount asks the provider to let the person choose another provider
 * account, for a "Switch account" sign-in.
 */
export function oauthStartHref(
  installationId: string | number,
  options: { next?: string | null; selectAccount?: boolean } = {},
): string {
  const query = new URLSearchParams();
  if (options.next) query.set("next", options.next);
  if (options.selectAccount) query.set("prompt", "select_account");
  const search = query.toString();
  return `/api/v2/auth/oauth/${encodeURIComponent(String(installationId))}/start${search ? `?${search}` : ""}`;
}

/** Sends the browser to another page (a provider, or a server start). */
export function leaveForProvider(url: string): void {
  window.location.assign(url);
}

// Set when someone signs out in this tab, so the login page does not send
// them straight back to a provider that still has its own session open;
// that would sign them in again at once. Cleared by the next sign-in.
const SIGNED_OUT_KEY = "silo.auth.signedOut";
const SESSION_END_KEY = "silo.auth.sessionEnd";

// Why the last session ended, for the sign-in page's notice. Only a
// deliberate sign-out also blocks the provider redirect: a session that
// expired or was revoked elsewhere goes back through single sign-on as before.
export function markSessionEnded(reason: "ended" | "signed-out"): void {
  if (reason === "signed-out") markSignedOut();
  try {
    window.sessionStorage.setItem(SESSION_END_KEY, reason);
  } catch {
    // The sign-in form remains usable without storage.
  }
}

export function sessionEndReason(): "ended" | "signed-out" | null {
  try {
    const reason = window.sessionStorage.getItem(SESSION_END_KEY);
    return reason === "ended" || reason === "signed-out" ? reason : null;
  } catch {
    return null;
  }
}

export function markSignedOut(): void {
  try {
    window.sessionStorage.setItem(SIGNED_OUT_KEY, "1");
  } catch {
    // Storage off: the login page may auto-redirect once more.
  }
}

export function clearSignedOut(): void {
  try {
    window.sessionStorage.removeItem(SIGNED_OUT_KEY);
    window.sessionStorage.removeItem(SESSION_END_KEY);
  } catch {
    // Nothing to clear.
  }
}

export function wasSignedOut(): boolean {
  try {
    return window.sessionStorage.getItem(SIGNED_OUT_KEY) === "1";
  } catch {
    return false;
  }
}
