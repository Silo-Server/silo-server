/** The tabs of an account's admin page, in display order. */
export type UserDetailTab = "overview" | "access" | "activity" | "downloads" | "preferences";

export const USER_DETAIL_TABS: readonly UserDetailTab[] = [
  "overview",
  "access",
  "activity",
  "downloads",
  "preferences",
];

/** The tab a `?tab=` value names; anything missing or unknown opens Overview. */
export function parseUserDetailTab(value: string | null | undefined): UserDetailTab {
  return (USER_DETAIL_TABS as readonly string[]).includes(value ?? "")
    ? (value as UserDetailTab)
    : "overview";
}

/**
 * The search string that opens `tab`, with any extra params after it:
 * "?tab=downloads", "?tab=preferences&level=profile.p1", and "" for a bare
 * Overview. Overview writes no `tab` param, so its URL stays the plain one.
 */
export function userDetailTabSearch(tab: UserDetailTab, extra?: Record<string, string>): string {
  const params = new URLSearchParams();
  if (tab !== "overview") params.set("tab", tab);
  for (const [key, value] of Object.entries(extra ?? {})) params.set(key, value);
  const search = params.toString();
  return search ? `?${search}` : "";
}
