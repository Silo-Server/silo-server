/**
 * The autoscan panel's URL contract, in one place so the page and the legacy
 * `/admin/autoscan` redirect cannot drift apart about which values mean what.
 */

export const AUTOSCAN_TABS = ["sources", "activity"] as const;
export type AutoscanTab = (typeof AUTOSCAN_TABS)[number];

/**
 * Connections and settings used to be peer tabs, which read as "set these up
 * first" — most operators never needed either. They now live in an Advanced
 * section on the Sources view, so their old deep links land on Sources with
 * that section already open rather than 404-ing into a missing tab.
 */
const LEGACY_ADVANCED_TABS = new Set(["connections", "settings"]);

export function normalizeTab(value: string | null): AutoscanTab {
  return AUTOSCAN_TABS.includes(value as AutoscanTab) ? (value as AutoscanTab) : "sources";
}

export function isLegacyAdvancedTab(value: string | null): boolean {
  return value !== null && LEGACY_ADVANCED_TABS.has(value);
}

/** Every value the panel still reacts to, current or legacy. */
function isKnownAutoscanView(value: string): boolean {
  return AUTOSCAN_TABS.includes(value as AutoscanTab) || LEGACY_ADVANCED_TABS.has(value);
}

/**
 * Where `/admin/autoscan` sends an old link. Autoscan is a tab on Libraries
 * now, so `tab` names that tab and the panel's own Sources/Activity selection
 * moves to `view`. A bookmarked `?tab=activity` therefore has to be translated
 * rather than dropped, or every old deep link silently lands on Sources.
 *
 * "sources" stays implicit — it is what an absent `view` already means, and the
 * panel drops the param when you select it.
 */
export function buildLegacyAutoscanRedirectTarget(search: string): string {
  const legacy = new URLSearchParams(search);
  const legacyView = legacy.get("tab") ?? legacy.get("view");
  legacy.delete("tab");
  legacy.delete("view");

  const target = new URLSearchParams({ tab: "autoscan" });
  if (legacyView && legacyView !== "sources" && isKnownAutoscanView(legacyView)) {
    target.set("view", legacyView);
  }
  // Anything else on the old URL is none of this redirect's business; carry it.
  for (const [key, value] of legacy) {
    target.append(key, value);
  }

  return `/admin/libraries?${target.toString()}`;
}
