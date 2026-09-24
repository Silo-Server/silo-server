import { useOptionalAuth } from "@/hooks/useAuth";
import { useProfiles } from "@/hooks/queries/profiles";
import { storage } from "@/utils/storage";
import type { Profile } from "@/api/types";

export function resolveCurrentProfile(
  profiles: Profile[],
  cachedProfile: Profile | null,
  selectedProfileId?: string | null,
): Profile | null {
  const activeProfileId = selectedProfileId ?? cachedProfile?.id ?? null;
  if (activeProfileId) {
    const freshProfile = profiles.find((profile) => profile.id === activeProfileId);
    if (freshProfile) {
      return freshProfile;
    }
  }
  return cachedProfile;
}

export function useCurrentProfile() {
  // Optional so components that can render outside AuthProvider (e.g. via
  // useOptionalAuth) can still call hooks built on top of this one.
  const auth = useOptionalAuth();
  const cachedProfile = auth?.profile ?? null;
  // The always-mounted shell calls this before a session exists (at boot and
  // on the login screen), when the account's profile list can only answer 401.
  const profilesQuery = useProfiles({ enabled: Boolean(auth?.user) });
  const selectedProfileId = storage.get(storage.KEYS.PROFILE_ID);
  const profile = resolveCurrentProfile(profilesQuery.data ?? [], cachedProfile, selectedProfileId);

  // Return only the fields callers consume. Spreading the whole query result
  // would mark every query property as tracked, re-rendering every subscriber
  // (including the route gates that wrap whole pages) on each isFetching
  // toggle instead of only when the resolved profile changes.
  return {
    profile,
    // True when a profile is selected even if it hasn't resolved to a Profile
    // yet (e.g. hard refresh before the profiles query returns). Lets policy
    // code distinguish "no profile selected" from "selection unresolved".
    hasSelectedProfile: Boolean(selectedProfileId ?? cachedProfile),
    isLoading: profilesQuery.isLoading,
  };
}
