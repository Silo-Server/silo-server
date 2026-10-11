import type { AdminUserDeviceRow } from "@/api/v2/adminUserActivity";
import type {
  AdminSettingClientFamily,
  AdminSettingIdentity,
  AdminSettingScope,
  AdminUserSettingEntry,
} from "@/hooks/queries/admin/users";
import {
  resolutionOrderFor,
  resolveInheritedValue,
  type InheritedValue,
  variesScopePhrase,
} from "@/lib/inheritedSettingValue";
import { SETTING_DEFINITIONS, type SettingKey } from "@/lib/settingsContract";

/**
 * The Preferences tab's "levels": the places a setting can be stored for an
 * account, in the order Silo resolves them. A profile's settings apply on
 * every device; a device, client family, library, or series under it can
 * replace them for that profile. Each level is exactly one storage identity,
 * so every edit, add, or removal on a level addresses one row.
 */

export type LevelKind = "account" | "profile" | "device" | "client" | "library" | "series";

export interface PreferenceLevel {
  /** Stable id, used as the `level` search param. */
  id: string;
  kind: LevelKind;
  /** The identity a new setting on this level is written at. */
  identity: AdminSettingIdentity;
  profileId?: string;
  /** The owning profile's name; "" for the account level. */
  profileName: string;
  /** The level's own name: "All devices", "Shield TV", "TV Shows library". */
  name: string;
  /** Settings stored at exactly this identity, in manifest order. */
  entries: AdminUserSettingEntry[];
  /** Device levels only. */
  device?: { id: string; platform: string; lastSeenAt: string | null };
}

export interface PreferenceLevelGroup {
  key: string;
  title: string;
  levels: PreferenceLevel[];
}

const FAMILY_ORDER: AdminSettingClientFamily[] = ["tv", "mobile", "tablet", "desktop", "web"];
const FAMILY_LABELS: Record<AdminSettingClientFamily, string> = {
  tv: "TV",
  mobile: "Mobile",
  tablet: "Tablet",
  desktop: "Desktop",
  web: "Web",
};

/** The level id for one profile on one device; also used to link here. */
export function deviceLevelId(profileId: string, deviceId: string): string {
  return `device.${profileId}.${deviceId}`;
}

export function profileLevelId(profileId: string): string {
  return `profile.${profileId}`;
}

const MANIFEST_ORDER = new Map(
  (Object.keys(SETTING_DEFINITIONS) as SettingKey[]).map((key, index) => [key as string, index]),
);

function manifestRank(key: string): number {
  return MANIFEST_ORDER.get(key) ?? Number.MAX_SAFE_INTEGER;
}

function byManifestOrder(a: AdminUserSettingEntry, b: AdminUserSettingEntry): number {
  return manifestRank(a.key) - manifestRank(b.key) || a.key.localeCompare(b.key);
}

