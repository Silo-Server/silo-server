import type { Profile } from "@/api/types";
import type { AccountIdentity } from "@/hooks/queries/account";
import type { AuthProviderOption } from "@/hooks/useAuth";

export const SILO_APP_EXAMPLES = "Silo for iPhone, Apple TV, Android, Android TV, and this website";

export const JELLYFIN_APP_EXAMPLES = "Infuse, Swiftfin, JellyCon, Findroid, Jellyfin Media Player";

/**
 * Builds the username a Jellyfin-protocol client must send.
 *
 * The compat resolver splits the username at the last `#` and matches the
 * suffix against profile names case-insensitively, so the profile name is
 * appended verbatim — see internal/jellycompat/login.go.
 */
export function buildJellyfinUsername(accountUsername: string, profileName: string): string {
  const account = accountUsername.trim();
  const profile = profileName.trim();
  if (account === "" || profile === "") {
    return account;
  }
  return `${account}#${profile}`;
}

/**
 * Reports why a profile can't be reached over the compat API, or null if it can.
 *
 * Profile names allow `#` (they're only checked for uniqueness), but the compat
 * resolver splits the username at the *last* `#`, so a name containing one is
 * unreachable: "alice#Movie #2" parses as account "alice#Movie", profile "2".
 * Saying so beats printing a username that cannot work.
 */
export function jellyfinUsernameIssue(profileName: string): string | null {
  if (profileName.includes("#")) {
    return "This profile's name contains a #, which Jellyfin apps can't sign in with. Rename it in Settings → Profiles to use it from one.";
  }
  return null;
}

/**
 * Reports whether a compat address is only reachable from the server itself.
 *
 * `jellyfin_compat.public_url` defaults to http://127.0.0.1:8096, and on the
 * phones and TVs this page names, a loopback address resolves to the client
 * device. Presenting it as the server address sends people chasing a broken
 * connection instead of an unset setting.
 */
export function isLoopbackURL(rawURL: string): boolean {
  let host: string;
  try {
    host = new URL(rawURL).hostname.toLowerCase();
  } catch {
    return false;
  }
  // Strip the brackets URL parsing keeps around IPv6 literals.
  host = host.replace(/^\[|\]$/g, "");
  return (
    host === "localhost" ||
    host === "::1" ||
    host === "0.0.0.0" ||
    host.endsWith(".localhost") ||
    /^127\./.test(host)
  );
}

/** Explains the password format for the selected profile, PIN or not. */
export function buildJellyfinPasswordHint(
  profile: Pick<Profile, "name" | "has_pin"> | null,
): string {
  if (!profile) {
    return "Your account password.";
  }
  return profile.has_pin
    ? `${profile.name} has a PIN, so append # and the PIN to your account password.`
    : `${profile.name} has no PIN — just your account password, nothing appended.`;
}

/**
 * How this account signs in on a Silo app's sign-in screen.
 *
 * - `password`: the username and Silo password form.
 * - `directory`: the same form, with the linked directory's (LDAP) username
 *   and password; the server routes a name without a Silo password there.
 * - `provider`: the linked OAuth (OIDC) provider's button, which opens the
 *   provider in a browser. TVs show a code to approve instead.
 * - `network`: the linked network provider's (such as Tailscale) "Continue
 *   as" button, offered only on a device reaching the server through it.
 * - `none`: nothing the sign-in screens offer works for this account.
 */
export type SiloSignIn =
  | { kind: "password" }
  | { kind: "directory"; provider: AuthProviderOption; username: string }
  | { kind: "provider"; provider: AuthProviderOption }
  | { kind: "network"; providerName: string }
  | { kind: "none" };

/**
 * Works out what a Silo sign-in screen lets this account use.
 *
 * The account's password counts only while the server's sign-in screens show
 * the password form (`passwordFormShown`, listAuthProviders' password_login):
 * with local sign-in off, a break-glass password still works for recovery
 * and Jellyfin apps, but the Silo apps hide the form. Otherwise the account
 * signs in through a linked identity whose provider is enabled; an identity
 * at a disabled provider has an empty provider_id. A network provider is
 * listed only to requests that come through its own network, so an enabled
 * provider missing from the list is one of those.
 */
export function resolveSiloSignIn(
  accountPasswordWorks: boolean,
  passwordFormShown: boolean,
  providers: AuthProviderOption[],
  identities: AccountIdentity[],
): SiloSignIn {
  if (accountPasswordWorks && passwordFormShown) {
    return { kind: "password" };
  }
  let network: SiloSignIn | null = null;
  for (const identity of identities) {
    if (!identity.provider_id) continue;
    const provider = providers.find((entry) => entry.id === identity.provider_id);
    if (provider?.mode === "oauth") {
      return { kind: "provider", provider };
    }
    if (provider?.mode === "credentials" && passwordFormShown) {
      return { kind: "directory", provider, username: identity.username };
    }
    if (!provider || provider.mode === "network") {
      network ??= {
        kind: "network",
        providerName: provider?.display_name ?? identity.provider_name,
      };
    }
  }
  return network ?? { kind: "none" };
}
