import { adoptTabProfileSelection, hasTabProfileScope, storage } from "@/utils/storage";

/**
 * What this browser does about the profile when the app is launched in a new
 * tab or window: open the last profile chosen here ("remember"), or ask who is
 * watching ("ask").
 *
 * The choice belongs to the browser, not the account or a profile: it has to be
 * readable before any profile is chosen, and each device decides for itself.
 * So it lives in localStorage rather than in the server settings contract.
 *
 * In "ask" each new tab keeps its own profile choice (see the profile scope in
 * utils/storage), starting with none, so it asks without disturbing the
 * profile other open tabs are using.
 */
export type ProfileLaunchMode = "remember" | "ask";

export const DEFAULT_PROFILE_LAUNCH_MODE: ProfileLaunchMode = "remember";

export function getProfileLaunchMode(): ProfileLaunchMode {
  return storage.get(storage.KEYS.PROFILE_LAUNCH) === "ask" ? "ask" : DEFAULT_PROFILE_LAUNCH_MODE;
}

/**
 * "ask" takes effect in tabs opened from now on; the current tab keeps its
 * profile. "remember" makes this browser remember the current tab's profile.
 */
export function setProfileLaunchMode(mode: ProfileLaunchMode): void {
  storage.set(storage.KEYS.PROFILE_LAUNCH, mode);
  if (mode === "remember") adoptTabProfileSelection();
}

/**
 * True while this tab is waiting for someone to answer "Who's watching?", so
 * nothing picks a profile on their behalf (the sole-profile shortcut).
 */
export function isProfileLaunchPending(): boolean {
  return hasTabProfileScope() && storage.get(storage.KEYS.PROFILE_ID) === null;
}