/** "playback" → "Playback", "client_local" → "Client Local". */
export function categoryTitle(category: string): string {
  return category
    .split(/[_\s]+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}

function bucket<K>(map: Map<K, AdminUserSettingEntry[]>, key: K, entry: AdminUserSettingEntry) {
  const list = map.get(key);
  if (list) list.push(entry);
  else map.set(key, [entry]);
}

interface ProfileRows {
  profile: AdminUserSettingEntry[];
  devices: Map<string, AdminUserSettingEntry[]>;
  clients: Map<AdminSettingClientFamily, AdminUserSettingEntry[]>;
  libraries: Map<number, AdminUserSettingEntry[]>;
  series: Map<string, AdminUserSettingEntry[]>;
}

function emptyProfileRows(): ProfileRows {
  return {
    profile: [],
    devices: new Map(),
    clients: new Map(),
    libraries: new Map(),
    series: new Map(),
  };
}

/**
 * Folds the account's stored settings into levels, one group per profile in
 * the account's profile order (a profile no longer listed but still holding
 * rows follows, named by its id). Under each profile: All devices, then each
 * device the profile has settings on or was seen with (account device order,
 * then unregistered ones), its client families, libraries, and series.
 * Account-scoped rows, if any exist, get their own level first.
 */
export function buildPreferenceLevels({
  entries,
  profiles,
  devices,
  libraryNames,
}: {
  entries: readonly AdminUserSettingEntry[];
  profiles: readonly { id: string; name: string }[];
  devices: readonly AdminUserDeviceRow[];
  libraryNames: ReadonlyMap<number, string>;
}): PreferenceLevelGroup[] {
  const account: AdminUserSettingEntry[] = [];
  const byProfile = new Map<string, ProfileRows>();
  const rowsFor = (profileId: string) => {
    let rows = byProfile.get(profileId);
    if (!rows) {
      rows = emptyProfileRows();
      byProfile.set(profileId, rows);
    }
    return rows;
  };
  for (const entry of entries) {
    if (entry.scope === "account") {
      account.push(entry);
      continue;
    }
    const rows = rowsFor(entry.profile_id ?? "");
    switch (entry.scope) {
      case "profile":
        rows.profile.push(entry);
        break;
      case "profile_device":
        bucket(rows.devices, entry.device_id ?? "", entry);
        break;
      case "profile_client":
        if (entry.client_family) bucket(rows.clients, entry.client_family, entry);
        break;
      case "profile_library":
        if (entry.library_id !== undefined) bucket(rows.libraries, entry.library_id, entry);
        break;
      case "profile_series":
        if (entry.series_id !== undefined) bucket(rows.series, entry.series_id, entry);
        break;
    }
  }

  const groups: PreferenceLevelGroup[] = [];
  if (account.length > 0) {
    groups.push({
      key: "account",
      title: "Account",
      levels: [
        {
          id: "account",
          kind: "account",
          identity: { scope: "account" },
          profileName: "",
          name: "Account-wide",
          entries: account.sort(byManifestOrder),
        },
      ],
    });
  }

  const listed = new Set(profiles.map((profile) => profile.id));
  const orphaned = [...byProfile.keys()].filter((id) => !listed.has(id)).sort();
  const ordered = [...profiles, ...orphaned.map((id) => ({ id, name: id || "Unknown profile" }))];

  for (const profile of ordered) {
    const rows = byProfile.get(profile.id) ?? emptyProfileRows();
    const base = { profileId: profile.id, profileName: profile.name };
    const levels: PreferenceLevel[] = [
      {
        ...base,
        id: profileLevelId(profile.id),
        kind: "profile",
        identity: { scope: "profile", profileId: profile.id },
        name: "All devices",
        entries: rows.profile.sort(byManifestOrder),
      },
    ];

    const deviceLevel = (
      deviceId: string,
      meta: { name: string; platform: string; lastSeenAt: string | null },
    ): PreferenceLevel => ({
      ...base,
      id: deviceLevelId(profile.id, deviceId),
      kind: "device",
      identity: { scope: "profile_device", profileId: profile.id, deviceId },
      name: meta.name,
      entries: (rows.devices.get(deviceId) ?? []).sort(byManifestOrder),
      device: { id: deviceId, platform: meta.platform, lastSeenAt: meta.lastSeenAt },
    });
    const registered = new Set<string>();
    for (const device of devices) {
      const seen = device.profiles.find((row) => row.profile_id === profile.id);
      if (!seen && !rows.devices.has(device.device_id)) continue;
      registered.add(device.device_id);
      levels.push(
        deviceLevel(device.device_id, {
          name: device.device_name || "Unnamed device",
          platform: device.device_platform,
          lastSeenAt: seen?.last_seen_at ?? device.last_seen_at ?? device.last_updated,
        }),
      );
    }
    for (const deviceId of [...rows.devices.keys()].sort()) {
      if (registered.has(deviceId)) continue;
      levels.push(
        deviceLevel(deviceId, {
          name: deviceId ? `Device ${deviceId.slice(0, 8)}` : "Unknown device",
          platform: "",
          lastSeenAt: null,
        }),
      );
    }

    for (const family of FAMILY_ORDER) {
      const familyRows = rows.clients.get(family);
      if (!familyRows) continue;
      levels.push({
        ...base,
        id: `client.${profile.id}.${family}`,
        kind: "client",
        identity: { scope: "profile_client", profileId: profile.id, clientFamily: family },
        name: `${FAMILY_LABELS[family]} apps`,
        entries: familyRows.sort(byManifestOrder),
      });
    }

    const libraryName = (id: number) => libraryNames.get(id) || `Library #${id}`;
    for (const [libraryId, libraryRows] of [...rows.libraries].sort(([a], [b]) =>
      libraryName(a).localeCompare(libraryName(b)),
    )) {
      levels.push({
        ...base,
        id: `library.${profile.id}.${libraryId}`,
        kind: "library",
        identity: { scope: "profile_library", profileId: profile.id, libraryId },
        name: `${libraryName(libraryId)} library`,
        entries: libraryRows.sort(byManifestOrder),
      });
    }

    for (const [seriesId, seriesRows] of [...rows.series].sort(([a], [b]) => a.localeCompare(b))) {
      levels.push({
        ...base,
        id: `series.${profile.id}.${seriesId}`,
        kind: "series",
        identity: { scope: "profile_series", profileId: profile.id, seriesId },
        name: `Series ${seriesId}`,
        entries: seriesRows.sort(byManifestOrder),
      });
    }

    groups.push({ key: `profile.${profile.id}`, title: `Profile · ${profile.name}`, levels });
  }
  return groups;
}

/** The storage identity of one stored row; every mutation on a row uses it. */
export function identityOf(entry: AdminUserSettingEntry): AdminSettingIdentity {
  return {
    scope: entry.scope,
    profileId: entry.profile_id,
    clientFamily: entry.client_family,
    deviceId: entry.device_id,
    libraryId: entry.library_id,
    seriesId: entry.series_id,
  };
}

/**
 * What a setting on a level replaces: for a device, client, library, or series
 * level, the profile's own stored value; otherwise (or when the profile has
 * none) the app default. `raw` is that value in the string form the controls
 * edit, so writing it as a new setting's start changes nothing.
 */
export type ReplacedValue = InheritedValue;

const LEVEL_SCOPE: Record<LevelKind, AdminSettingScope> = {
  account: "account",
  profile: "profile",
  device: "profile_device",
  client: "profile_client",
  library: "profile_library",
  series: "profile_series",
};

/**
 * Walks the setting's resolution order below this level's scope: the first
 * layer the page can name (the profile's own value, else the app default) is
 * what the level replaces. A context-dependent layer on the way that holds a
 * value for this key marks the result as varying.
 */
export function replacedValue(
  level: PreferenceLevel,
  key: string,
  profileEntries: readonly AdminUserSettingEntry[],
  profileAllEntries: readonly AdminUserSettingEntry[] = profileEntries,
): ReplacedValue {
  const order = resolutionOrderFor(key);
  const at = order.indexOf(LEVEL_SCOPE[level.kind]);
  return resolveInheritedValue({
    key,
    scopes: at >= 0 ? order.slice(at + 1) : ["profile", "default"],
    profileName: level.kind === "profile" || level.kind === "account" ? null : level.profileName,
    profileEntries,
    profileAllEntries,
  });
}

function variesSuffix(replaced: ReplacedValue): string {
  return replaced.orVaries
    ? `, or ${variesScopePhrase(replaced.orVaries)} setting where one applies`
    : "";
}

/** "Replaces Main: English", "Replaces app default: Off". */
export function replacesText(replaced: ReplacedValue): string {
  const base =
    replaced.profileName !== null
      ? `Replaces ${replaced.profileName}: ${replaced.display ?? replaced.raw}`
      : replaced.display === null
        ? "Replaces the app default"
        : `Replaces app default: ${replaced.display}`;
  return base + variesSuffix(replaced);
}

/** The short form the add picker shows: "Main: English", "App default: Off". */
export function replacedShort(replaced: ReplacedValue): string {
  if (replaced.profileName !== null) {
    const named = `${replaced.profileName}: ${replaced.display ?? replaced.raw}`;
    return replaced.orVaries ? `${named} or per ${replaced.orVaries}` : named;
  }
  const base = replaced.display === null ? "App default" : `App default: ${replaced.display}`;
  return replaced.orVaries ? `${base} or per ${replaced.orVaries}` : base;
}

export interface AddableSetting {
  key: string;
  label: string;
  description: string;
  category: string;
  /** Already stored on this level; listed but not selectable. */
  setHere: boolean;
  replaced: ReplacedValue;
}

export interface AddableCategory {
  category: string;
  settings: AddableSetting[];
}

/**
 * Settings a level can hold, by category in manifest order: remote,
 * non-deprecated definitions whose scopes include the level's scope.
 */
export function addableSettings(
  level: PreferenceLevel,
  profileEntries: readonly AdminUserSettingEntry[],
  profileAllEntries: readonly AdminUserSettingEntry[] = profileEntries,
): AddableCategory[] {
  const scope = LEVEL_SCOPE[level.kind];
  const stored = new Set(level.entries.map((entry) => entry.key));
  const categories = new Map<string, AddableSetting[]>();
  for (const key of Object.keys(SETTING_DEFINITIONS) as SettingKey[]) {
    const definition = SETTING_DEFINITIONS[key];
    if (
      definition.persistence !== "remote" ||
      definition.deprecated ||
      !definition.scopes.includes(scope)
    ) {
      continue;
    }
    const category = categoryTitle(definition.category);
    const list = categories.get(category) ?? [];
    list.push({
      key,
      label: definition.label,
      description: definition.description,
      category,
      setHere: stored.has(key),
      replaced: replacedValue(level, key, profileEntries, profileAllEntries),
    });
    categories.set(category, list);
  }
  return [...categories].map(([category, settings]) => ({ category, settings }));
}

/** Where a level's settings apply, for sentences: "on Shield TV", "in TV Shows library". */
export function levelPlace(level: PreferenceLevel): string {
  switch (level.kind) {
    case "account":
      return "for the whole account";
    case "profile":
      return `for ${level.profileName}`;
    case "device":
      return `on ${level.name}`;
    case "client":
      return `on ${level.profileName}'s ${level.name}`;
    case "library":
      return `in ${level.name}`;
    case "series":
      return "in this series";
  }
}

/** The noun the tab uses for a level: "device", "profile", "library". */
export function levelNoun(level: PreferenceLevel): string {
  switch (level.kind) {
    case "account":
      return "account";
    case "client":
      return "app family";
    default:
      return level.kind;
  }
}
