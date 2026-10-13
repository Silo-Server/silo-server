// Text for what the server answers the admin external sign-in controls: the
// Sign-in settings page and an account's Sign-in tab. The refusals are listed
// in docs/auth-api.md#external-sign-in.
import type { PluginAuthBinding, PluginCapability, PluginInstallation } from "@/api/types";
import type { SignInBindingWrite } from "@/hooks/queries/admin/externalSignIn";
import { StaleApiRequestContextError } from "@/api/client";
import { V2ProblemError } from "@/api/v2/request";

export const BREAK_GLASS_REQUIRED_TEXT =
  "Password sign-in is off for this server, and this would leave no break-glass admin who can still sign in with a password. Turn password sign-in back on in Settings → Sign-in first, or make another admin break-glass.";

const PROBLEM_TEXT: Record<string, string> = {
  provider_already_enabled:
    "Another sign-in provider is already on. A server has one sign-in provider at a time, plus one network sign-in such as Tailscale: turn the other one off first.",
  break_glass_required: BREAK_GLASS_REQUIRED_TEXT,
  identity_linked_elsewhere:
    "That provider account is already connected to another Silo account. Unlink it there first.",
  last_sign_in_method: "This would leave the account with no way to sign in.",
};

/**
 * Readable text for a failed admin sign-in action. Known problem types get
 * their own line; anything else keeps the server's message, which names the
 * refused field or rule.
 */
export function adminSignInErrorText(error: unknown, fallback: string): string {
  if (error instanceof StaleApiRequestContextError) {
    return "Select an administrator profile, then try again.";
  }
  if (!(error instanceof V2ProblemError)) {
    return "Couldn't reach the server. Try again.";
  }
  const known = PROBLEM_TEXT[error.problemType];
  if (known) return known;
  if (error.status === 403 && error.problemType === "permission_denied") {
    return "Only the server owner can change another admin's sign-in.";
  }
  if (error.status === 412) {
    return "The account changed while you were editing it. Reload the page and try again.";
  }
  if (error.status === 503) {
    return "External sign-in isn't available on this server right now.";
  }
  return error.message || fallback;
}

/** The auth_provider.v1 capability an installed plugin offers, if any. */
export function authCapabilityOf(installation: PluginInstallation): PluginCapability | undefined {
  return (installation.capabilities ?? []).find(
    (capability) => capability.type === "auth_provider.v1",
  );
}

/** Installed plugins that can sign people in. */
export function authPluginInstallations(
  installations: readonly PluginInstallation[] | undefined,
): PluginInstallation[] {
  return (installations ?? []).filter((installation) => authCapabilityOf(installation));
}

/**
 * Whether an installation signs people in from its network (such as the
 * Tailscale plugin) rather than as the server's one OIDC or LDAP provider.
 * One network sign-in can be on beside that provider.
 */
export function isNetworkSignIn(installation: PluginInstallation): boolean {
  return authCapabilityOf(installation)?.sign_in_mode === "network";
}

/** Installed OIDC and LDAP sign-in plugins: the one-at-a-time provider slot. */
export function primarySignInInstallations(
  installations: readonly PluginInstallation[] | undefined,
): PluginInstallation[] {
  return authPluginInstallations(installations).filter(
    (installation) => !isNetworkSignIn(installation),
  );
}

/** Installed network sign-in plugins (such as Tailscale). */
export function networkSignInInstallations(
  installations: readonly PluginInstallation[] | undefined,
): PluginInstallation[] {
  return authPluginInstallations(installations).filter(isNetworkSignIn);
}

/** The binding row of an installation's sign-in capability; none on a fresh install. */
export function authBindingOf(installation: PluginInstallation): PluginAuthBinding | undefined {
  const capability = authCapabilityOf(installation);
  return installation.auth_bindings?.find((entry) => entry.capability_id === capability?.id);
}

/** Whether the installation creates accounts for people it signs in; on until saved off. */
export function savedAutoProvision(installation: PluginInstallation): boolean {
  return authBindingOf(installation)?.auto_provision ?? true;
}

/**
 * The binding write for an installation: its saved binding (or a fresh
 * install's defaults) with change applied.
 */
export function signInBindingWrite(
  installation: PluginInstallation,
  change: Partial<Omit<SignInBindingWrite, "capability_id">> = {},
): SignInBindingWrite {
  const binding = authBindingOf(installation);
  return {
    capability_id: authCapabilityOf(installation)!.id,
    enabled: binding?.enabled ?? false,
    display_order: binding?.display_order ?? 1,
    auto_provision: savedAutoProvision(installation),
    default_login: binding?.default_login ?? false,
    ...change,
  };
}

/**
 * The OIDC or LDAP installation whose binding is on: a server has at most
 * one. A network sign-in beside it is not counted: it signs in only people
 * on its network.
 */
export function activeSignInInstallation(
  installations: readonly PluginInstallation[] | undefined,
): PluginInstallation | undefined {
  return primarySignInInstallations(installations).find(
    (installation) => authBindingOf(installation)?.enabled === true,
  );
}

/** The name an admin knows a sign-in plugin by. */
export function authProviderName(installation: PluginInstallation): string {
  const capability = authCapabilityOf(installation);
  return (
    installation.presentation?.display_name?.trim() ||
    capability?.display_name?.trim() ||
    installation.plugin_id
  );
}

/**
 * The name people know the provider by: the label its sign-in button shows
 * (the saved display_name setting, else the capability's), as the server
 * resolves it for the login page. Falls back to the capability's name, then
 * the plugin's.
 */
export function authProviderLabel(installation: PluginInstallation): string {
  const saved = installation.global_configs?.find((entry) => entry.key === "display_name")?.value
    ?.value;
  if (typeof saved === "string" && saved.trim()) return saved.trim();
  const capability = authCapabilityOf(installation);
  const declared = capability?.metadata?.display_name;
  if (typeof declared === "string" && declared.trim()) return declared.trim();
  return capability?.display_name?.trim() || authProviderName(installation);
}

/** What the last provider re-check said about an identity, in words. */
export function identityCheckStatusText(status: string): string {
  switch (status) {
    case "active":
      return "Active at the provider";
    case "not_found":
      return "Not found at the provider (signed out)";
    case "disabled":
      return "Disabled at the provider (signed out)";
    case "not_permitted":
      return "No longer allowed by the provider (signed out)";
    case "unsupported":
      return "The provider can't re-check this account: sessions end at a fixed age, and its API keys and Audiobookshelf sessions are removed once the person hasn't signed in with the provider for that long";
    case "unavailable":
      return "The provider couldn't be reached; Silo asks again at the next refresh";
    case "none":
      return "Not checked yet";
    default:
      return status;
  }
}
