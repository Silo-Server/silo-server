// Shared pieces of the web's external sign-in (OIDC and LDAP) surfaces: the
// login page, the account Sign-in section and the device approval page.
// The rules behind them are in docs/architecture/external-sign-in.md.

// Text for the reason= codes an OAuth flow reports: on /login after a failed
// sign-in (error=oauth_failed) and on a linking flow's return path
// (error=oauth_link_failed). See internal/auth/oauth_handler.go.
const OAUTH_FAILURE_TEXT: Record<string, string> = {
  not_permitted: "Your account at the sign-in provider isn't allowed to use this server.",
  account_required: "You don't have an account on this server yet. Ask an admin to add you.",
  email_in_use:
    "An account with this email already exists. Ask an admin to connect it to the sign-in provider.",
  identity_linked_elsewhere: "That provider account is already connected to another account.",
  account_disabled: "This account is disabled.",
  provider_unavailable: "The sign-in provider can't be reached right now. Try again later.",
  state_invalid: "The sign-in didn't finish in this browser. Start again from this page.",
  session_expired: "The sign-in took too long. Start again from this page.",
  already_linked: "This account is already connected to the sign-in provider.",
};
const OAUTH_FAILURE_FALLBACK = "Sign-in with the provider failed. Try again.";

/** Readable text for an OAuth failure reason code; unknown codes get a generic line. */
export function oauthFailureText(reason: string | null | undefined): string {
  return (reason && OAUTH_FAILURE_TEXT[reason]) || OAUTH_FAILURE_FALLBACK;
}

// A linking flow's reasons, where the person was already signed in and
// asked to connect a provider; the rest read as they do on sign-in.
const OAUTH_LINK_FAILURE_TEXT: Record<string, string> = {
  identity_linked_elsewhere:
    "That provider account is already connected to another account on this server.",
  already_linked: "Your account is already connected to this sign-in provider.",
  state_invalid: "Connecting didn't finish in this browser. Try again from this page.",
  session_expired: "Connecting took too long. Try again from this page.",
};
const OAUTH_LINK_FAILURE_FALLBACK = "Connecting the sign-in provider failed. Try again.";

/** Readable text for a linking flow's failure reason (error=oauth_link_failed). */
export function oauthLinkFailureText(reason: string | null | undefined): string {
  if (reason && OAUTH_LINK_FAILURE_TEXT[reason]) return OAUTH_LINK_FAILURE_TEXT[reason];
  if (reason && OAUTH_FAILURE_TEXT[reason]) return OAUTH_FAILURE_TEXT[reason];
  return OAUTH_LINK_FAILURE_FALLBACK;
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
