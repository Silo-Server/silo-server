import {
  SETTING_DEFINITIONS,
  type SettingDefinition,
  type SettingKey,
} from "@/lib/settingsContract";
import { languageOptionsFor, type SettingOption } from "@/lib/languageOptions";

/**
 * Display helpers over the generated settings contract.
 *
 * Everything a settings control needs to render — label, description, control
 * shape, option list, bounds, unit — is already in the manifest, so this module
 * only adapts those fields to what the UI asks for. It deliberately owns no
 * per-key knowledge: a hand-written table beside the generated one is exactly
 * the drift the contract exists to remove, and the one that shipped before this
 * disagreed with the server about the type and range of several keys.
 */

export type SettingDisplay = SettingDefinition;

/** Control shapes the admin/settings widgets can render. */
export type SettingControlKind = "switch" | "select" | "slider" | "stepper" | "panel" | "text";

/** The generated definition for a key, or null when this build has no such key. */
export function getSettingDefinition(key: string): SettingDisplay | null {
  return SETTING_DEFINITIONS[key as SettingKey] ?? null;
}

/**
 * The control to render for a definition.
 *
 * A definition with no recommended_control falls back to its value type, which
 * is what keeps a newly added key renderable without a manifest edit here.
 */
export function controlKindFor(definition: SettingDisplay): SettingControlKind {
  switch (definition.control) {
    case "switch":
    case "select":
    case "slider":
    case "stepper":
    case "panel":
    case "text":
      return definition.control;
  }
  if (definition.type === "boolean") return "switch";
  if (definition.type === "enum" || definition.type === "language_tag") return "select";
  if (definition.type === "integer" || definition.type === "number") return "slider";
  return "text";
}

/** Whether a definition is edited through a bespoke panel or raw JSON. */
export function isStructuredSetting(definition: SettingDisplay | null): boolean {
  if (!definition) return true;
  return definition.type === "object" || controlKindFor(definition) === "panel";
}

/**
 * The option list for a select, in manifest order.
 *
 * Enum members come straight from the contract. Open language tags use the
 * definition's generated advisory option set. A nullable definition gains a
 * leading, context-specific unset entry so a control can express clearing it.
 */
export function optionsFor(definition: SettingDisplay): SettingOption[] {
  if (definition.type === "language_tag") {
    return languageOptionsFor(definition.key);
  }
  const members = (definition.values ?? []).map((member) => ({
    value: String(member.value),
    // The manifest leaves a label empty when the member is its own label
    // (the date formats spell themselves).
    label: member.label || String(member.value),
  }));
  if (definition.nullable) {
    return [{ value: "", label: "Unset" }, ...members];
  }
  return members;
}

/**
 * A stored value rendered for display, in the string form the admin surface
 * edits. Falls back to the raw value for a key this build does not know, which
 * is the older-client-newer-server case.
 */
export function formatSettingValue(key: string, value: string | null | undefined): string {
  const definition = getSettingDefinition(key);
  if (!definition) {
    return value ?? "Unset";
  }
  if (controlKindFor(definition) === "switch") {
    return value === "true" ? "Enabled" : "Disabled";
  }
  if (definition.type === "language_tag") {
    const match = languageOptionsFor(definition.key, value).find(
      (option) => option.value === (value ?? ""),
    );
    return match?.label ?? value ?? definition.unsetLabel ?? "Unset";
  }
  if (definition.values?.length) {
    const fallback = value ?? defaultValueToString(definition);
    const match = definition.values.find((member) => String(member.value) === fallback);
    return match ? match.label || String(match.value) : fallback || "Unset";
  }
  if (definition.unit) {
    return `${value ?? defaultValueToString(definition) ?? "0"} ${definition.unit}`;
  }
  if (definition.type === "object") {
    return value ? "Custom" : "Using fallback";
  }
  return value || defaultValueToString(definition) || "Unset";
}

/** The definition's default in the string form the admin controls compare against. */
export function defaultValueToString(definition: SettingDisplay): string {
  const value = definition.defaultValue;
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  if (typeof value === "boolean" || typeof value === "number") return String(value);
  return JSON.stringify(value);
}

/**
 * Every remote setting a device can override, in manifest order. The admin
 * "show all overrides" view iterates this so an admin can create an override on
 * any device-scoped setting, not only the ones that already have a row.
 */
export const ALL_DEVICE_SETTING_KEYS: SettingKey[] = (
  Object.keys(SETTING_DEFINITIONS) as SettingKey[]
).filter((key) => {
  const definition = SETTING_DEFINITIONS[key];
  return definition.persistence === "remote" && definition.scopes.includes("profile_device");
});

/** Device-setting keys understood by a connected server contract revision. */
export function deviceSettingKeysForRevision(revision: number | undefined): SettingKey[] {
  if (revision === undefined) return [];
  return ALL_DEVICE_SETTING_KEYS.filter((key) => {
    const definition = SETTING_DEFINITIONS[key];
    const scopeIndex = definition.scopes.indexOf("profile_device");
    return (
      scopeIndex >= 0 &&
      (definition.scopeIntroducedIn[scopeIndex] ?? Number.POSITIVE_INFINITY) <= revision
    );
  });
}
