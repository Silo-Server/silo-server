import type {
  AdminSettingClientFamily,
  AdminSettingScope,
  AdminUserSettingEntry,
} from "@/hooks/queries/admin/users";
import { getSettingDefinition } from "@/lib/settingsDisplay";

export interface PreferenceGroup {
  key: string;
  title: string;
  subtitle?: string;
  entries: AdminUserSettingEntry[];
}

export type PreferenceScopeFilter = "all" | "account" | "profile" | "client" | "library" | "series";

export const PREFERENCE_SCOPE_OPTIONS: { value: PreferenceScopeFilter; label: string }[] = [
  { value: "all", label: "All scopes" },
  { value: "account", label: "Account-wide" },
  { value: "profile", label: "Profiles" },
  { value: "client", label: "Client apps" },
  { value: "library", label: "Libraries" },
  { value: "series", label: "Series" },
];

const SCOPE_FILTER: Record<Exclude<PreferenceScopeFilter, "all">, AdminSettingScope> = {
  account: "account",
  profile: "profile",
  client: "profile_client",
  library: "profile_library",
  series: "profile_series",
};

/** Group order: account, profiles, client apps, libraries, series. */
const SCOPE_ORDER: Record<AdminSettingScope, number> = {
  account: 0,
  profile: 1,
  profile_client: 2,
  profile_library: 3,
  profile_series: 4,
  // Device rows never reach this view; they sort last if they ever do.
  profile_device: 5,
};

const FAMILY_LABELS: Record<AdminSettingClientFamily, string> = {
  tv: "TV",
  mobile: "Mobile",
  tablet: "Tablet",
  desktop: "Desktop",
  web: "Web",
};

interface GroupDraft extends PreferenceGroup {
  scope: AdminSettingScope;
  /** Sorts before sortName within a scope; library and series groups use 0. */
  primaryRank: number;
  sortName: string;
  profileRank: number;
}

/**
 * Folds stored settings into one group per place they apply: the account,
 * each profile, each profile's client family, and each profile's library or
 * series. Names come from the profile and library lists; an id stands in when
 * the profile or library is gone. Profiles keep the account's profile order
 * (the order the profile list returns, primary first); a profile that is no
 * longer listed sorts after them.
 */
export function groupPreferences(
  entries: AdminUserSettingEntry[],
  names: { profiles: Map<string, string>; libraries: Map<number, string> },
): PreferenceGroup[] {
  const groups = new Map<string, GroupDraft>();
  const profileName = (id: string | undefined) =>
    id ? names.profiles.get(id) || id : "Unknown profile";
  const profileOrder = [...names.profiles.keys()];
  const rankOf = (id: string | undefined) => {
    const index = id === undefined ? -1 : profileOrder.indexOf(id);
    return index === -1 ? profileOrder.length : index;
  };
  for (const entry of entries) {
    const profile = profileName(entry.profile_id);
    const profileRank = rankOf(entry.profile_id);
    let key: string;
    let title: string;
    let subtitle: string | undefined;
    let sortName: string;
    // Profile and client-app groups sort by profile first; library and
    // series groups by their name, then profile.
    let primaryRank = profileRank;
    switch (entry.scope) {
      case "account":
        key = "account";
        title = "Account-wide";
        subtitle = "Applies to every profile";
        sortName = "";
        break;
      case "profile":
        key = `profile:${entry.profile_id ?? ""}`;
        title = `Profile · ${profile}`;
        sortName = profile;
        break;
      case "profile_client": {
        const family = entry.client_family
          ? (FAMILY_LABELS[entry.client_family] ?? entry.client_family)
          : "Other";
        key = `client:${entry.profile_id ?? ""}:${entry.client_family ?? ""}`;
        title = `Profile · ${profile} · ${family} apps`;
        sortName = `${profile}\u0000${family}`;
        break;
      }
      case "profile_library": {
        const library =
          entry.library_id === undefined
            ? "Unknown library"
            : names.libraries.get(entry.library_id) || `#${entry.library_id}`;
        key = `library:${entry.profile_id ?? ""}:${entry.library_id ?? ""}`;
        title = `Library · ${library}`;
        subtitle = `Profile ${profile}`;
        sortName = library;
        primaryRank = 0;
        break;
      }
      case "profile_series":
        key = `series:${entry.profile_id ?? ""}:${entry.series_id ?? ""}`;
        title = `Series · ${entry.series_id ?? "unknown"}`;
        subtitle = `Profile ${profile}`;
        sortName = entry.series_id ?? "";
        primaryRank = 0;
        break;
      default:
        key = `other:${entry.scope}:${entry.profile_id ?? ""}`;
        title = entry.scope;
        sortName = profile;
    }
    let group = groups.get(key);
    if (!group) {
      group = {
        key,
        title,
        subtitle,
        entries: [],
        scope: entry.scope,
        primaryRank,
        sortName,
        profileRank,
      };
      groups.set(key, group);
    }
    group.entries.push(entry);
  }
  return [...groups.values()]
    .sort(
      (a, b) =>
        (SCOPE_ORDER[a.scope] ?? 9) - (SCOPE_ORDER[b.scope] ?? 9) ||
        a.primaryRank - b.primaryRank ||
        a.sortName.localeCompare(b.sortName) ||
        a.profileRank - b.profileRank ||
        a.key.localeCompare(b.key),
    )
    .map(({ key, title, subtitle, entries: rows }) => ({ key, title, subtitle, entries: rows }));
}

function matchesQuery(entry: AdminUserSettingEntry, query: string): boolean {
  if (!query) return true;
  const definition = getSettingDefinition(entry.key);
  return [entry.key, definition?.label, definition?.description]
    .filter((text): text is string => typeof text === "string")
    .some((text) => text.toLowerCase().includes(query));
}

/** Keeps the entries that match the search text and scope; drops emptied groups. */
export function filterPreferences(
  groups: PreferenceGroup[],
  query: string,
  scope: PreferenceScopeFilter,
): PreferenceGroup[] {
  const needle = query.trim().toLowerCase();
  const wanted = scope === "all" ? null : SCOPE_FILTER[scope];
  const out: PreferenceGroup[] = [];
  for (const group of groups) {
    const entries = group.entries.filter(
      (entry) => (wanted === null || entry.scope === wanted) && matchesQuery(entry, needle),
    );
    if (entries.length > 0) out.push({ ...group, entries });
  }
  return out;
}
