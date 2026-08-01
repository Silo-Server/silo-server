import { useCallback } from "react";

import { useSettingValue, useSetSettingValue } from "@/hooks/queries/settingValues";
import type { SettingIdentity } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";

/** Coarse search scope: "video" = movies & series, "audiobook" = books. */
export type SearchMediaScope = "all" | "video" | "audiobook";

export const DEFAULT_SEARCH_MEDIA_SCOPE: SearchMediaScope = "video";

/** `search.media_scope` is profile-wide in the contract (no device scope). */
const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };

export function parseSearchMediaScope(value: string | null | undefined): SearchMediaScope | null {
  return value === "all" || value === "video" || value === "audiobook" ? value : null;
}

/**
 * Server-persisted preference for the default search scope. Search entry
 * points (global search, the catalog search page) apply this scope when the
 * URL doesn't carry an explicit `type` param, and the scope chips write back
 * to it so the choice sticks across sessions and devices.
 */
export function useSearchMediaScope() {
  const { value } = useSettingValue<string>(SETTING_KEYS.SEARCH_MEDIA_SCOPE);
  const setSetting = useSetSettingValue();

  const scope = parseSearchMediaScope(value) ?? DEFAULT_SEARCH_MEDIA_SCOPE;

  const setScope = useCallback(
    (next: SearchMediaScope) => {
      setSetting.mutate({
        key: SETTING_KEYS.SEARCH_MEDIA_SCOPE,
        value: next,
        identity: PROFILE_SCOPE,
      });
    },
    [setSetting],
  );

  // While the setting loads, scope falls back to the contract default
  // ("video") so consumers can fetch immediately; a differing stored
  // preference simply refetches once it arrives.
  return { scope, setScope };
}
