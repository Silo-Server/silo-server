import { useCallback } from "react";

import { useSetSetting, useSetting } from "@/hooks/queries/settings";

export const SEARCH_MEDIA_SCOPE_SETTING_KEY = "search.media_scope";

/** Coarse search scope: "video" = movies & series, "audiobook" = books. */
export type SearchMediaScope = "all" | "video" | "audiobook";

export const DEFAULT_SEARCH_MEDIA_SCOPE: SearchMediaScope = "video";

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
  const settingQuery = useSetting(SEARCH_MEDIA_SCOPE_SETTING_KEY);
  const setSetting = useSetSetting();

  const scope = parseSearchMediaScope(settingQuery.data) ?? DEFAULT_SEARCH_MEDIA_SCOPE;

  const setScope = useCallback(
    (next: SearchMediaScope) => {
      setSetting.mutate({ key: SEARCH_MEDIA_SCOPE_SETTING_KEY, value: next });
    },
    [setSetting],
  );

  // While the setting loads, scope falls back to the default ("video") so
  // consumers can fetch immediately; a differing stored preference simply
  // refetches once it arrives.
  return { scope, setScope };
}
