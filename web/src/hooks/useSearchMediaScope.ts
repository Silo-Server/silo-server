import { useCallback } from "react";

import { useSettingValue, useSetSettingValue } from "@/hooks/queries/settingValues";
import type { SettingIdentity } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { CATALOG_REQUEST_PAGE_PARAM } from "@/pages/catalogSearchParams";

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

/**
 * The search page's scope chips: the chip a query's media scope shows, and a
 * pick that saves the preference and puts the scope in the URL. A new scope
 * searches other TMDB types, so the request pager starts over.
 */
export function useSearchScopeChips(
  mediaScope: string | undefined,
  searchParams: URLSearchParams,
  setSearchParams: (next: URLSearchParams) => void,
) {
  const { setScope } = useSearchMediaScope();
  const activeScope: SearchMediaScope =
    mediaScope === "audiobook" ? "audiobook" : mediaScope ? "video" : "all";
  const onScopeChange = useCallback(
    (scope: SearchMediaScope) => {
      setScope(scope);
      const next = new URLSearchParams(searchParams);
      next.delete(CATALOG_REQUEST_PAGE_PARAM);
      next.set("type", scope);
      setSearchParams(next);
    },
    [searchParams, setScope, setSearchParams],
  );
  return { activeScope, onScopeChange };
}
