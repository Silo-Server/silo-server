import type { Profile } from "@/api/types";

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
