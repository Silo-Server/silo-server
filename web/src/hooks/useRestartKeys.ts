import { useMemo } from "react";

import { useAdminRestartKeys, type RestartKeysResponse } from "@/hooks/queries/admin/settings";

/**
 * Prefix-aware lookup over the server's restart-required registry. It is
 * deliberately `Set`-shaped (`has(key)`) so call sites read the same whether
 * the key is listed exactly or covered by a namespace prefix.
 */
export interface RestartKeyMatcher {
  has(key: string): boolean;
}

const EMPTY_MATCHER: RestartKeyMatcher = { has: () => false };

/**
 * Builds a matcher from the endpoint payload. Anything that is not the
 * expected `{ keys, prefixes }` shape — a loading query, or a server too old
 * to serve the endpoint — degrades to "no key needs a restart" rather than to
 * a broken page.
 */
export function createRestartKeyMatcher(data: RestartKeysResponse | undefined): RestartKeyMatcher {
  const exact = new Set(Array.isArray(data?.keys) ? data.keys.filter(isNonEmptyString) : []);
  const prefixes = Array.isArray(data?.prefixes) ? data.prefixes.filter(isNonEmptyString) : [];
  if (exact.size === 0 && prefixes.length === 0) return EMPTY_MATCHER;
  return {
    has: (key: string) => exact.has(key) || prefixes.some((prefix) => key.startsWith(prefix)),
  };
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value !== "";
}

/**
 * Keys whose saved value only takes effect after a server restart. Feed it to
 * `SettingField`'s `restartRequired` prop instead of writing "requires a
 * restart" into hint text.
 */
export function useRestartKeys(): RestartKeyMatcher {
  const { data } = useAdminRestartKeys();
  return useMemo(() => createRestartKeyMatcher(data), [data]);
}
