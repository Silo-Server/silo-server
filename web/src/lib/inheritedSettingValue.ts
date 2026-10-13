import type { AdminUserSettingEntry } from "@/hooks/queries/admin/users";
import { SETTING_DEFINITIONS } from "@/lib/settingsContract";
import {
  defaultValueToString,
  formatSettingValue,
  getSettingDefinition,
} from "@/lib/settingsDisplay";

/**
 * The value a setting falls back to from an account's stored rows: the
 * profile's own value when it has one, otherwise the app default.
 */
export interface InheritedValue {
  /** The profile name when the profile stores the value, else null (app default). */
  profileName: string | null;
  /**
   * Set when an app-family, device, library, or series setting can come
   * between the caller's level and the value named here, so in some places it
   * applies instead. The admin pages can't tell which: the server doesn't infer a
   * device's app family, and a library or series setting spans devices.
   */
  orVaries?: "app family" | "device" | "library" | "series";
  /** Display form, or null when this build has no definition to name a default. */
  display: string | null;
  /** The value in the string form the admin controls edit. */
  raw: string;
}

const VARYING_SCOPES: Partial<Record<string, NonNullable<InheritedValue["orVaries"]>>> = {
  profile_client: "app family",
  profile_device: "device",
  profile_library: "library",
  profile_series: "series",
};

/** The key's resolution order from the manifest; unknown keys fall to profile then default. */
export function resolutionOrderFor(key: string): readonly string[] {
  return (
    (SETTING_DEFINITIONS as Record<string, { resolutionOrder: readonly string[] }>)[key]
      ?.resolutionOrder ?? ["profile", "default"]
  );
}

/**
 * Walks [scopes] in order: the first layer that can be named (the profile's
 * own value, else the app default) is the answer. A context-dependent layer on
 * the way that holds a value for this key marks the result as varying.
 *
 * [profileName] is null when the profile's own value must not be named (the
 * caller is the profile or the account itself).
 */
export function resolveInheritedValue({
  key,
  scopes,
  profileName,
  profileEntries,
  profileAllEntries = profileEntries,
}: {
  key: string;
  scopes: readonly string[];
  profileName: string | null;
  /** The profile's own (All devices) settings. */
  profileEntries: readonly AdminUserSettingEntry[];
  /** Every scope the profile stores. */
  profileAllEntries?: readonly AdminUserSettingEntry[];
}): InheritedValue {
  let orVaries: InheritedValue["orVaries"];
  for (const scope of scopes) {
    if (scope === "profile" && profileName !== null) {
      const stored = profileEntries.find((entry) => entry.key === key);
      if (stored) {
        return {
          profileName,
          display: formatSettingValue(key, stored.value),
          raw: stored.value,
          ...(orVaries ? { orVaries } : {}),
        };
      }
    }
    const varies = VARYING_SCOPES[scope];
    if (
      varies &&
      !orVaries &&
      profileAllEntries.some((entry) => entry.key === key && entry.scope === scope)
    ) {
      orVaries = varies;
    }
    if (scope === "default") break;
  }
  const definition = getSettingDefinition(key);
  if (!definition) return { profileName: null, display: null, raw: "" };
  const raw = defaultValueToString(definition);
  const extra = orVaries ? { orVaries } : {};
  // A structured default (a menu layout, a remembered view) has no short form.
  if (definition.type === "object") return { profileName: null, display: null, raw, ...extra };
  return { profileName: null, display: formatSettingValue(key, raw), raw, ...extra };
}

/**
 * What a setting resolves to for a profile on a device that stores no value of
 * its own for it, following the key's resolution order. The device's own scope
 * is skipped (its rows here belong to the profile's other devices). Library and
 * series settings apply per title, so they never answer for the device as a
 * whole, but one that outranks the profile marks the answer as varying. A key
 * that can only be set per device falls straight to the app default.
 */
export function deviceInheritedValue(
  key: string,
  profileName: string,
  profileEntries: readonly AdminUserSettingEntry[],
  profileAllEntries: readonly AdminUserSettingEntry[] = profileEntries,
): InheritedValue {
  return resolveInheritedValue({
    key,
    scopes: resolutionOrderFor(key).filter((scope) => scope !== "profile_device"),
    profileName,
    profileEntries,
    profileAllEntries,
  });
}

/** "a device", "a library", "a series", "an app family": the scope with its article. */
export function variesScopePhrase(scope: NonNullable<InheritedValue["orVaries"]>): string {
  return `${/^[aeiou]/.test(scope) ? "an" : "a"} ${scope}`;
}
